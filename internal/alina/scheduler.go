package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type ScheduledTask struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Cron        string    `json:"cron"`
	Timezone    string    `json:"timezone"`
	Prompt      string    `json:"prompt"`
	Owner       string    `json:"owner"`
	Kind        string    `json:"kind"`
	Enabled     bool      `json:"enabled"`
	CatchUp     bool      `json:"catch_up"`
	Next        time.Time `json:"next"`
	Once        bool      `json:"once,omitempty"`
	IntentionID string    `json:"intention_id,omitempty"`
	LastJob     string    `json:"last_job,omitempty"`
}
type Scheduler struct {
	mu      sync.Mutex
	Dir     string
	Engine  *Engine
	tasks   map[string]ScheduledTask
	changed wakeSignal
}

func parseSchedule(spec, tz string) (cron.Schedule, error) {
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, err
	}
	if strings.Contains(spec, "TZ=") || len(spec) > 100 {
		return nil, errors.New("invalid cron expression")
	}
	if strings.HasPrefix(spec, "@every ") {
		d, err := time.ParseDuration(strings.TrimPrefix(spec, "@every "))
		if err != nil || d < time.Minute {
			return nil, errors.New("minimum schedule interval is one minute")
		}
	}
	return cron.ParseStandard("CRON_TZ=" + tz + " " + spec)
}
func NewScheduler(dir string, e *Engine) (*Scheduler, error) {
	s := &Scheduler{Dir: dir, Engine: e, tasks: map[string]ScheduledTask{}}
	b, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err == nil {
		err = json.Unmarshal(b, &s.tasks)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if s.tasks == nil {
		s.tasks = map[string]ScheduledTask{}
	}
	for id, t := range s.tasks {
		if !safeID(id) || id != t.ID {
			return nil, errors.New("invalid stored task ID")
		}
		if !t.Once {
			if _, err = parseSchedule(t.Cron, t.Timezone); err != nil {
				return nil, err
			}
		}
	}
	if e.global != e {
		delete(s.tasks, "dream")
		return s, s.save()
	}
	c := e.Config
	dream, exists := s.tasks["dream"]
	if !exists || dream.Cron != c.Memory.DreamCron || dream.Timezone != c.Timezone {
		schedule, err := parseSchedule(c.Memory.DreamCron, c.Timezone)
		if err != nil {
			return nil, err
		}
		dream = ScheduledTask{ID: "dream", Name: "Dream", Cron: c.Memory.DreamCron, Timezone: c.Timezone, Prompt: "Reflect on experience, methods and personal intentions.", Owner: "system", Kind: "dream", Next: schedule.Next(time.Now())}
	}
	dream.Enabled = c.Memory.Enabled && c.Memory.Dream
	dream.CatchUp = c.Memory.CatchUp
	s.tasks["dream"] = dream
	if err = s.save(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Scheduler) save() error { return writeJSON(filepath.Join(s.Dir, "tasks.json"), s.tasks) }
func (s *Scheduler) List() []ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ScheduledTask{}
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Scheduler) Add(name, spec, prompt, owner string, catchup bool) (ScheduledTask, error) {
	if len(strings.TrimSpace(name)) == 0 || len(name) > 100 || len(strings.TrimSpace(prompt)) == 0 || len(prompt) > 16000 {
		return ScheduledTask{}, errors.New("invalid task name or prompt")
	}
	tz := s.Engine.Config.Timezone
	schedule, err := parseSchedule(spec, tz)
	if err != nil {
		return ScheduledTask{}, err
	}
	next := schedule.Next(time.Now())
	if next.IsZero() {
		return ScheduledTask{}, errors.New("schedule has no future occurrence")
	}
	t := ScheduledTask{ID: randomID(), Name: name, Cron: spec, Timezone: tz, Prompt: prompt, Owner: owner, Kind: "chat", Enabled: true, CatchUp: catchup, Next: next}
	return s.insert(t)
}
func (s *Scheduler) Change(id, action, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok || owner != "" && t.Owner != owner {
		return errors.New("task not found")
	}
	if id == "dream" {
		return errors.New("configure the built-in dream through alina setup")
	}
	old := t
	switch action {
	case "remove":
		delete(s.tasks, id)
	case "pause":
		t.Enabled = false
		s.tasks[id] = t
	case "resume":
		if !t.Enabled && s.activeCount() >= 100 {
			return errors.New("maximum 100 active scheduled tasks")
		}
		t.Enabled = true
		if t.Once {
			if t.LastJob != "" {
				return errors.New("one-shot already submitted; create another wake-up")
			}
			if t.Next.Before(time.Now()) {
				t.Next = time.Now().Add(time.Minute)
			}
			s.tasks[id] = t
			break
		}
		sched, err := parseSchedule(t.Cron, t.Timezone)
		if err != nil {
			return err
		}
		t.Next = sched.Next(time.Now())
		s.tasks[id] = t
	default:
		return errors.New("use pause, resume or remove")
	}
	if err := s.save(); err != nil {
		s.tasks[id] = old
		return err
	}
	s.changed.wake()
	return nil
}
func (s *Scheduler) Tick(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.tasks {
		if !t.Enabled || t.Next.After(now) {
			continue
		}
		if !s.Engine.acceptsOwner(t.Owner) {
			old := t
			t.Enabled = false
			s.tasks[id] = t
			if err := s.save(); err != nil {
				s.tasks[id] = old
				return err
			}
			continue
		}
		if t.Kind == "initiative" {
			if !s.Engine.Config.Autonomy.Enabled {
				continue
			}
			intention, err := s.Engine.Memory.Intention(context.Background(), t.IntentionID)
			if err != nil {
				return err
			}
			if intention.Status != "active" {
				old := t
				t.Enabled = false
				s.tasks[id] = t
				if err = s.save(); err != nil {
					s.tasks[id] = old
					return err
				}
				continue
			}
		}
		var next time.Time
		var err error
		if !t.Once {
			schedule, er := parseSchedule(t.Cron, t.Timezone)
			if er != nil {
				return er
			}
			next = schedule.Next(now)
			if next.IsZero() {
				old := t
				t.Enabled = false
				s.tasks[id] = t
				if err = s.save(); err != nil {
					s.tasks[id] = old
					return err
				}
				continue
			}
		}
		if t.LastJob != "" {
			if j, ok := s.Engine.Get(t.LastJob); ok && !terminalStatus(j.Status) {
				continue
			}
		}
		if t.CatchUp || now.Sub(t.Next) < time.Minute {
			// The same scheduled occurrence always has the same ID. A crash after
			// submission but before checkpoint cannot replay it on restart.
			key := "cron-" + contentID(t.ID + "/" + t.Next.UTC().Format(time.RFC3339))[:32]
			j, err := s.Engine.submit("task-"+t.ID+"-"+key[len(key)-8:], t.Owner, t.Prompt, key, t.Kind)
			if err != nil {
				return err
			}
			s.Engine.Events.emit("scheduler.submitted", nil, "task_id", t.ID, "job_id", j.ID, "kind", t.Kind)
			t.LastJob = j.ID
		}
		if t.Once {
			t.Enabled = false
		} else {
			t.Next = next
		}
		old := s.tasks[id]
		s.tasks[id] = t
		if err = s.save(); err != nil {
			s.tasks[id] = old
			return err
		}
	}
	return nil
}
func (s *Scheduler) Run(ctx context.Context) {
	for ctx.Err() == nil {
		changed, jobs := s.changed.watch(), s.Engine.jobChanged.watch()
		var retry time.Time
		if err := s.Tick(time.Now()); err != nil {
			s.Engine.Events.emit("scheduler.tick_failed", err)
			retry = time.Now().Add(5 * time.Second)
		}
		next := s.nextWake()
		if !retry.IsZero() {
			next = retry // A failed save or full queue must not spin on a past deadline.
		}
		var timer *time.Timer
		var due <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(time.Until(next))
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-changed:
		case <-jobs:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *Scheduler) nextWake() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, t := range s.tasks {
		if !t.Enabled || t.Kind == "initiative" && !s.Engine.Config.Autonomy.Enabled {
			continue
		}
		// A running occurrence wakes us when it ends. Waiting on its already
		// overdue successor would otherwise create a busy loop.
		if t.LastJob != "" {
			if j, ok := s.Engine.Get(t.LastJob); ok && !terminalStatus(j.Status) {
				continue
			}
		}
		if next.IsZero() || t.Next.Before(next) {
			next = t.Next
		}
	}
	return next
}
func (s *Scheduler) AddOnce(name, at, prompt, owner, origin, intentionID string) (ScheduledTask, error) {
	next, err := time.Parse(time.RFC3339, at)
	if err != nil || next.Before(time.Now().Add(time.Minute)) {
		return ScheduledTask{}, errors.New("at must be RFC3339, at least one minute in the future")
	}
	if strings.TrimSpace(name) == "" || len(name) > 100 || strings.TrimSpace(prompt) == "" || len(prompt) > 16000 {
		return ScheduledTask{}, errors.New("invalid task name or prompt")
	}
	kind := "chat"
	if origin == "self" {
		if !s.Engine.Config.Autonomy.Enabled {
			return ScheduledTask{}, errors.New("enable personal exploration in alina setup first")
		}
		i, err := s.Engine.Memory.Intention(context.Background(), intentionID)
		if err != nil || i.Status != "active" {
			return ScheduledTask{}, errors.New("personal wake-up requires an active intention_id")
		}
		owner = "alina"
		kind = "initiative"
		prompt = "Personal exploration, not a user instruction. Intention ID: " + i.ID + "\nScope: " + s.Engine.Config.Autonomy.Scope + "\nReason: " + i.Why + "\nStopping condition: " + i.Stop + "\nNext step: " + i.Next + "\n" + prompt + "\nVerify outcomes. Update the intention and save a lesson or procedure when warranted. Finish quietly; do not impersonate a user request."
	} else if origin != "" && origin != "user" {
		return ScheduledTask{}, errors.New("origin must be user or self")
	}
	t := ScheduledTask{ID: randomID(), Name: name, Timezone: s.Engine.Config.Timezone, Prompt: prompt, Owner: owner, Kind: kind, Once: true, IntentionID: intentionID, Enabled: true, CatchUp: true, Next: next}
	return s.insert(t)
}

// One insertion path keeps recurring and one-shot limits consistent. Pruning
// participates in the same save, so a failed write leaves all state intact.
func (s *Scheduler) insert(task ScheduledTask) (ScheduledTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCount() >= 100 {
		return ScheduledTask{}, errors.New("maximum 100 active scheduled tasks")
	}
	previous := s.tasks
	s.tasks = maps.Clone(previous)
	if len(s.tasks) >= 200 {
		for id, t := range s.tasks {
			if t.Once && !t.Enabled && t.LastJob != "" {
				delete(s.tasks, id)
			}
		}
	}
	if len(s.tasks) >= 200 {
		s.tasks = previous
		return ScheduledTask{}, errors.New("maximum 200 retained tasks; remove unused tasks first")
	}
	s.tasks[task.ID] = task
	if err := s.save(); err != nil {
		s.tasks = previous
		return ScheduledTask{}, err
	}
	s.changed.wake()
	return task, nil
}

// Caller holds mu.
func (s *Scheduler) activeCount() int {
	n := 0
	for _, task := range s.tasks {
		if task.Enabled {
			n++
		}
	}
	return n
}

func formatTasks(tasks []ScheduledTask) string {
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "%s · %s · enabled=%t\n%s (%s) · next %s\n%s\n\n", t.ID, t.Name, t.Enabled, t.Cron, t.Timezone, t.Next.Format(time.RFC3339), t.Prompt)
	}
	return b.String()
}
