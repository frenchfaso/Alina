package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSteeringSkipsStaleToolsAndPersistsAttachments(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	captured := make(chan []Message, 1)
	step := 0
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		step++
		if step == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Message{}, ctx.Err()
			}
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "a", Name: "write", Arguments: `{"path":"stale-a","content":"wrong"}`}, {ID: "b", Name: "write", Arguments: `{"path":"stale-b","content":"wrong"}`}}}, nil
		}
		captured <- m
		return Message{Role: "assistant", Content: "corrected"}, nil
	}))
	j, err := e.Receive("steering", "local", "original objective", "original")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	a := Attachment{Path: "/fixture/image.png", Name: "image.png", MIME: "image/png"}
	update, err := e.Receive(j.Session, j.Owner, "new constraint", "correction", a)
	if err != nil || update.ID != j.ID || update.PendingSteering != 1 {
		t.Fatal(update, err)
	}
	if _, err = e.Receive(j.Session, j.Owner, "new constraint", "correction", a); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Receive(j.Session, j.Owner, "different", "correction", a); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	close(release)
	done := awaitStatus(t, e, j.ID, "completed")
	if done.Output != "corrected" || done.PendingSteering != 0 {
		t.Fatal(done)
	}
	messages := <-captured
	userCount, toolCount := 0, 0
	for _, m := range messages {
		if m.Content == "new constraint" {
			userCount++
			if len(m.Attachments) != 1 || m.Attachments[0].Path != a.Path {
				t.Fatal(m)
			}
		}
		if m.Role == "tool" {
			toolCount++
			if !strings.Contains(m.Content, "not executed") {
				t.Fatal(m)
			}
		}
	}
	if userCount != 1 || toolCount != 2 {
		t.Fatal(userCount, toolCount)
	}
	for _, path := range []string{"stale-a", "stale-b"} {
		if _, err := os.Stat(filepath.Join(e.Config.WorkDir, path)); !os.IsNotExist(err) {
			t.Fatal("stale action executed", path, err)
		}
	}
	var count int
	if err := e.Memory.DB.QueryRow("SELECT count(*) FROM journal WHERE id=?", "steer-"+contentID("correction")[:32]).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if old, err := e.Receive(j.Session, j.Owner, "new constraint", "correction", a); err != nil || old.ID != j.ID {
		t.Fatal("completed retry duplicated", old, err)
	}
}

func TestSteeringSupersedesApprovalWithoutGrant(t *testing.T) {
	step := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "pending", Name: "shell", Arguments: `{"command":"printf do-not-run","network":true}`}}}, nil
		}
		return Message{Role: "assistant", Content: "new plan"}, nil
	}))
	j, err := e.Receive("approval-steer", "local", "start", "start")
	if err != nil {
		t.Fatal(err)
	}
	pending := awaitStatus(t, e, j.ID, "approval")
	if _, err = e.Steer(j.ID, "stranger", "change", "stranger"); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if _, err = e.Steer(j.ID, "local", "change plans", "change"); err != nil {
		t.Fatal(err)
	}
	if err = e.Approve(j.ID, pending.Approval.ID, "always", "local"); err == nil {
		t.Fatal("obsolete approval accepted")
	}
	awaitStatus(t, e, j.ID, "completed")
	if len(e.Permissions.List()) != 0 {
		t.Fatal("steering created permission")
	}
}

func TestSteeringDuringShellWaitsThenSkipsRemainder(t *testing.T) {
	step := 0
	e := newTestEngine(t, modelFunc(func(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", Calls: []ToolCall{{ID: "shell", Name: "shell", Arguments: `{"command":"printf started > started; while [ ! -f release ]; do sleep 0.02; done; printf finished > finished"}`}, {ID: "write", Name: "write", Arguments: `{"path":"stale","content":"wrong"}`}}}, nil
		}
		return Message{Role: "assistant", Content: "reconsidered"}, nil
	}))
	j, err := e.Receive("shell-steer", "local", "start", "shell-start")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(e.Config.WorkDir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = e.Steer(j.ID, "local", "new constraint", "shell-update"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(e.Config.WorkDir, "finished")); !os.IsNotExist(err) {
		t.Fatal("shell already finished")
	}
	if err = os.WriteFile(filepath.Join(e.Config.WorkDir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "completed")
	if _, err = os.Stat(filepath.Join(e.Config.WorkDir, "finished")); err != nil {
		t.Fatal("running shell was killed", err)
	}
	if _, err = os.Stat(filepath.Join(e.Config.WorkDir, "stale")); !os.IsNotExist(err) {
		t.Fatal("later tool executed", err)
	}
}

func TestSteeringFinalReplyRaceAndQueueBound(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	step := 0
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		step++
		if step == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Message{}, ctx.Err()
			}
			return Message{Role: "assistant", Content: "stale final"}, nil
		}
		return Message{Role: "assistant", Content: m[len(m)-1].Content}, nil
	}))
	j, err := e.Receive("final-steer", "local", "start", "final-start")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < 16; i++ {
		if _, err = e.Steer(j.ID, "local", fmt.Sprint(i), fmt.Sprintf("update-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = e.Steer(j.ID, "local", "overflow", "overflow"); err == nil {
		t.Fatal("unbounded mailbox")
	}
	close(release)
	done := awaitStatus(t, e, j.ID, "completed")
	if done.Output != "15" {
		t.Fatal("returned stale final", done)
	}
	if _, err = e.Steer(j.ID, "local", "too late", "too-late"); err == nil {
		t.Fatal("closed mailbox accepted message")
	}
	next, err := e.Receive(j.Session, j.Owner, "next request", "next-request")
	if err != nil || next.ID == j.ID {
		t.Fatal(next, err)
	}
	awaitStatus(t, e, next.ID, "completed")
}

func TestSteeringSurvivesRestartAndResume(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.WorkDir = t.TempDir()
	started := make(chan struct{})
	e, err := NewEngine(dir, c, modelFunc(func(ctx context.Context, _ string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		close(started)
		<-ctx.Done()
		return Message{}, ctx.Err()
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.Receive("recover-steer", "local", "original", "recover-start")
	if err != nil {
		e.Close()
		t.Fatal(err)
	}
	<-started
	if _, err = e.Steer(j.ID, "local", "do not overwrite the backup", "recover-update"); err != nil {
		e.Close()
		t.Fatal(err)
	}
	e.Close()
	var captured []Message
	e, err = NewEngine(dir, c, modelFunc(func(_ context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		captured = m
		return Message{Role: "assistant", Content: "recovered"}, nil
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	old, ok := e.Get(j.ID)
	if !ok || old.PendingSteering != 1 {
		t.Fatal(old)
	}
	resumed, err := e.Resume(j.ID, "local")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, resumed.ID, "completed")
	if captured[len(captured)-1].Content != "do not overwrite the backup" {
		t.Fatal("pending correction lost on resume", captured)
	}
	if receipt, err := e.Steer(j.ID, "local", "do not overwrite the backup", "recover-update"); err != nil || receipt.ID != resumed.ID {
		t.Fatal("receipt lost after recovery", receipt, err)
	}
	if old, _ = e.Get(j.ID); old.PendingSteering != 0 {
		t.Fatal("old pending count not cleared", old)
	}
}

func TestSteeringPersistenceFailureDoesNotAccept(t *testing.T) {
	e := newTestEngine(t, &scriptedModel{wait: true})
	j, err := e.Receive("failed-steer", "local", "start", "failed-start")
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, j.ID, "running")
	if _, err = e.Memory.DB.Exec(`CREATE TRIGGER fail_steer BEFORE UPDATE OF payload ON jobs BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Steer(j.ID, "local", "change", "failed-update"); err == nil {
		t.Fatal("accepted unpersisted message")
	}
	if _, err = e.Memory.DB.Exec("DROP TRIGGER fail_steer"); err != nil {
		t.Fatal(err)
	}
	if received, err := e.steeringReceived("failed-update", "local"); received || err != nil {
		t.Fatal("partial receipt survived rollback", received, err)
	}
	if job, _ := e.Get(j.ID); job.PendingSteering != 0 {
		t.Fatal(job)
	}
}

type lockedChatBuffer struct {
	sync.Mutex
	b bytes.Buffer
}

func (b *lockedChatBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.b.Write(p)
}
func (b *lockedChatBuffer) String() string { b.Lock(); defer b.Unlock(); return b.b.String() }

func TestChatAcceptsSteeringWhileWaiting(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	step := 0
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		step++
		if step == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Message{}, ctx.Err()
			}
			return Message{Role: "assistant", Content: "first"}, nil
		}
		return Message{Role: "assistant", Content: "received: " + m[len(m)-1].Content}, nil
	}))
	socketDir, err := os.MkdirTemp("", "ac-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	ln, err := net.Listen("unix", socketPath(socketDir))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler(e)}
	go srv.Serve(ln)
	defer srv.Close()
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out := &lockedChatBuffer{}
	finished := make(chan error, 1)
	go func() { finished <- chat(ctx, socketDir, "chat-steer", bufio.NewReader(in), out) }()
	fmt.Fprintln(writer, "initial")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("chat did not start")
	}
	fmt.Fprintln(writer, "correction")
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "Messaggio aggiunto") {
		if time.Now().After(deadline) {
			t.Fatal("terminal input was blocked", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	writer.Close()
	if err = <-finished; err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "received: correction") {
		t.Fatal(out.String())
	}
}

func TestSteeringBudgetDoesNotReset(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, _ []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
		return Message{Role: "assistant", Content: "old reply"}, nil
	}))
	e.Config.MaxSteps = 1
	j, err := e.Receive("budget-steer", "local", "start", "budget-start")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = e.Steer(j.ID, "local", "new constraint", "budget-update"); err != nil {
		t.Fatal(err)
	}
	close(release)
	done := awaitStatus(t, e, j.ID, "failed")
	if !strings.Contains(done.Error, "step budget") || done.PendingSteering != 1 {
		t.Fatal(done)
	}
	var payload string
	if err = e.Memory.DB.QueryRow("SELECT payload FROM steering WHERE id='budget-update' AND applied=0").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var pending steeringInput
	if err = json.Unmarshal([]byte(payload), &pending); err != nil || pending.Message.Content != "new constraint" {
		t.Fatal(pending, err)
	}
}

func TestTelegramAttachmentSteersAndDeduplicates(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	step := 0
	e := newTestEngine(t, modelFunc(func(ctx context.Context, _ string, m []Message, _ []ToolSpec, _ func(string)) (Message, error) {
		step++
		if step == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Message{}, ctx.Err()
			}
			return Message{Role: "assistant", Content: "old response"}, nil
		}
		last := m[len(m)-1]
		if last.Content != "Considera anche questo" || len(last.Attachments) != 1 {
			return Message{}, fmt.Errorf("steering attachment missing: %+v", last)
		}
		return Message{Role: "assistant", Content: "attachment received"}, nil
	}))
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			fmt.Fprint(w, `{"ok":true,"result":{"file_path":"documents/file.txt","file_size":4}}`)
		case strings.Contains(r.URL.Path, "/file/bot"):
			downloads.Add(1)
			fmt.Fprint(w, "data")
		default:
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer srv.Close()
	tg := NewTelegram(e.Dir, TelegramConfig{Token: "fixture", OwnerID: 42}, e, srv.Client())
	tg.BaseURL = srv.URL
	u := photoUpdate()
	u.Message.Photo = nil
	u.Message.Text = "original request"
	if err := tg.process(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	<-started
	u.ID = 2
	u.Message.Text = ""
	u.Message.Caption = "Considera anche questo"
	u.Message.Document = &tgFile{ID: "doc", Name: "file.txt", Size: 4}
	for i := 0; i < 2; i++ {
		if err := tg.process(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.Jobs("telegram:42")) != 1 || downloads.Load() != 1 {
		t.Fatal("steering became another job or repeated a download")
	}
	close(release)
	if done := awaitStatus(t, e, "tg1", "completed"); done.Output != "attachment received" {
		t.Fatal(done)
	}
	if err := tg.process(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if len(e.Jobs("telegram:42")) != 1 || downloads.Load() != 1 {
		t.Fatal("completed steering retry was replayed")
	}
}
