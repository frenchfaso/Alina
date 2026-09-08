package alina

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ncruces/go-sqlite3"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const logFileBytes = 2 << 20
const logCopies = 3 // Current file plus two rotated files: at most 6 MiB.

// Events contain operational metadata only. Never pass prompts, tool arguments,
// outputs, URLs, credentials or raw error messages to this logger.
type EventLog struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	limit   int64
	logger  *slog.Logger
	dropped int
	failed  bool
}

func openEventLog(dir string) (*EventLog, error) {
	path := filepath.Join(dir, "logs", "alina.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	l := &EventLog{path: path, limit: logFileBytes}
	if err := l.open(); err != nil {
		return nil, err
	}
	l.logger = slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Time(slog.TimeKey, a.Value.Time().UTC())
		}
		return a
	}})).With("schema", 1, "version", Version, "run_id", randomID())
	return l, nil
}
func (l *EventLog) open() error {
	if info, err := os.Lstat(l.path); err == nil && !info.Mode().IsRegular() {
		return errors.New("log path must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	l.file = f
	return nil
}
func (l *EventLog) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() {
		if err != nil {
			l.dropped++
			if !l.failed {
				fmt.Fprintln(os.Stderr, `{"level":"ERROR","msg":"logging.failed","hint":"alina doctor"}`)
			}
			l.failed = true
		} else {
			l.failed = false
		}
	}()
	if int64(len(p)) > min(l.limit, 32<<10) {
		return 0, errors.New("log event too large")
	}
	if l.file == nil {
		if err = l.open(); err != nil {
			return 0, err
		}
	}
	info, err := l.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size()+int64(len(p)) > l.limit {
		if err = l.file.Close(); err != nil {
			l.file = nil
			return 0, err
		}
		l.file = nil
		for i := logCopies - 2; i >= 0; i-- {
			from := l.path
			if i > 0 {
				from += fmt.Sprint(".", i)
			}
			to := fmt.Sprint(l.path, ".", i+1)
			if err = os.Rename(from, to); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err = l.open(); err != nil {
			return 0, err
		}
	}
	return l.file.Write(p)
}
func (l *EventLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Sync()
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(err, closeErr)
}
func (l *EventLog) health() map[string]any {
	if l == nil {
		return map[string]any{"enabled": false}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{"enabled": true, "ok": !l.failed, "dropped_events": l.dropped, "max_bytes": logFileBytes * logCopies}
}
func (l *EventLog) emit(event string, err error, attrs ...any) {
	if l == nil {
		return
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
		if errors.Is(err, context.Canceled) {
			level = slog.LevelInfo
		}
		attrs = append(attrs, "error", errorInfo(err))
	}
	l.logger.Log(context.Background(), level, event, attrs...)
}

// Stable classifications make logs useful without copying error strings that
// can embed shell commands, document text, query strings or remote responses.
type shellExitError struct{ code int }

func (e *shellExitError) Error() string { return fmt.Sprintf("shell exited with status %d", e.code) }

func errorInfo(err error) map[string]any {
	if err == nil {
		return nil
	}
	code := "operation_failed"
	var process *shellExitError
	var db *sqlite3.Error
	var network net.Error
	var api *localAPIError
	var remote *remoteHTTPError
	var telegram *telegramAPIError
	var path *os.PathError
	var dns *net.DNSError
	var syntax *json.SyntaxError
	status := 0
	switch {
	case errors.Is(err, errStateLocked):
		code = "state_locked"
	case errors.Is(err, syscall.ENOSPC):
		code = "disk_full"
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.As(err, &process):
		code = "process_exit"
	case errors.As(err, &api):
		code = "local_api_error"
		status = api.Status
	case errors.As(err, &remote):
		code = "http_error"
		status = remote.Status
	case errors.As(err, &telegram):
		code = "telegram_error"
		status = telegram.code
	case errors.Is(err, os.ErrPermission):
		code = "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		code = "not_found"
	case errors.As(err, &path):
		code = "filesystem_error"
	case errors.As(err, &dns):
		code = "dns_error"
	case errors.As(err, &db):
		code = "database_error"
	case errors.As(err, &network):
		code = "network_error"
		if network.Timeout() {
			code = "timeout"
		}
	case errors.As(err, &syntax):
		code = "invalid_json"
	default:
		text := strings.ToLower(err.Error())
		for _, v := range []struct{ match, code string }{
			{"login required", "auth_required"}, {"login expired", "auth_expired"},
			{"network request failed", "network_error"}, {"budget reached", "budget_reached"},
			{"webhook", "telegram_webhook"}, {"queue full", "queue_full"}, {"approval timed out", "approval_timeout"},
			{"denied by user", "approval_denied"}, {"steering", "steering_pending"},
			{"sqlite", "database_error"}, {"checkpoint", "checkpoint_error"},
			{"response", "provider_response_error"}, {"invalid", "invalid_input"},
		} {
			if strings.Contains(text, v.match) {
				code = v.code
				break
			}
		}
	}
	out := map[string]any{"code": code, "type": fmt.Sprintf("%T", err)}
	if process != nil {
		out["exit_code"] = process.code
	}
	if api != nil && safeID(api.RequestID) {
		out["request_id"] = api.RequestID
	}
	if db != nil {
		out["sqlite_code"] = int(db.ExtendedCode())
	}
	if status != 0 {
		out["http_status"] = status
	}
	return out
}

func logsCLI(ctx context.Context, dir string, args []string, out io.Writer) error {
	f := flag.NewFlagSet("logs", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	lines := f.Int("lines", 100, "recent matching events (1-1000)")
	job := f.String("job", "", "filter job ID")
	level := f.String("level", "", "INFO or ERROR")
	follow := f.Bool("follow", false, "follow new events")
	if err := f.Parse(args); err != nil {
		return err
	}
	*level = strings.ToUpper(*level)
	if f.NArg() != 0 || *lines < 1 || *lines > 1000 || *job != "" && !safeID(*job) || *level != "" && *level != "INFO" && *level != "ERROR" {
		return errors.New("usage: alina logs [--lines 1-1000] [--job ID] [--level INFO|ERROR] [--follow]")
	}
	path := filepath.Join(dir, "logs", "alina.jsonl")
	// Keep the current file open while reading its predecessors. Its offset
	// provides an exact boundary for follow without duplicate timestamps.
	current, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	defer func() {
		if current != nil {
			current.Close()
		}
	}()
	match := func(raw []byte) bool {
		var event struct {
			Level string `json:"level"`
			Job   string `json:"job_id"`
		}
		return json.Unmarshal(raw, &event) == nil && (*job == "" || event.Job == *job) && (*level == "" || event.Level == *level)
	}
	var tail [][]byte
	read := func(file *os.File) error {
		reader := bufio.NewReaderSize(file, 64<<10)
		for {
			start, _ := file.Seek(0, io.SeekCurrent)
			start -= int64(reader.Buffered())
			raw, err := reader.ReadSlice('\n')
			if err == io.EOF {
				_, err = file.Seek(start, io.SeekStart)
				return err
			}
			if err != nil {
				return err
			}
			if match(raw) {
				tail = append(tail, append([]byte(nil), bytes.TrimSuffix(raw, []byte("\n"))...))
				if len(tail) > *lines {
					tail = tail[1:]
				}
			}
		}
	}
	for i := logCopies - 1; i > 0; i-- {
		f, err := os.Open(fmt.Sprint(path, ".", i))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = read(f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if current != nil {
		if err = read(current); err != nil {
			return err
		}
	}
	for _, raw := range tail {
		if _, err = fmt.Fprintln(out, string(raw)); err != nil {
			return err
		}
	}
	if !*follow {
		return nil
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if current != nil {
			// Seek back over an incomplete final line; the writer appends one
			// JSON object per write, but a reader may race that write.
			reader := bufio.NewReaderSize(current, 64<<10)
			for {
				start, _ := current.Seek(0, io.SeekCurrent)
				start -= int64(reader.Buffered())
				raw, err := reader.ReadSlice('\n')
				if err == io.EOF {
					current.Seek(start, io.SeekStart)
					break
				}
				if err != nil {
					return err
				}
				if len(raw) > 64<<10 {
					return errors.New("log event exceeds read limit")
				}
				if match(raw) {
					if _, err = out.Write(raw); err != nil {
						return err
					}
				}
			}
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if current != nil {
			old, err := current.Stat()
			if err != nil {
				return err
			}
			if os.SameFile(old, info) {
				continue
			}
			// Catch up through retained intermediate files when more than one
			// rotation occurred between polls.
			var candidates []string
			found := false
			for i := logCopies - 1; i >= 0; i-- {
				candidate := path
				if i > 0 {
					candidate += fmt.Sprint(".", i)
				}
				stat, err := os.Stat(candidate)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return err
				}
				if os.SameFile(old, stat) {
					found = true
					candidates = nil
					continue
				}
				candidates = append(candidates, candidate)
			}
			if !found {
				fmt.Fprintln(out, `{"level":"WARN","msg":"logs.retention_gap","hint":"Events older than retained files are no longer available."}`)
			}
			if len(candidates) == 0 {
				continue
			}
			next, err := os.Open(candidates[0])
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			current.Close()
			current = next
		} else {
			current, err = os.Open(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
		}
	}
}
