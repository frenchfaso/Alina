package alina

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type steeringInput struct {
	Origin  string  `json:"origin"`
	Message Message `json:"message"`
}

// Interactive messages steer the oldest accepting chat in this conversation.
// Explicit submissions and scheduled work retain their ordinary FIFO behavior.
func (e *Engine) Receive(session, owner, input, key string, attachments ...Attachment) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if key != "" {
		old, item, err := e.steeringReceiptLocked(key)
		if err == nil {
			if old.Owner != owner || old.Session != session || item.Message.Content != input || jsonText(item.Message.Attachments) != jsonText(attachments) {
				return Job{}, errors.New("request ID conflict")
			}
			return old, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Job{}, err
		}
		if _, exists := e.getLocked(key); exists {
			return e.submitLocked(session, owner, input, key, "chat", "", attachments...)
		}
	}
	var target *runningJob
	for _, j := range e.jobs {
		if j.Session == session && j.Owner == owner && (j.Kind == "chat" || j.Kind == "") && j.accepting && j.ctx.Err() == nil && (target == nil || j.Created.Before(target.Created)) {
			target = j
		}
	}
	if target != nil {
		return e.steerLocked(target, input, key, attachments...)
	}
	return e.submitLocked(session, owner, input, key, "chat", "", attachments...)
}

func (e *Engine) Steer(id, owner, input, key string) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if key != "" {
		old, item, err := e.steeringReceiptLocked(key)
		if err == nil {
			if old.Owner != owner || item.Origin != id || item.Message.Content != input {
				return Job{}, errors.New("request ID conflict")
			}
			return old, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Job{}, err
		}
	}
	j, ok := e.jobs[id]
	if !ok || j.Owner != owner {
		return Job{}, errors.New("active chat job not found")
	}
	return e.steerLocked(j, input, key)
}

func (e *Engine) steeringReceiptLocked(key string) (Job, steeringInput, error) {
	var target, payload string
	var item steeringInput
	if err := e.Memory.DB.QueryRow("SELECT job,payload FROM steering WHERE id=?", key).Scan(&target, &payload); err != nil {
		return Job{}, item, err
	}
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return Job{}, item, err
	}
	j, ok := e.getLocked(target)
	if !ok {
		return Job{}, item, errors.New("steering target is missing")
	}
	return j, item, nil
}

// Telegram checks receipts before downloading an attachment again on retries.
func (e *Engine) steeringReceived(key, owner string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, _, err := e.steeringReceiptLocked(key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if j.Owner != owner {
		return false, errors.New("steering owner mismatch")
	}
	return true, nil
}

func (e *Engine) steerLocked(j *runningJob, input, key string, attachments ...Attachment) (Job, error) {
	if !j.accepting || j.ctx.Err() != nil || j.Kind != "chat" && j.Kind != "" {
		return Job{}, errors.New("job no longer accepts steering; send a new message")
	}
	if strings.TrimSpace(input) == "" || len(input) > 32000 || len(attachments) > 4 {
		return Job{}, errors.New("steering requires 1-32000 bytes and at most four attachments")
	}
	if key == "" {
		key = randomID()
	}
	if !safeID(key) {
		return Job{}, errors.New("invalid request ID")
	}
	if _, exists := e.getLocked(key); exists {
		return Job{}, errors.New("request ID conflict")
	}
	if j.PendingSteering >= 16 {
		return Job{}, errors.New("steering queue full (16 pending messages)")
	}
	item := steeringInput{Origin: j.ID, Message: Message{ArchiveID: "steer-" + contentID(key)[:32], Role: "user", Content: input, Attachments: append([]Attachment(nil), attachments...)}}
	tx, err := e.Memory.DB.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO steering(id,job,payload) VALUES(?,?,?)", key, j.ID, jsonText(item)); err != nil {
		return Job{}, err
	}
	j.PendingSteering++
	_, err = tx.Exec(saveJobSQL, j.ID, j.Owner, j.Created.Format(time.RFC3339Nano), j.Status, jsonText(j.Job))
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		j.PendingSteering--
		return Job{}, err
	}
	e.Events.emit("steering.queued", nil, "job_id", j.ID, "pending", j.PendingSteering)
	select {
	case j.steerSignal <- struct{}{}:
	default:
	}
	return cloneJob(j.Job), nil
}

func (e *Engine) hasSteering(j *runningJob) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return j.PendingSteering > 0
}

// The final check and closing the mailbox share the same lock as Receive.
// A message racing completion is either consumed here or starts a new job.
func (e *Engine) closeMailbox(j *runningJob) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j.PendingSteering > 0 {
		return false
	}
	j.accepting = false
	return true
}

func (e *Engine) drainSteering(j *runningJob, history *[]Message, appendMessage func(Message) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j.PendingSteering == 0 {
		return nil
	}
	rows, err := e.Memory.DB.Query("SELECT payload FROM steering WHERE job=? AND applied=0 ORDER BY rowid", j.ID)
	if err != nil {
		return err
	}
	var pending []steeringInput
	for rows.Next() {
		var payload string
		var item steeringInput
		if err = rows.Scan(&payload); err == nil {
			err = json.Unmarshal([]byte(payload), &item)
		}
		if err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range pending {
		found := false
		for _, m := range *history {
			if m.ArchiveID == item.Message.ArchiveID {
				found = true
				break
			}
		}
		if !found {
			if err = appendMessage(item.Message); err != nil {
				return err
			}
		}
	}
	// Transcript is durable first. Stable archive IDs make recovery idempotent
	// if the daemon stops before acknowledging the mailbox in SQLite.
	tx, err := e.Memory.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE steering SET applied=1 WHERE job=? AND applied=0", j.ID); err != nil {
		return err
	}
	previous := j.PendingSteering
	j.PendingSteering = 0
	_, err = tx.Exec(saveJobSQL, j.ID, j.Owner, j.Created.Format(time.RFC3339Nano), j.Status, jsonText(j.Job))
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		j.PendingSteering = previous
		return err
	}
	select {
	case <-j.steerSignal:
	default:
	}
	return nil
}

func (e *Engine) persistSubmission(j *runningJob, resumeFrom string) error {
	if resumeFrom == "" {
		return e.persist(j)
	}
	old, ok := e.getLocked(resumeFrom)
	if !ok {
		return errors.New("resume target missing")
	}
	tx, err := e.Memory.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT count(*) FROM steering WHERE job=? AND applied=0", old.ID).Scan(&j.PendingSteering); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE steering SET job=? WHERE job=? AND applied=0", j.ID, old.ID); err != nil {
		return err
	}
	old.PendingSteering = 0
	for _, job := range []Job{old, j.Job} {
		if _, err = tx.Exec(saveJobSQL, job.ID, job.Owner, job.Created.Format(time.RFC3339Nano), job.Status, jsonText(job)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
