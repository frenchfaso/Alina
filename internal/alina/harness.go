package alina

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func harnessSpec() ToolSpec {
	return ToolSpec{Name: "harness", Description: "Inspect your harness: manual (Markdown reference), status, config (active/saved/pending, no credentials or other families), diagnose (local checks and recent operational events; optional job_id). configure stages a validated JSON merge patch; discard cancels your staged change; restart applies it after this turn and resumes the conversation. Mutations are ONLY for an explicit user request, never personal exploration. Call restart last, then finish with a brief restart notice and remaining work. Account credentials, people, Telegram routing and endpoints require local setup/config. Read procedures/harness.md for details.", Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"manual", "status", "config", "diagnose", "configure", "discard", "restart"}},
		"patch":  map[string]any{"type": "object", "description": "Partial configuration; omitted fields stay unchanged."},
		"reason": map[string]any{"type": "string", "description": "The user's request authorizing configuration or restart."},
		"job_id": map[string]any{"type": "string"},
	}, "required": []string{"action"}}}
}

// The account/device owner manages credentials and routing locally. In-chat
// configuration covers behavior, without exposing or rewriting another family.
var harnessConfigFields = map[string]string{
	"provider": "", "model": "", "opencode_api": "", "reasoning_effort": "", "dream_reasoning_effort": "", "checkpoint_reasoning_effort": "", "verbosity": "", "context_tokens": "", "max_steps": "", "model_timeout_seconds": "", "command_timeout_seconds": "", "work_dir": "", "timezone": "", "location": "", "network_policy": "",
	"search": "default openai_model", "memory": "enabled dream dream_cron catch_up", "autonomy": "enabled scope max_calls_per_day minutes_per_run web_search",
}

func harnessConfig(c Config) map[string]any {
	obj := redactedConfig(c)
	delete(obj, "users")
	delete(obj, "local_user")
	obj["telegram"] = map[string]any{"enabled": c.Telegram.Enabled, "configured": c.Telegram.Token != ""}
	return obj
}
func patchHarnessConfig(base Config, patch map[string]any) (Config, error) {
	if len(patch) == 0 {
		return base, errors.New("provide a nonempty configuration patch")
	}
	for key, value := range patch {
		children, ok := harnessConfigFields[key]
		if !ok {
			return base, errors.New("field requires local setup/config: " + key)
		}
		if children != "" {
			obj, ok := value.(map[string]any)
			if !ok {
				return base, errors.New("nested configuration requires an object: " + key)
			}
			for k := range obj {
				if !strings.Contains(" "+children+" ", " "+k+" ") {
					return base, errors.New("field requires local setup/config: " + key + "." + k)
				}
			}
		}
	}
	obj := configObject(base)
	mergeConfig(obj, patch)
	next := DefaultConfig()
	dec := json.NewDecoder(strings.NewReader(jsonText(obj)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return base, err
	}
	return next, next.Validate()
}

func (e *Engine) harnessStatus() map[string]any {
	binary, _ := os.Executable()
	return map[string]any{"version": Version, "pid": os.Getpid(), "executable": binary, "self_restart_available": e.global.managed, "scope": e.Scope, "provider": e.Config.Provider, "model": e.Config.Model, "jobs": jobSummaries(e.Jobs("")), "logging": e.Events.health(), "network_sandbox": sandboxAvailable()}
}
func (e *Engine) harnessTool(j *runningJob, raw string) (string, error) {
	if len(raw) > 65536 {
		return "", errors.New("harness request exceeds 64 KiB")
	}
	var a struct {
		Action string         `json:"action"`
		Patch  map[string]any `json:"patch"`
		Reason string         `json:"reason"`
		JobID  string         `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", err
	}
	switch a.Action {
	case "manual":
		return "Alina " + Version + "\n\n" + harnessGuide, nil
	case "status":
		out := e.harnessStatus()
		model, effort := e.Config.Model, e.Config.ReasoningEffort
		if j.Model != "" {
			model, effort = j.Model, j.Reasoning
		}
		out["conversation_model"] = model
		out["conversation_reasoning"] = effort
		return jsonText(out), nil
	case "config":
		e.global.restartMu.Lock()
		defer e.global.restartMu.Unlock()
		out := map[string]any{"active": harnessConfig(e.global.Config)}
		saved, err := checkedConfig(e.AdminDir)
		if err != nil {
			out["saved_error"] = errorInfo(err)
		} else {
			out["saved"] = harnessConfig(saved)
			out["restart_required"] = jsonText(saved) != jsonText(e.global.Config)
		}
		if tx, err := readRestart(e.AdminDir); err == nil && tx.Owner == j.Owner && tx.Scope == e.Scope {
			out["pending"] = harnessConfig(tx.Next)
			out["restart_state"] = tx.State
			out["outcome"] = tx.Outcome
		}
		return jsonText(out), nil
	case "diagnose":
		out := e.harnessStatus()
		out["config_error"] = errorInfo(e.global.Config.Validate())
		var integrity string
		err := e.Memory.DB.QueryRowContext(j.ctx, "PRAGMA quick_check").Scan(&integrity)
		if err != nil {
			out["memory_error"] = errorInfo(err)
		} else {
			out["memory_integrity"] = integrity
		}
		info, err := os.Stat(e.Config.WorkDir)
		out["work_directory_ok"] = err == nil && info.IsDir()
		id := a.JobID
		if id == "" {
			id = j.ID
		}
		if _, ok := e.Get(id); !ok {
			return "", errors.New("job not found in current memory scope")
		}
		var logs bytes.Buffer
		if err := logsCLI(j.ctx, e.AdminDir, []string{"--lines", "40", "--job", id}, &logs); err != nil {
			out["logs_error"] = errorInfo(err)
		} else {
			out["events_jsonl"] = logs.String()
		}
		out["connectivity_tested"] = false
		return jsonText(out), nil
	case "configure", "discard", "restart":
		if j.ServiceNotice != "" {
			return "", errors.New("this is a restart continuation; verify the outcome without changing configuration or restarting again")
		}
		if j.Kind != "" && j.Kind != "chat" || j.Owner == "alina" || j.Owner == "system" {
			return "", errors.New("harness changes require a user conversation")
		}
		if strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 1000 {
			return "", errors.New("state the user request authorizing this operation (1-1000 bytes)")
		}
		root := e.global
		root.restartMu.Lock()
		defer root.restartMu.Unlock()
		tx, err := readRestart(e.AdminDir)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil {
			if tx.Owner != j.Owner || tx.Scope != e.Scope {
				return "", errors.New("another configuration operation is pending")
			}
			if tx.State != "staged" {
				return "", errors.New("restart already requested; finish this turn")
			}
		} else {
			saved, err := checkedConfig(e.AdminDir)
			if err != nil {
				return "", err
			}
			raw, err := os.ReadFile(filepath.Join(e.AdminDir, "config.json"))
			if err != nil {
				return "", err
			}
			tx = restartTransaction{ID: randomID(), Scope: e.Scope, Owner: j.Owner, Previous: saved, Next: saved, Base: contentID(string(raw)), State: "staged"}
		}
		if a.Action == "discard" {
			if err := os.Remove(restartPath(e.AdminDir)); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			return "Staged configuration discarded; active and saved settings are unchanged.", nil
		}
		if a.Action == "configure" {
			next, err := patchHarnessConfig(tx.Next, a.Patch)
			if err != nil {
				return "", err
			}
			if err = validatePeopleState(e.AdminDir, next); err != nil {
				return "", err
			}
			tx.Next = next
			if err = writeJSON(restartPath(e.AdminDir), tx); err != nil {
				return "", err
			}
			e.Events.emit("harness.config_staged", nil, "job_id", j.ID)
			return jsonText(map[string]any{"staged": harnessConfig(next), "active_unchanged": true, "next": "Call harness restart when ready to apply, then finish with a restart notice."}), nil
		}
		if !root.managed {
			return "", errors.New("self-restart requires alina serve in background; foreground/supervised instances must use their external controller")
		}
		tx.JobID = j.ID
		tx.AfterID = randomID()
		tx.State = "waiting"
		tx.Created = time.Now()
		if err = writeJSON(restartPath(e.AdminDir), tx); err != nil {
			return "", err
		}
		if err = launchRestart(e.AdminDir, tx.ID); err != nil {
			tx.State = "staged"
			_ = writeJSON(restartPath(e.AdminDir), tx)
			return "", err
		}
		e.mu.Lock()
		j.Continuation = tx.AfterID
		err = e.persist(j)
		e.mu.Unlock()
		if err != nil {
			return "", err
		}
		e.Events.emit("harness.restart_requested", nil, "job_id", j.ID)
		return "Restart queued. Finish this turn now with a brief user-facing restart notice and any remaining work. Active configuration has not changed. The helper waits for delivery and idle work, restarts, then resumes this conversation with the verified outcome. Do not launch a shell restart.", nil
	}
	return "", errors.New("unknown harness action")
}
