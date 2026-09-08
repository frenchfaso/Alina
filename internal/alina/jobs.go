package alina

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Job payloads live in SQLite and are loaded only for active work or explicit
// history requests. Legacy JSON remains an untouched migration backup.
func (e *Engine) loadJobs() error {
	files, err := filepath.Glob(filepath.Join(e.Dir, "jobs", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range files {
		var j Job
		b, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(b, &j)
		}
		if err != nil || !safeID(j.ID) || !safeID(j.Session) {
			e.Events.emit("job.legacy_unreadable", err)
			continue
		}
		if _, err = e.Memory.DB.Exec("INSERT OR IGNORE INTO jobs VALUES(?,?,?,?,?)", j.ID, j.Owner, j.Created.Format(time.RFC3339Nano), j.Status, jsonText(j)); err != nil {
			return err
		}
	}
	rows, err := e.Memory.DB.Query("SELECT payload FROM jobs WHERE status IN ('queued','running','approval')")
	if err != nil {
		return err
	}
	var interrupted []Job
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		var j Job
		if err = json.Unmarshal([]byte(payload), &j); err != nil {
			rows.Close()
			return err
		}
		interrupted = append(interrupted, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range interrupted {
		e.Events.emit("job.interrupted", nil, "job_id", j.ID)
		j.Status = "interrupted"
		j.Approval = nil
		j.Error = "Service restarted; use resume to recover the intention and verify the current state. No command was replayed."
		if err = e.persist(&runningJob{Job: j}); err != nil {
			return err
		}
	}
	return nil
}

const saveJobSQL = `INSERT INTO jobs VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload`

func (e *Engine) persist(j *runningJob) error {
	_, err := e.Memory.DB.Exec(saveJobSQL, j.ID, j.Owner, j.Created.Format(time.RFC3339Nano), j.Status, jsonText(j.Job))
	if err == nil && (j.Status == "approval" || terminalStatus(j.Status)) {
		e.jobChanged.wake()
	}
	return err
}
func (e *Engine) getLocked(id string) (Job, bool) {
	if !safeID(id) {
		return Job{}, false
	}
	if j, ok := e.jobs[id]; ok {
		return cloneJob(j.Job), true
	}
	var payload string
	if err := e.Memory.DB.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&payload); err != nil {
		return Job{}, false
	}
	var j Job
	if json.Unmarshal([]byte(payload), &j) != nil || j.ID != id {
		return Job{}, false
	}
	return j, true
}
func (e *Engine) Get(id string) (Job, bool) { e.mu.Lock(); defer e.mu.Unlock(); return e.getLocked(id) }
func (e *Engine) Jobs(owner string) []Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Job{}
	for _, j := range e.jobs {
		if owner == "" || owner == j.Owner {
			out = append(out, cloneJob(j.Job))
		}
	}
	query := "SELECT payload FROM jobs WHERE status NOT IN ('queued','running','approval')"
	args := []any{}
	if owner != "" {
		query += " AND owner=?"
		args = append(args, owner)
	}
	query += " ORDER BY created DESC LIMIT 50"
	rows, err := e.Memory.DB.Query(query, args...)
	if err != nil {
		e.Events.emit("job.history_failed", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			break
		}
		var j Job
		if json.Unmarshal([]byte(payload), &j) == nil {
			out = append(out, j)
		}
	}
	return out
}
