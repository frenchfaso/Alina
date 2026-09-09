package alina

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

func sendFileSpec() ToolSpec {
	return ToolSpec{Name: "send_file", Description: "Attach a local workspace file to your final Telegram reply to the current user. No recipient selection. Copy outside files into your workspace first. Up to 4 files, 20 MiB each; files are sent as documents, preserving originals. This queues a snapshot, not an immediate send. Do not claim delivery has already succeeded.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}
}
func (e *Engine) canSendFile(j *runningJob) bool {
	if !e.Config.Telegram.Enabled || j.Kind == "dream" || j.Kind == "initiative" {
		return false
	}
	for _, u := range e.Config.Users {
		if u.TelegramID > 0 && j.Owner == telegramOwner(e.Config.Telegram, u.TelegramID) && e.acceptsOwner(j.Owner) {
			return true
		}
	}
	return len(e.Config.Users) == 0 && e.Config.Telegram.OwnerID > 0 && j.Owner == telegramOwner(e.Config.Telegram, e.Config.Telegram.OwnerID)
}
func (e *Engine) queueFile(j *runningJob, args string) (string, error) {
	if err := j.ctx.Err(); err != nil {
		return "", err
	}
	if !e.canSendFile(j) {
		return "", errors.New("file delivery requires a Telegram user request")
	}
	var a struct{ Path string }
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("file path is required")
	}
	if !filepath.IsAbs(a.Path) {
		a.Path = filepath.Join(e.Workspace(), a.Path)
	}
	b, err := readAttachment(e.Workspace(), a.Path)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("cannot send an empty file")
	}
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	name := filepath.Base(a.Path)
	if len(name) > 200 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("use a file name up to 200 bytes without control characters")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range j.OutputFiles {
		if f.SHA256 == digest && f.Name == name {
			return "File already attached to this reply.", nil
		}
	}
	if len(j.OutputFiles) >= 4 {
		return "", errors.New("at most four files per reply")
	}
	root, err := os.OpenRoot(e.Workspace())
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err = root.MkdirAll("outbox", 0700); err != nil {
		return "", err
	}
	rel := "outbox/" + j.ID + "-" + randomID()
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		root.Remove(rel)
		return "", err
	}
	attachment := Attachment{Name: name, Path: filepath.Join(e.Workspace(), rel), Size: int64(len(b)), SHA256: digest}
	j.OutputFiles = append(j.OutputFiles, attachment)
	if err = e.persist(j); err != nil {
		j.OutputFiles = j.OutputFiles[:len(j.OutputFiles)-1]
		root.Remove(rel)
		return "", err
	}
	return "File attached to the final reply: " + name, nil
}

func (t *Telegram) sendDocument(ctx context.Context, id int64, e *Engine, a Attachment) error {
	b, err := readAttachment(e.Workspace(), a.Path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(b)
	if int64(len(b)) != a.Size || hex.EncodeToString(hash[:]) != a.SHA256 {
		return errors.New("outgoing file changed; delivery stopped")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err = form.WriteField("chat_id", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	part, err := form.CreateFormFile("document", a.Name)
	if err != nil {
		return err
	}
	if _, err = part.Write(b); err != nil {
		return err
	}
	if err = form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint("sendDocument"), &body)
	if err != nil {
		return errors.New("cannot create Telegram upload")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	client := *t.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Telegram upload connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &telegramAPIError{code: resp.StatusCode}
	}
	var envelope struct {
		OK        bool
		ErrorCode int `json:"error_code"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid Telegram upload response")
	}
	if !envelope.OK {
		return &telegramAPIError{code: envelope.ErrorCode}
	}
	return nil
}

func (t *Telegram) sendChunks(ctx context.Context, id int64, chunks []tgText, keyboard any) error {
	for i, c := range chunks {
		body := map[string]any{"chat_id": id, "text": c.Text, "link_preview_options": map[string]bool{"is_disabled": true}}
		if len(c.Entities) > 0 {
			body["entities"] = c.Entities
		}
		if keyboard != nil && i == len(chunks)-1 {
			body["reply_markup"] = keyboard
		}
		err := t.api(ctx, "sendMessage", body, nil)
		var rejected *telegramAPIError
		if len(c.Entities) > 0 && errors.As(err, &rejected) && rejected.code == 400 {
			delete(body, "entities")
			err = t.api(ctx, "sendMessage", body, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Checkpoint each confirmed part so a later upload failure does not replay the
// answer or earlier files. A crash between Telegram acceptance and SQLite is
// still ambiguous, as Telegram sendMessage has no idempotency key.
func (t *Telegram) deliverReply(ctx context.Context, id int64, e *Engine, j Job, text string) error {
	chunks := splitTelegramText(text, nil)
	if j.Status == "completed" {
		chunks = telegramText(text)
	}
	part := func(index int, send func() error) error {
		key := fmt.Sprintf("telegram-part:%s:%s:%d", j.ID, j.Status, index)
		var exists int
		if err := e.Memory.DB.QueryRowContext(ctx, "SELECT count(*) FROM memory_state WHERE key=?", key).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return nil
		}
		if err := send(); err != nil {
			return err
		}
		_, err := e.Memory.DB.ExecContext(ctx, "INSERT OR IGNORE INTO memory_state VALUES(?,?)", key, "sent")
		return err
	}
	for i, c := range chunks {
		if err := part(i, func() error { return t.sendChunks(ctx, id, []tgText{c}, nil) }); err != nil {
			return err
		}
	}
	if j.Status != "completed" {
		return nil
	}
	for i, a := range j.OutputFiles {
		if err := part(len(chunks)+i, func() error { return t.sendDocument(ctx, id, e, a) }); err != nil {
			return err
		}
	}
	return nil
}
