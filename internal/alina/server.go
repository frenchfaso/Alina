package alina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func socketPath(dir string) string { return filepath.Join(dir, "alina.sock") }

var errStateLocked = errors.New("Alina already running or state locked; stop it before changing files")

func lockDaemonState(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errStateLocked
	}
	return lock, nil
}

func Serve(ctx context.Context, dir string, c Config) (result error) {
	lock, e := lockDaemonState(dir)
	if e != nil {
		return e
	}
	defer lock.Close()
	events, e := openEventLog(dir)
	if e != nil {
		return e
	}
	defer events.Close()
	events.emit("service.starting", nil)
	defer func() { events.emit("service.stopped", result) }()
	if c.Version == 0 {
		c, e = LoadConfig(dir)
		if e != nil {
			return configError(e)
		}
	}
	if e = c.Validate(); e != nil {
		return e
	}
	client := newHTTPClient()
	p := &Provider{Config: c, Auth: &Auth{Dir: dir, Client: client}, Client: client}
	search := &Search{Config: c.Search, Provider: p, Client: client}
	engine, e := NewEngine(dir, c, p, search, events)
	if e != nil {
		events.emit("service.init_failed", e)
		return e
	}
	defer engine.Close()
	sock := socketPath(dir)
	if info, er := os.Lstat(sock); er == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("socket path is occupied by a non-socket file")
		}
		if e = os.Remove(sock); e != nil {
			return e
		}
	} else if !os.IsNotExist(er) {
		return er
	}
	ln, e := net.Listen("unix", sock)
	if e != nil {
		return e
	}
	defer ln.Close()
	if e = os.Chmod(sock, 0600); e != nil {
		return e
	}
	s := &http.Server{Handler: logAPI(handler(engine), events), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	serviceCtx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer func() { cancel(); background.Wait() }()
	background.Add(1)
	go func() { defer background.Done(); engine.Scheduler.Run(serviceCtx) }()
	if c.Telegram.Enabled {
		tg := NewTelegram(dir, c.Telegram, engine, client)
		background.Add(1)
		go func() { defer background.Done(); tg.Run(serviceCtx) }()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ln) }()
	events.emit("service.ready", nil, "provider", c.Provider, "model", c.Model)
	fmt.Fprintln(os.Stderr, "Alina", Version, "listening on", sock)
	select {
	case <-ctx.Done():
		cancel()
		engine.cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.Shutdown(shutdown)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func handler(e *Engine) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if job, ok := v.(Job); ok {
			w.Header().Set("X-Alina-Job-ID", job.ID)
		}
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 40<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			http.Error(w, "invalid request", 400)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"version": Version, "provider": e.Config.Provider, "model": e.Config.Model, "network_sandbox": sandboxAvailable(), "jobs": jobSummaries(e.Jobs("")), "logging": e.Events.health()})
	})
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Session     string `json:"session"`
			Message     string `json:"message"`
			RequestID   string `json:"request_id,omitempty"`
			Interactive bool   `json:"interactive,omitempty"`
		}
		if !decode(w, r, &req) {
			return
		}
		var j Job
		var err error
		if req.Interactive {
			j, err = e.Receive(req.Session, "local", req.Message, req.RequestID)
		} else {
			j, err = e.SubmitKey(req.Session, "local", req.Message, req.RequestID)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, j)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/steer", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Message   string `json:"message"`
			RequestID string `json:"request_id,omitempty"`
		}
		if !decode(w, r, &req) {
			return
		}
		j, err := e.Steer(r.PathValue("id"), "local", req.Message, req.RequestID)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, j)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, ok := e.Get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(w, j)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		j, err := e.Resume(r.PathValue("id"), "")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, j)
	})
	mux.HandleFunc("GET /v1/intentions", func(w http.ResponseWriter, r *http.Request) {
		i, err := e.Memory.Intentions(r.Context(), false)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		reply(w, i)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Cancel(r.PathValue("id"), ""); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/jobs/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ApprovalID string `json:"approval_id"`
			Scope      string `json:"scope"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := e.Approve(r.PathValue("id"), req.ApprovalID, req.Scope, ""); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/grants", func(w http.ResponseWriter, r *http.Request) { reply(w, e.Permissions.List()) })
	mux.HandleFunc("DELETE /v1/grants/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Permissions.Revoke(r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	stateHandlers(mux, e, reply, decode)
	return mux
}
func LocalClient(dir string) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath(dir))
	}}}
}

// Polls stay quiet. Mutations and failed requests log route templates and IDs,
// never query strings, headers or bodies.
func logAPI(next http.Handler, events *EventLog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := randomID()
		w.Header().Set("X-Alina-Request-ID", id)
		recorder := &apiResponse{ResponseWriter: w, status: 200}
		next.ServeHTTP(recorder, r)
		if r.Method == http.MethodGet && recorder.status < 400 {
			return
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		method := r.Method
		if method != "GET" && method != "POST" && method != "DELETE" {
			method = "OTHER"
		}
		attrs := []any{"request_id", id, "route", route, "method", method, "status", recorder.status, "duration_ms", time.Since(started).Milliseconds()}
		job := w.Header().Get("X-Alina-Job-ID")
		if job == "" {
			job = r.PathValue("id")
		}
		if safeID(job) {
			attrs = append(attrs, "job_id", job)
		}
		var err error
		if recorder.status >= 400 {
			err = &remoteHTTPError{Status: recorder.status}
		}
		events.emit("api.request", err, attrs...)
	})
}

type apiResponse struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *apiResponse) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *apiResponse) Write(p []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(p)
}

type jobSummary struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	Activity        string    `json:"activity,omitempty"`
	Created         time.Time `json:"created"`
	PendingSteering int       `json:"pending_steering,omitempty"`
}

func jobSummaries(jobs []Job) []jobSummary {
	out := make([]jobSummary, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobSummary{j.ID, j.Kind, j.Status, j.Activity, j.Created, j.PendingSteering})
	}
	return out
}
