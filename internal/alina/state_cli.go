package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func stateCLI(ctx context.Context, dir string, args []string, in *bufio.Reader, out io.Writer) error {
	if args[0] == "dream" || args[0] == "memory" && len(args) == 2 && args[1] == "reindex" {
		kind := args[0]
		if kind == "memory" {
			kind = "reindex"
		}
		var j Job
		if err := localRequest(ctx, dir, "POST", "/v1/memory/jobs", map[string]string{"kind": kind}, &j); err != nil {
			return err
		}
		return waitJob(ctx, dir, j.ID, in, out)
	}
	if args[0] == "memory" {
		if len(args) < 3 {
			return errors.New("usage: alina memory read PART | search QUERY | reindex")
		}
		query := strings.Join(args[2:], " ")
		var r any
		if err := localRequest(ctx, dir, "GET", "/v1/memory/"+args[1]+"?q="+url.QueryEscape(query), nil, &r); err != nil {
			return err
		}
		if text, ok := r.(string); ok {
			fmt.Fprintln(out, text)
		} else {
			fmt.Fprintln(out, jsonText(r))
		}
		return nil
	}
	if len(args) == 1 {
		var tasks []ScheduledTask
		if err := localRequest(ctx, dir, "GET", "/v1/tasks", nil, &tasks); err != nil {
			return err
		}
		fmt.Fprintln(out, formatTasks(tasks))
		return nil
	}
	if args[1] == "add" && len(args) == 5 {
		var task ScheduledTask
		if err := localRequest(ctx, dir, "POST", "/v1/tasks", map[string]any{"name": args[2], "cron": args[3], "prompt": args[4], "catch_up": true}, &task); err != nil {
			return err
		}
		fmt.Fprintln(out, formatTasks([]ScheduledTask{task}))
		return nil
	}
	if len(args) == 3 {
		return localRequest(ctx, dir, "POST", "/v1/tasks/"+args[2], map[string]string{"action": args[1]}, &map[string]bool{})
	}
	return errors.New("usage: alina tasks [add NAME CRON PROMPT | pause/resume/remove ID]")
}
func stateHandlers(mux *http.ServeMux, e *Engine, reply func(http.ResponseWriter, any), decode func(http.ResponseWriter, *http.Request, any) bool) {
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) { reply(w, e.Scheduler.List()) })
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Name, Cron, Prompt string
			CatchUp            bool `json:"catch_up"`
		}
		if !decode(w, r, &a) {
			return
		}
		t, err := e.Scheduler.Add(a.Name, a.Cron, a.Prompt, "local", a.CatchUp)
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
			out, err = e.Memory.Read(r.Context(), r.URL.Query().Get("q"), time.Now())
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
