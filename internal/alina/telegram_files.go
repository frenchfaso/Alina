package alina

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type tgFile struct {
	ID       string `json:"file_id"`
	UniqueID string `json:"file_unique_id"`
	Name     string `json:"file_name"`
	MIME     string `json:"mime_type"`
	Size     int64  `json:"file_size"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
}

func (m *tgMessage) file() *tgFile {
	for _, f := range []*tgFile{m.Document, m.Audio, m.Video, m.Voice, m.Animation, m.VideoNote} {
		if f != nil {
			return f
		}
	}
	var best *tgFile
	for i := range m.Photo {
		f := &m.Photo[i]
		if best == nil || f.Width*f.Height > best.Width*best.Height {
			best = f
		}
	}
	return best
}

type attachmentRejected struct{ reason string }

func (e *attachmentRejected) Error() string { return e.reason }

type telegramAPIError struct{ code int }

func (e *telegramAPIError) Error() string { return fmt.Sprintf("Telegram API error %d", e.code) }

// The owner explicitly sending a file authorizes fetching that specific Telegram
// upload. This path cannot fetch URLs supplied in captions or file names.
func (t *Telegram) receiveFile(ctx context.Context, updateID int64, file tgFile) (Attachment, error) {
	if file.ID == "" || file.Size < 0 || file.Size > maxAttachmentBytes {
		return Attachment{}, &attachmentRejected{"Attachment exceeds the 20 MiB download limit or has invalid metadata."}
	}
	workspace := t.Engine.Workspace()
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return Attachment{}, err
	}
	defer root.Close()
	if err = root.MkdirAll("inbox/telegram", 0700); err != nil {
		return Attachment{}, err
	}
	inbox, err := root.OpenRoot("inbox/telegram")
	if err != nil {
		return Attachment{}, err
	}
	defer inbox.Close()
	key := t.updateKey(updateID) + "-" + contentID(file.ID)[:16]
	// Do not use the original name as a path. Keep a safe extension for tools.
	ext := strings.ToLower(filepath.Ext(file.Name))
	if len(ext) > 12 || strings.ContainsAny(ext, "/\\\x00\r\n") || strings.Trim(ext, ".abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		ext = ""
	}
	manifestName := key + ".meta.json"
	if _, er := inbox.Lstat(manifestName); er == nil {
		cached, er := readRootFile(inbox, manifestName, 4096)
		var a Attachment
		if er != nil || json.Unmarshal(cached, &a) != nil {
			return Attachment{}, &attachmentRejected{"Attachment cache is invalid; send the file again."}
		}
		name := filepath.Base(a.Path)
		if a.Path != filepath.Join(workspace, "inbox", "telegram", name) || !strings.HasPrefix(name, key+"-") {
			return Attachment{}, &attachmentRejected{"Attachment cache is invalid; send the file again."}
		}
		b, er := readRootFile(inbox, name, maxAttachmentBytes)
		hash := sha256.Sum256(b)
		if er != nil || int64(len(b)) != a.Size || hex.EncodeToString(hash[:]) != a.SHA256 {
			return Attachment{}, &attachmentRejected{"Saved attachment changed or is unavailable; send it again."}
		}
		return a, nil
	} else if !os.IsNotExist(er) {
		return Attachment{}, er
	}
	var remote struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if err = t.api(ctx, "getFile", map[string]string{"file_id": file.ID}, &remote); err != nil {
		var apiErr *telegramAPIError
		if errors.As(err, &apiErr) && apiErr.code >= 400 && apiErr.code < 500 && apiErr.code != 429 {
			return Attachment{}, &attachmentRejected{"Telegram cannot provide this attachment; check its size and send it again."}
		}
		return Attachment{}, err
	}
	if remote.Size < 0 || remote.Size > maxAttachmentBytes {
		return Attachment{}, &attachmentRejected{"Telegram attachment exceeds the 20 MiB download limit."}
	}
	if remote.Path == "" || strings.HasPrefix(remote.Path, "/") || strings.ContainsAny(remote.Path, "\\%?#:\x00\r\n") {
		return Attachment{}, &attachmentRejected{"Telegram returned an invalid attachment path."}
	}
	for _, part := range strings.Split(remote.Path, "/") {
		if part == ".." || part == "." || part == "" {
			return Attachment{}, &attachmentRejected{"Telegram returned an invalid attachment path."}
		}
	}
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/file/bot"+t.Config.Token+"/"+remote.Path, nil)
	if err != nil {
		return Attachment{}, errors.New("cannot create Telegram file request")
	}
	client := *t.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors include the URL, which contains the bot token.
		return Attachment{}, errors.New("Telegram attachment connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 300 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return Attachment{}, &attachmentRejected{"Telegram could not download this attachment; send it again."}
		}
		return Attachment{}, errors.New("Telegram attachment download failed")
	}
	if resp.ContentLength > maxAttachmentBytes {
		return Attachment{}, &attachmentRejected{"Telegram attachment exceeds the 20 MiB download limit."}
	}
	// Exclusive creation works on Android where hard links can be denied.
	// Publish the reference only after the complete file has been synced.
	name := key + "-" + randomID() + ext
	f, err := inbox.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Attachment{}, err
	}
	committed := false
	defer func() {
		if !committed {
			inbox.Remove(name)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if n > maxAttachmentBytes {
		return Attachment{}, &attachmentRejected{"Telegram attachment exceeds the 20 MiB download limit."}
	}
	if err != nil || closeErr != nil || remote.Size > 0 && n != remote.Size {
		return Attachment{}, errors.New("Telegram attachment was incomplete; retry pending")
	}
	stored, err := inbox.Open(name)
	if err != nil {
		return Attachment{}, err
	}
	header := make([]byte, 512)
	headSize, _ := stored.Read(header)
	stored.Close()
	mime := strings.Split(http.DetectContentType(header[:headSize]), ";")[0]
	imageFile := mime == "image/png" || mime == "image/jpeg" || mime == "image/webp"
	label := file.Name
	if label == "" {
		label = filepath.Base(remote.Path)
	}
	a := Attachment{Name: truncate(label, 200), Path: filepath.Join(workspace, "inbox", "telegram", name), MIME: mime, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil)), Image: imageFile}
	// Manifest is private and atomic. Its destination never uses remote names.
	temp := ".download-" + randomID()
	manifest, err := inbox.OpenFile(temp+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Attachment{}, err
	}
	defer inbox.Remove(temp + ".json")
	err = json.NewEncoder(manifest).Encode(a)
	if err == nil {
		err = manifest.Sync()
	}
	closeErr = manifest.Close()
	if err != nil {
		return Attachment{}, err
	}
	if closeErr != nil {
		return Attachment{}, closeErr
	}
	if err = inbox.Rename(temp+".json", manifestName); err != nil {
		return Attachment{}, err
	}
	committed = true
	return a, nil
}
