package alina

import (
	"net/http"
	"strconv"
	"time"
)

func stateHandlers(mux *http.ServeMux, e *Engine, reply func(http.ResponseWriter, any), decode func(http.ResponseWriter, *http.Request, any) bool) {
	mux.HandleFunc("POST /v1/memory/focus", func(w http.ResponseWriter, r *http.Request) {
		if !e.Config.Memory.Enabled {
			http.Error(w, "memory disabled", 400)
			return
		}
		var a struct {
			ID     string
			Pinned *bool
		}
		if !decode(w, r, &a) {
			return
		}
		if err := e.Memory.Focus(r.Context(), a.ID, a.Pinned, time.Now()); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { reply(w, e.Scheduler.List()) })
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Name, Cron, Prompt, At string
			CatchUp                bool `json:"catch_up"`
		}
		if !decode(w, r, &a) {
			return
		}
		var t ScheduledTask
		var err error
		if a.At != "" {
			t, err = e.Scheduler.AddOnce(a.Name, a.At, a.Prompt, "local", "user", "")
		} else {
			t, err = e.Scheduler.Add(a.Name, a.Cron, a.Prompt, "local", a.CatchUp)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, t)
	})
	mux.HandleFunc("POST /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Action string }
		if !decode(w, r, &a) {
			return
		}
		if err := e.Scheduler.Change(r.PathValue("id"), a.Action, ""); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/memory/jobs", func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Kind string }
		if !decode(w, r, &a) {
			return
		}
		if a.Kind != "dream" && a.Kind != "reindex" {
			http.Error(w, "invalid memory job", 400)
			return
		}
		j, err := e.submit("memory-"+randomID(), "local", a.Kind, "", a.Kind)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, j)
	})
	mux.HandleFunc("GET /v1/memory/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !e.Config.Memory.Enabled {
			http.Error(w, "memory disabled", 400)
			return
		}
		var out any
		var err error
		switch r.PathValue("action") {
		case "read":
			offset := 0
			if raw := r.URL.Query().Get("offset"); raw != "" {
				offset, err = strconv.Atoi(raw)
				if err != nil || offset < 0 {
					http.Error(w, "invalid offset", 400)
					return
				}
			}
			out, err = e.Memory.ReadPage(r.Context(), r.URL.Query().Get("q"), offset, time.Now())
		case "search":
			out, err = e.Memory.Recall(r.Context(), r.URL.Query().Get("q"))
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, out)
	})
}
