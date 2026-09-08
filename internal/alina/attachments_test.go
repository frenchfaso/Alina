package alina

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func photoUpdate() tgUpdate {
	m := &tgMessage{Caption: "Descrivi ORCHID_PHOTO", Photo: []tgFile{{ID: "small", Width: 1, Height: 1}, {ID: "large", Width: 4, Height: 4}}}
	m.From.ID, m.Chat.ID, m.Chat.Type = 42, 42, "private"
	return tgUpdate{ID: 1, Message: m}
}

func TestTelegramPhotoReachesAstraAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	img := testPNG(t)
	manifestPath := filepath.Join(dir, "workspace", "inbox", "telegram", "tg1-"+contentID("large")[:16]+".meta.json")
	var gets, downloads, requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botfixture/getFile":
			gets.Add(1)
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["file_id"] != "large" {
				t.Error("did not choose largest photo", in)
			}
			fmt.Fprintf(w, "{\"ok\":true,\"result\":{\"file_path\":\"photos/large.png\",\"file_size\":%d}}", len(img))
		case "/file/botfixture/photos/large.png":
			downloads.Add(1)
			w.Write(img)
		case "/botfixture/sendMessage":
			fmt.Fprint(w, "{\"ok\":true,\"result\":{}}")
		case "/responses":
			n := requests.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			input, _ := body["input"].([]any)
			if len(input) == 0 {
				t.Error("no model input")
				w.WriteHeader(400)
				return
			}
			last := input[len(input)-1].(map[string]any)
			field := "content"
			if n == 2 || n == 4 {
				field = "output"
				if last["type"] != "function_call_output" || last["call_id"] != "look" {
					t.Error("image tool result is not linked to its call")
				}
			}
			blocks, ok := last[field].([]any)
			if !ok || len(blocks) != 2 {
				t.Error("missing multimodal content", last)
			} else {
				block := blocks[1].(map[string]any)
				want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
				if block["type"] != "input_image" || block["image_url"] != want || block["detail"] != "high" {
					t.Error("wrong visual input")
				}
			}
			if strings.Contains(jsonText(body), "/file/botfixture") {
				t.Error("bot download URL leaked to provider")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Fixture image received."}}}}
			if n == 1 || n == 3 {
				b, err := os.ReadFile(manifestPath)
				var saved Attachment
				if err != nil || json.Unmarshal(b, &saved) != nil {
					t.Error("missing committed attachment", err)
				}
				output = []any{map[string]any{"type": "function_call", "call_id": "look", "name": "view_image", "arguments": jsonText(map[string]string{"path": saved.Path})}}
			}
			fmt.Fprint(w, "data: "+jsonText(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})+"\n\n")
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := DefaultConfig()
	c.WorkDir = dir
	if err := writeJSON(filepath.Join(dir, "chatgpt.json"), Credential{Access: "own-access", AccountID: "own-account", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	p := &Provider{Config: c, Auth: &Auth{Dir: dir, Client: srv.Client()}, Client: srv.Client(), BaseURL: srv.URL + "/responses"}
	e, err := NewEngine(dir, c, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e != nil {
			e.Close()
		}
	}()
	tg := NewTelegram(dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, srv.Client())
	tg.BaseURL = srv.URL
	u := photoUpdate()
	if err = tg.process(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, "tg1", "completed")
	if len(done.Attachments) != 1 || done.Input != u.Message.Caption {
		t.Fatal("attachment/caption was not persisted", done)
	}
	path := done.Attachments[0].Path
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != int64(len(img)) {
		t.Fatal("attachment file not private or incomplete", err)
	}
	history, err := os.ReadFile(filepath.Join(dir, "sessions", done.Session+".json"))
	if err != nil || bytes.Contains(history, []byte("base64,")) || !bytes.Contains(history, []byte(path)) {
		t.Fatal("history must store file references, not base64", err)
	}
	recall, err := e.Memory.Recall(e.ctx, "ORCHID_PHOTO")
	if err != nil || len(recall.Hits) == 0 || !strings.Contains(jsonText(recall), path) {
		t.Fatal("attachment is not searchable across channels", err)
	}
	e.Close()
	e, err = NewEngine(dir, c, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	tg = NewTelegram(dir, tg.Config, e, srv.Client())
	tg.BaseURL = srv.URL
	if err = tg.process(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 || downloads.Load() != 1 || len(e.Jobs("")) != 1 {
		t.Fatal("duplicate update downloaded or executed again after restart")
	}
	j, err := e.submit("new-source", "local", "Inspect this saved image.", "", "chat", done.Attachments...)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	if requests.Load() != 4 {
		t.Fatal("image tool did not complete after restart", requests.Load())
	}
}

func TestTelegramDocumentCanBeUsedByShell(t *testing.T) {
	payload := []byte("ORCHID_DOCUMENT\n")
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, messages []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		last := messages[len(messages)-1]
		if last.Role == "tool" {
			return Message{Role: "assistant", Content: last.Content}, nil
		}
		if len(last.Attachments) != 1 || last.Attachments[0].Image {
			return Message{}, errors.New("missing document")
		}
		return Message{Role: "assistant", Calls: []ToolCall{{ID: "read", Name: "shell", Arguments: jsonText(map[string]string{"command": "cat '" + strings.ReplaceAll(last.Attachments[0].Path, "'", "'\\''") + "'"})}}}, nil
	}))
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			fmt.Fprintf(w, "{\"ok\":true,\"result\":{\"file_path\":\"documents/file.txt\",\"file_size\":%d}}", len(payload))
		} else if strings.Contains(r.URL.Path, "/file/bot") {
			downloads.Add(1)
			w.Write(payload)
		} else {
			fmt.Fprint(w, "{\"ok\":true,\"result\":{}}")
		}
	}))
	defer srv.Close()
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, srv.Client())
	tg.BaseURL = srv.URL
	u := photoUpdate()
	u.Message.Photo = nil
	u.Message.Document = &tgFile{ID: "doc", Name: "../../outside;$(touch bad).json", MIME: "image/png", Size: int64(len(payload))}
	u.Message.Caption = "Leggi il documento"
	a, err := tg.receiveFile(context.Background(), u.ID, *u.Message.Document)
	if err != nil {
		t.Fatal(err)
	}
	if !within(filepath.Join(e.Workspace(), "inbox", "telegram"), a.Path) || a.Image || a.MIME != "text/plain" {
		t.Fatal("untrusted file name or MIME was used", a)
	}
	if err = tg.process(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	done := awaitStatus(t, e, "tg1", "completed")
	if !strings.Contains(done.Output, "ORCHID_DOCUMENT") || downloads.Load() != 1 {
		t.Fatal("document unavailable to shell or downloaded twice", done.Output)
	}
	if err = os.WriteFile(a.Path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = tg.receiveFile(context.Background(), u.ID, *u.Message.Document)
	var rejected *attachmentRejected
	if !errors.As(err, &rejected) || downloads.Load() != 1 {
		t.Fatal("retry overwrote changed original", err)
	}
}

func TestTelegramFilesRejectUnauthorizedOversizeAndUnsafePaths(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "{\"ok\":true,\"result\":{}}")
	}))
	defer srv.Close()
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, srv.Client())
	tg.BaseURL = srv.URL
	for _, mode := range []string{"stranger", "group"} {
		u := photoUpdate()
		if mode == "stranger" {
			u.Message.From.ID = 43
		} else {
			u.Message.Chat.Type = "group"
		}
		if err := tg.process(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 || len(e.Jobs("")) != 0 {
		t.Fatal("unauthorized attachment was fetched")
	}
	_, err := tg.receiveFile(context.Background(), 1, tgFile{ID: "huge", Size: maxAttachmentBytes + 1})
	var rejected *attachmentRejected
	if !errors.As(err, &rejected) || calls.Load() != 0 {
		t.Fatal("oversize file was fetched", err)
	}
	for _, remote := range []string{"/absolute", "../escape", "dir/%2e%2e/escape", "dir/file?token=bad", "https://example.org/file", "dir\\file"} {
		t.Run(remote, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/getFile") {
					t.Error("invalid download attempted")
				}
				fmt.Fprint(w, jsonText(map[string]any{"ok": true, "result": map[string]string{"file_path": remote}}))
			}))
			defer server.Close()
			tg.BaseURL = server.URL
			_, err := tg.receiveFile(context.Background(), 1, tgFile{ID: "unsafe"})
			if !errors.As(err, &rejected) {
				t.Fatal("invalid remote path accepted", err)
			}
		})
	}
}

func TestTelegramDownloadLimitsAndRetry(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	for _, mode := range []string{"stream-too-large", "redirect", "api-400", "retry", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			var downloads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					if mode == "api-400" {
						w.WriteHeader(400)
						fmt.Fprint(w, "{\"ok\":false,\"error_code\":400}")
					} else {
						size := 0
						if mode == "incomplete" {
							size = 123
						}
						fmt.Fprintf(w, "{\"ok\":true,\"result\":{\"file_path\":\"documents/file\",\"file_size\":%d}}", size)
					}
					return
				}
				n := downloads.Add(1)
				switch mode {
				case "stream-too-large":
					w.(http.Flusher).Flush()
					io.Copy(w, strings.NewReader(strings.Repeat("x", maxAttachmentBytes+1)))
				case "redirect":
					if r.URL.Path == "/redirect-target" {
						t.Error("redirect was followed")
					}
					http.Redirect(w, r, "/redirect-target", 302)
				case "retry":
					if n == 1 {
						w.WriteHeader(503)
						return
					}
					fmt.Fprint(w, "retry succeeded")
				case "incomplete":
					fmt.Fprint(w, "short")
				}
			}))
			defer srv.Close()
			tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, srv.Client())
			tg.BaseURL = srv.URL
			file := tgFile{ID: mode}
			_, err := tg.receiveFile(context.Background(), 1, file)
			var rejected *attachmentRejected
			if err == nil || strings.Contains(err.Error(), "fixture") {
				t.Fatal("download should fail without leaking token", err)
			}
			if want := mode != "retry" && mode != "incomplete"; errors.As(err, &rejected) != want {
				t.Fatal("wrong retry classification", mode, err)
			}
			partials, _ := filepath.Glob(filepath.Join(e.Workspace(), "inbox", "telegram", "tg1-"+contentID(mode)[:16]+"-*"))
			if len(partials) != 0 {
				t.Fatal("failed transfer retained a partial file", partials)
			}
			if mode == "retry" {
				if _, err = tg.receiveFile(context.Background(), 1, file); err != nil || downloads.Load() != 2 {
					t.Fatal("transient download did not recover", err)
				}
			}
			temps, _ := filepath.Glob(filepath.Join(e.Workspace(), "inbox", "telegram", ".download-*"))
			if len(temps) != 0 {
				t.Fatal("incomplete download left temporary files", temps)
			}
		})
	}
}

func TestVisualInputIsBoundedAndRejectsChangedOrEscapedFiles(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	img := testPNG(t)
	path := filepath.Join(e.Workspace(), "image.png")
	if err := os.WriteFile(path, img, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := e.imageAttachment(path)
	if err != nil {
		t.Fatal(err)
	}
	p := Provider{Workspace: e.Workspace()}
	var messages []Message
	for i := 0; i < 8; i++ {
		messages = append(messages, Message{Role: "user", Content: fmt.Sprint(i), Attachments: []Attachment{a}})
	}
	prepared := p.prepareImages(messages)
	for i, m := range prepared {
		if (len(m.imageInputs) == 1) != (i >= 4) {
			t.Fatal("visual input not bounded to four most recent images", i)
		}
		if messages[i].Content != fmt.Sprint(i) || len(messages[i].imageInputs) != 0 {
			t.Fatal("preparation mutated persistent history")
		}
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	m := p.prepareImages(messages[len(messages)-1:])[0]
	if len(m.imageInputs) != 0 || !strings.Contains(m.Content, "file changed") {
		t.Fatal("modified image was silently substituted", m.Content)
	}
	os.Remove(path)
	m = p.prepareImages(messages[len(messages)-1:])[0]
	if len(m.imageInputs) != 0 || !strings.Contains(m.Content, "unavailable") {
		t.Fatal("missing image not explained", m.Content)
	}
	outside := filepath.Join(t.TempDir(), "private.png")
	os.WriteFile(outside, img, 0600)
	os.Symlink(filepath.Dir(outside), filepath.Join(e.Workspace(), "escape"))
	for _, path := range []string{outside, filepath.Join(e.Workspace(), "escape", "private.png")} {
		if _, err := e.imageAttachment(path); err == nil {
			t.Fatal("image escaped workspace", path)
		}
	}
}

func TestAttachmentReferencesSurviveCompaction(t *testing.T) {
	var imagePath string
	sawPath := false
	e := newTestEngine(t, modelFunc(func(_ context.Context, _ string, msg []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		if strings.Contains(jsonText(msg), "base64,") {
			return Message{}, errors.New("checkpoint includes binary payload")
		}
		if strings.Contains(jsonText(msg), imagePath) {
			sawPath = true
			return Message{Role: "assistant", Content: "The photo remains available at " + imagePath}, nil
		}
		return Message{Role: "assistant", Content: "Continue reviewing the recorded context."}, nil
	}))
	e.Config.ContextTokens = 32768
	imagePath = filepath.Join(e.Workspace(), "original.png")
	if err := os.WriteFile(imagePath, testPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := e.imageAttachment(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{{Role: "user", Content: "COMPACT_PHOTO " + strings.Repeat("x", 110000), Attachments: []Attachment{a}}}
	for i := 0; i < 20; i++ {
		history = append(history, Message{Role: "user", Content: "Small follow-up."})
	}
	j := &runningJob{Job: Job{ID: "photo-checkpoint", Session: "photo-history", Owner: "local", Kind: "chat"}, ctx: e.ctx}
	path := filepath.Join(e.Dir, "sessions", j.Session+".json")
	next, err := e.compact(j, history, path)
	if err != nil || len(next) >= len(history) || !sawPath {
		t.Fatal("photo history did not compact", err)
	}
	if _, err = e.imageAttachment(imagePath); err != nil {
		t.Fatal("compaction removed the original image", err)
	}
	archives, _ := filepath.Glob(filepath.Join(e.Dir, "sessions", j.Session, "*.json"))
	if len(archives) != 1 {
		t.Fatal("original transcript not retained")
	}
	b, err := os.ReadFile(archives[0])
	if err != nil || !bytes.Contains(b, []byte(a.SHA256)) || !bytes.Contains(b, []byte(imagePath)) {
		t.Fatal("archive lost attachment metadata", err)
	}
}

func TestNonVisualAdaptersKeepFileReferencesWithoutClaimingVision(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if !bytes.Contains(b, []byte("/workspace/file.png")) || !bytes.Contains(b, []byte("Do not claim to have seen")) || bytes.Contains(b, []byte("base64,")) {
					t.Error("file metadata or vision limitation missing", string(b))
				}
				w.Header().Set("Content-Type", "application/json")
				if protocol == "chat" {
					fmt.Fprint(w, "{\"choices\":[{\"message\":{\"content\":\"ok\"}}]}")
				} else {
					fmt.Fprint(w, "{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}")
				}
			}))
			defer srv.Close()
			c := DefaultConfig()
			c.Provider, c.OpenCodeAPI, c.OpenCodeKey = "opencode-go", protocol, "fixture"
			p := &Provider{Config: c, Client: srv.Client(), BaseURL: srv.URL}
			a := Attachment{Path: "/workspace/file.png", Image: true}
			if _, err := p.Complete(context.Background(), "test", []Message{{Role: "tool", CallID: "look", Attachments: []Attachment{a}}}, nil, nil); err != nil {
				t.Fatal(err)
			}
			e := newTestEngine(t, p)
			if _, err := e.tool(&runningJob{ctx: context.Background()}, ToolCall{Name: "view_image", Arguments: "{\"path\":\"/workspace/file.png\"}"}); err == nil {
				t.Fatal("view_image succeeded on a nonvisual adapter")
			}
		})
	}
}
