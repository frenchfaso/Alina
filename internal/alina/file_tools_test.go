package alina

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newFileTestEngine(t *testing.T) *Engine {
	e := newTestEngine(t, &scriptedModel{wait: true})
	e.Config.WorkDir = t.TempDir()
	return e
}

func invokeFile(e *Engine, kind, name string, args any) (string, error) {
	return e.tool(&runningJob{Job: Job{Kind: kind}, ctx: context.Background()}, ToolCall{Name: name, Arguments: jsonText(args)})
}

func editArgs(path, old, next string) map[string]any {
	return map[string]any{"path": path, "edits": []map[string]string{{"oldText": old, "newText": next}}}
}

func TestFileToolsThroughAgentLoop(t *testing.T) {
	step := 0
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, messages []Message, specs []ToolSpec, _ func(string)) (Message, error) {
		step++
		names := map[string]bool{}
		for _, s := range specs {
			names[s.Name] = true
		}
		if len(names) != 8 || names["web_search"] || !names["read"] || !names["write"] || !names["edit"] || !strings.Contains(messages[0].Content, "prefer read") {
			return Message{}, errors.New("file tools or prompt guidance missing")
		}
		last := messages[len(messages)-1]
		name := ""
		var args any
		switch step {
		case 1:
			name, args = "write", map[string]string{"path": "note.txt", "content": "alpha\nbeta\n"}
		case 2:
			if !strings.Contains(last.Content, "Wrote") {
				return Message{}, errors.New(last.Content)
			}
			name, args = "read", map[string]string{"path": "note.txt"}
		case 3:
			if !strings.Contains(last.Content, "alpha\nbeta\n") {
				return Message{}, errors.New("read did not return file contents")
			}
			name, args = "edit", editArgs("note.txt", "beta", "gamma")
		case 4:
			if !strings.Contains(last.Content, "Edited") {
				return Message{}, errors.New(last.Content)
			}
			name, args = "read", map[string]string{"path": "note.txt"}
		default:
			if !strings.Contains(last.Content, "alpha\ngamma\n") {
				return Message{}, errors.New("edit did not reach the file")
			}
			return Message{Role: "assistant", Content: "Verified the updated file."}, nil
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: fmt.Sprint(step), Name: name, Arguments: jsonText(args)}}}, nil
	}))
	e.Config.WorkDir = t.TempDir()
	j, err := e.Submit("files", "local", "Create and update a note.")
	if err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, j.ID, "completed")
	if done.Output != "Verified the updated file." {
		t.Fatal(done)
	}
}

func TestReadFilePaginationAndLimits(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "text.txt")
	content := "\ufeffprima\r\nseconda 界\r\nterza\r\n"
	os.WriteFile(path, []byte(content), 0400)
	out, err := invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 1, "limit": 2})
	if err != nil || !strings.Contains(out, "\ufeffprima\r\nseconda 界\r\n") || !strings.Contains(out, "next_offset=3") || strings.Contains(out, "terza") {
		t.Fatal(out, err)
	}
	out, err = invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 3})
	if err != nil || !strings.Contains(out, "terza\r\n") || strings.Contains(out, "next_offset") {
		t.Fatal(out, err)
	}
	if _, err = invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 4}); err == nil {
		t.Fatal("accepted offset beyond EOF")
	}
	os.Chmod(path, 0600)
	os.WriteFile(path, []byte(strings.Repeat("row\n", maxReadLines+1)), 0600)
	out, err = invokeFile(e, "chat", "read", map[string]string{"path": path})
	if err != nil || !strings.Contains(out, "next_offset=2001") || strings.Count(out, "row\n") != maxReadLines {
		t.Fatal("line limit", err)
	}
	line := strings.Repeat("界", 3000) + "\n"
	os.WriteFile(path, []byte(strings.Repeat(line, 5)), 0600)
	out, err = invokeFile(e, "chat", "read", map[string]string{"path": path})
	if err != nil || !strings.Contains(out, "next_offset=4") || strings.Count(out, line) != 3 || len(out) > maxReadBytes+1024 {
		t.Fatal("byte limit split a line or UTF-8 character", err)
	}
	os.WriteFile(path, []byte("short\n"+strings.Repeat("x", maxReadBytes+1)+"\nlast\n"), 0600)
	out, err = invokeFile(e, "chat", "read", map[string]string{"path": path})
	if err != nil || !strings.Contains(out, "next_offset=2") {
		t.Fatal(out, err)
	}
	if _, err = invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 2}); err == nil || !strings.Contains(err.Error(), "line 2 exceeds") {
		t.Fatal("oversize line was returned", err)
	}
	out, err = invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 3})
	if err != nil || !strings.Contains(out, "last\n") {
		t.Fatal("could not skip long lines", out, err)
	}
	for _, b := range [][]byte{{0, 1, 2}, {0xff, 0xfe}, testPNG(t)} {
		os.WriteFile(path, b, 0600)
		if _, err = invokeFile(e, "chat", "read", map[string]string{"path": path}); err == nil {
			t.Fatal("binary/invalid UTF-8 returned as text")
		}
	}
	os.WriteFile(path, nil, 0600)
	out, err = invokeFile(e, "chat", "read", map[string]string{"path": path})
	if err != nil || !strings.Contains(out, "Empty file") {
		t.Fatal(out, err)
	}
}

func TestExactEditsPreserveBytesAndFailTogether(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "edit.txt")
	original := "\ufefftitle\r\n  alpha  \r\n  beta  \r\nend\r\n"
	os.WriteFile(path, []byte(original), 0751)
	if err := os.Chmod(path, 0751); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": path, "edits": []map[string]string{
		{"oldText": "  beta  \r\n", "newText": "  γάμμα  \r\n"},
		{"oldText": "alpha", "newText": "ALPHA"},
	}}
	out, err := invokeFile(e, "chat", "edit", args)
	want := "\ufefftitle\r\n  ALPHA  \r\n  γάμμα  \r\nend\r\n"
	got, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if err != nil || string(got) != want || info.Mode().Perm() != 0751 || !strings.Contains(out, "first_changed_line=2") {
		t.Fatal(out, string(got), info.Mode(), err)
	}
	for _, tc := range []struct {
		name, text string
		edits      []map[string]string
	}{
		{"missing", "alpha beta", []map[string]string{{"oldText": "alpha", "newText": "changed"}, {"oldText": "missing", "newText": ""}}},
		{"duplicate", "same same", []map[string]string{{"oldText": "same", "newText": "next"}}},
		{"overlapping_occurrences", "aaa", []map[string]string{{"oldText": "aa", "newText": ""}}},
		{"overlapping_edits", "abcdef", []map[string]string{{"oldText": "abcde", "newText": ""}, {"oldText": "bcdef", "newText": ""}}},
		{"no_fuzzy", "“quoted”", []map[string]string{{"oldText": "\"quoted\"", "newText": "next"}}},
		{"line_endings", "a\r\nb", []map[string]string{{"oldText": "a\nb", "newText": "next"}}},
		{"missing_new_text", "alpha", []map[string]string{{"oldText": "alpha"}}},
		{"empty_old_text", "alpha", []map[string]string{{"oldText": "", "newText": "next"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.WriteFile(path, []byte(tc.text), 0751)
			_, err := invokeFile(e, "chat", "edit", map[string]any{"path": path, "edits": tc.edits})
			b, _ := os.ReadFile(path)
			if err == nil || string(b) != tc.text {
				t.Fatal("invalid edit changed file or succeeded", err, string(b))
			}
		})
	}
	os.WriteFile(path, []byte("remove"), 0751)
	if _, err = invokeFile(e, "chat", "edit", editArgs(path, "remove", "")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if len(got) != 0 {
		t.Fatal("explicit deletion failed")
	}
}

func TestWriteFilesPreservesModeAndSymlinkTarget(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "new", "file.txt")
	if _, err := invokeFile(e, "chat", "write", map[string]string{"path": "new/file.txt", "content": "first"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("new file is not private")
	}
	os.Chmod(path, 0751)
	link := filepath.Join(e.Config.WorkDir, "alias.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeFile(e, "chat", "write", map[string]string{"path": link, "content": "second"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	info, _ = os.Stat(path)
	alias, _ := os.Lstat(link)
	if string(b) != "second" || info.Mode().Perm() != 0751 || alias.Mode()&os.ModeSymlink == 0 {
		t.Fatal("write replaced the symlink or lost mode/content")
	}
	if _, err := invokeFile(e, "chat", "write", map[string]string{"path": path}); err == nil {
		t.Fatal("missing content silently emptied file")
	}
	if _, err := invokeFile(e, "chat", "write", map[string]string{"path": path, "content": ""}); err != nil {
		t.Fatal("explicit empty content rejected", err)
	}
}

func TestFileToolsRespectAdministrativeAndInitiativePaths(t *testing.T) {
	e := newFileTestEngine(t)
	admin := filepath.Join(e.Dir, "config.json")
	os.WriteFile(admin, []byte("PRIVATE_TOKEN"), 0600)
	alias := filepath.Join(e.Config.WorkDir, "alias")
	os.Symlink(e.Dir, alias)
	for _, path := range []string{admin, filepath.Join(alias, "config.json"), filepath.Join(e.Workspace(), "..", "config.json")} {
		for _, name := range []string{"read", "write", "edit"} {
			args := map[string]any{"path": path, "content": "changed", "edits": []map[string]string{{"oldText": "PRIVATE_TOKEN", "newText": "changed"}}}
			if _, err := invokeFile(e, "chat", name, args); err == nil {
				t.Fatal("administrative state exposed", name, path)
			}
		}
	}
	archive := filepath.Join(e.Dir, "sessions", "archived.json")
	os.MkdirAll(filepath.Dir(archive), 0700)
	os.WriteFile(archive, []byte("archived event"), 0600)
	if _, err := invokeFile(e, "chat", "read", map[string]string{"path": archive}); err != nil {
		t.Fatal("archive is not readable", err)
	}
	if _, err := invokeFile(e, "chat", "write", map[string]string{"path": archive, "content": "changed"}); err == nil {
		t.Fatal("archive was writable")
	}
	os.Symlink(e.Config.WorkDir, filepath.Join(e.Workspace(), "outside"))
	for _, path := range []string{filepath.Join(e.Config.WorkDir, "new.txt"), "../new.txt", "outside/nested/new.txt"} {
		if _, err := invokeFile(e, "initiative", "write", map[string]string{"path": path, "content": "no"}); err == nil {
			t.Fatal("initiative escaped workspace", path)
		}
	}
	if _, err := invokeFile(e, "initiative", "write", map[string]string{"path": "experiments/note.txt", "content": "within scope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Workspace(), "experiments", "note.txt")); err != nil {
		t.Fatal("initiative did not default to workspace", err)
	}
	for _, name := range []string{"read", "write", "edit"} {
		if _, err := e.reflectionTool(&runningJob{ctx: e.ctx}, ToolCall{Name: name, Arguments: "{}"}); err == nil {
			t.Fatal("reflection gained general file access", name)
		}
	}
}

func TestFileCommitDetectsChangesAndCancellation(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "file.txt")
	os.WriteFile(path, []byte("before"), 0600)
	root, err := os.OpenRoot(e.Config.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, info, err := textSnapshot(root, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("external change"), 0600)
	if err := commitText(e.ctx, root, "file.txt", before, info, "stale write"); err == nil {
		t.Fatal("external update was overwritten")
	}
	if err := commitText(e.ctx, root, "file.txt", nil, nil, "new file"); err == nil {
		t.Fatal("newly appeared destination was overwritten")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "external change" {
		t.Fatal(string(b))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.fileTool(&runningJob{ctx: ctx}, ToolCall{Name: "write", Arguments: jsonText(map[string]string{"path": filepath.Join(e.Config.WorkDir, "not-created", "file"), "content": "no"})})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(e.Config.WorkDir, "not-created")); !os.IsNotExist(err) {
		t.Fatal("cancelled operation created directories")
	}
	if err := commitText(ctx, root, "file.txt", before, info, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	temps, _ := filepath.Glob(filepath.Join(e.Config.WorkDir, ".alina-write-*"))
	if len(temps) > 0 {
		t.Fatal("uncommitted temporary files retained", temps)
	}
}

func TestFileToolsBoundLargeFilesAndRejectSpecialFiles(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "large.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(strings.Repeat("x", 512))
	if err = f.Truncate(maxReadScan + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = invokeFile(e, "chat", "read", map[string]any{"path": path, "offset": 2}); err == nil || !strings.Contains(err.Error(), "scanning more than") {
		t.Fatal("read did not bound scanning", err)
	}
	for _, name := range []string{"write", "edit"} {
		args := map[string]any{"path": path, "content": "small", "edits": []map[string]string{{"oldText": "x", "newText": "y"}}}
		if _, err = invokeFile(e, "chat", name, args); err == nil || !strings.Contains(err.Error(), "8 MiB") {
			t.Fatal("large file mutation accepted", name, err)
		}
	}
	for _, path := range []string{e.Config.WorkDir, "/dev/null"} {
		for _, name := range []string{"read", "write"} {
			if _, err = invokeFile(e, "chat", name, map[string]string{"path": path, "content": "data"}); err == nil {
				t.Fatal("non-regular file accepted", name, path)
			}
		}
	}
}

func TestConcurrentFileEditsKeepIndependentChanges(t *testing.T) {
	e := newFileTestEngine(t)
	path := filepath.Join(e.Config.WorkDir, "concurrent.txt")
	const count = 12
	var content strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&content, "key[%d]=old\n", i)
	}
	os.WriteFile(path, []byte(content.String()), 0600)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := invokeFile(e, "chat", "edit", editArgs(path, fmt.Sprintf("key[%d]=old", i), fmt.Sprintf("key[%d]=new", i)))
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "=new") != count || strings.Contains(string(b), "=old") {
		t.Fatal("concurrent updates were lost", string(b))
	}
}
