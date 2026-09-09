package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var errHarnessRestarting = errors.New("service restarting; retry after reconnect")

// One private transaction, no permanent supervisor or automatic retry loop.
// Configuration is untouched until work has drained and the old daemon stops.
type restartTransaction struct {
	ID, Scope, Owner, JobID, AfterID, State, Base, Outcome string
	Resume                                                 bool
	Previous, Next                                         Config
	Created                                                time.Time
}

func restartPath(dir string) string { return filepath.Join(dir, "harness-restart.json") }
func readRestart(dir string) (restartTransaction, error) {
	var tx restartTransaction
	b, err := readSmallFile(restartPath(dir), 128<<10)
	if err != nil {
		return tx, err
	}
	err = json.Unmarshal([]byte(b), &tx)
	if err == nil && !safeID(tx.ID) {
		err = errors.New("invalid restart transaction")
	}
	return tx, err
}
func launchRestart(dir, id string) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := daemonOutput(dir)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, "__restart", id)
	cmd.Env = append(os.Environ(), "ALINA_HOME="+dir)
	cmd.Dir = "/"
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func restartHelper(ctx context.Context, dir, id string) (err error) {
	if !safeID(id) {
		return errors.New("invalid restart ID")
	}
	tx, err := readRestart(dir)
	if err != nil {
		return err
	}
	if tx.ID != id || tx.State != "waiting" {
		return errors.New("restart transaction no longer waiting")
	}
	// Taking this lock before draining prevents competing CLI lifecycle actions.
	lock, err := os.OpenFile(filepath.Join(dir, "service-control.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		tx.Outcome = "Restart cancelled: another service operation is in progress. Configuration was not applied."
		return finishRestart(dir, tx)
	}
	defer func() {
		if err != nil && tx.Outcome == "" {
			tx.Outcome = "Restart could not complete. Inspect harness status and local logs before retrying. No automatic retry will run."
		}
		if tx.Outcome != "" {
			err = errors.Join(err, finishRestart(dir, tx))
		}
	}()
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		var result struct{ Ready bool }
		err = localRequest(wait, dir, "POST", "/v1/harness/restart/prepare", map[string]string{"id": id}, &result)
		if err != nil {
			return err
		}
		if result.Ready {
			break
		}
		select {
		case <-wait.Done():
			return wait.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	if err = stopDaemon(ctx, dir); err != nil {
		return err
	}
	if err = installRestartConfig(dir, tx, false); err != nil {
		// No proposed configuration was installed. Bring the saved instance back.
		tx.Outcome = "Restart cancelled: saved configuration changed or could not be written. Inspect the current configuration."
		return errors.Join(err, startDaemon(ctx, dir, io.Discard))
	}
	if err = startDaemon(ctx, dir, io.Discard); err != nil {
		startupErr := err
		if err = installRestartConfig(dir, tx, true); err != nil {
			return errors.Join(startupErr, err)
		}
		tx.Outcome = "The proposed configuration failed to start. The previous saved configuration was restored."
		if err = startDaemon(ctx, dir, io.Discard); err != nil {
			return errors.Join(startupErr, err)
		}
		tx.Resume = true
		return nil
	}
	tx.Resume = true
	tx.Outcome = "Restart completed; the new daemon passed its local readiness check and the saved configuration is active. Remote provider connectivity has not been tested."
	return nil
}
func installRestartConfig(dir string, tx restartTransaction, rollback bool) error {
	lock, err := lockDaemonState(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	if !rollback {
		if contentID(string(raw)) != tx.Base {
			return errors.New("saved configuration changed after staging")
		}
		return SaveConfig(dir, tx.Next)
	}
	current, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	if jsonText(current) != jsonText(tx.Next) {
		return errors.New("configuration changed during restart; rollback refused")
	}
	return SaveConfig(dir, tx.Previous)
}
func finishRestart(dir string, tx restartTransaction) error {
	tx.State = "done"
	if err := writeJSON(restartPath(dir), tx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		var result map[string]any
		err := localRequest(ctx, dir, "POST", "/v1/harness/restart/complete", map[string]string{"id": tx.ID}, &result)
		if err == nil {
			return os.Remove(restartPath(dir))
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}

}
func (e *Engine) restartEngine(tx restartTransaction) (*Engine, error) {
	selected := e
	if tx.Scope != "" && tx.Scope != "global" {
		selected = e.scopes[tx.Scope]
	}
	if selected == nil || !selected.acceptsOwner(tx.Owner) {
		return nil, errors.New("restart owner or memory scope is unavailable")
	}
	return selected, nil
}
func restartHandler(root *Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var a struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&a); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		root.restartMu.Lock()
		defer root.restartMu.Unlock()
		tx, err := readRestart(root.AdminDir)
		if err != nil || tx.ID != a.ID {
			http.Error(w, "restart transaction not found", 404)
			return
		}
		selected, err := root.restartEngine(tx)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		switch r.URL.Path {
		case "/v1/harness/restart/prepare":
			if tx.State != "waiting" {
				http.Error(w, "restart is not waiting", 409)
				return
			}
			old, ok := selected.Get(tx.JobID)
			if !ok {
				http.Error(w, "restart job missing", 409)
				return
			}
			if terminalStatus(old.Status) && (old.Status != "completed" || old.PendingSteering > 0 || old.Continuation != tx.AfterID) {
				http.Error(w, "restart turn did not complete cleanly", 409)
				return
			}
			ready := terminalStatus(old.Status)
			if ready && strings.HasPrefix(old.Owner, "telegram:") {
				var receipt string
				ready = selected.Memory.DB.QueryRow("SELECT value FROM memory_state WHERE key=?", "telegram-delivered:"+old.ID).Scan(&receipt) == nil && receipt == old.Status
			}
			root.restarting.Store(true)
			for _, e := range root.engines() {
				e.mu.Lock()
				ready = ready && e.inFlight == 0
				e.mu.Unlock()
			}
			if !ready {
				root.restarting.Store(false)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"ready": ready})
		case "/v1/harness/restart/complete":
			root.restarting.Store(false)
			if tx.State != "done" || tx.Outcome == "" {
				http.Error(w, "restart has no outcome", 409)
				return
			}
			selected.mu.Lock()
			defer selected.mu.Unlock()
			if _, ok := selected.getLocked(tx.AfterID); !ok {
				old, ok := selected.getLocked(tx.JobID)
				if !ok || !terminalStatus(old.Status) {
					http.Error(w, "original turn is not finished", 409)
					return
				}
				if !tx.Resume || old.Status == "cancelled" {
					notice := Job{ID: tx.AfterID, Session: old.Session, Owner: old.Owner, Kind: "chat", Status: "completed", Input: "Harness lifecycle outcome", Output: tx.Outcome, ServiceNotice: tx.Outcome, Created: time.Now().UTC()}
					if err := selected.persist(&runningJob{Job: notice}); err != nil {
						http.Error(w, "cannot persist restart outcome", 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"continuation": tx.AfterID})
					return
				}
				input := fmt.Sprintf("Harness lifecycle result: %s\nContinue the user-authorized task from job %s: %s\nCheck harness status/config, report the outcome in the user's language, and finish any remaining work. Do not repeat the configuration change or restart in this continuation. Earlier tool outcomes and any pending steering are preserved.", tx.Outcome, old.ID, truncate(old.Input, 20000))
				_, err := selected.submitLocked(old.Session, old.Owner, input, tx.AfterID, "chat", old.ID, tx.Outcome, old.Attachments...)
				if err != nil {
					http.Error(w, "cannot persist restart continuation", 500)
					return
				}
			}
			root.Events.emit("harness.restart_finished", nil, "job_id", tx.JobID)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"continuation": tx.AfterID})
		default:
			http.NotFound(w, r)
		}
	})
}
