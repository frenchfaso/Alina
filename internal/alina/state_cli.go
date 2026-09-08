package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
		if len(args) >= 3 && args[1] == "focus" {
			request := map[string]any{"id": args[2]}
			if len(args) == 4 {
				if args[3] != "pin" && args[3] != "unpin" {
					return errors.New("use pin or unpin")
				}
				request["pinned"] = args[3] == "pin"
			} else if len(args) > 4 {
				return errors.New("usage: alina memory focus ID [pin|unpin]")
			}
			var result map[string]bool
			if err := localRequest(ctx, dir, "POST", "/v1/memory/focus", request, &result); err != nil {
				return err
			}
			fmt.Fprintln(out, "Attention updated.")
			return nil
		}
		if len(args) < 3 {
			return errors.New("usage: alina memory read PART | search QUERY | reindex")
		}
		query := strings.Join(args[2:], " ")
		offset := ""
		if args[1] == "read" && len(args) == 4 {
			n, err := strconv.Atoi(args[3])
			if err != nil || n < 0 {
				return errors.New("offset must be non-negative")
			}
			query = args[2]
			offset = "&offset=" + args[3]
		}
		var r any
		if err := localRequest(ctx, dir, "GET", "/v1/memory/"+args[1]+"?q="+url.QueryEscape(query)+offset, nil, &r); err != nil {
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
	if (args[1] == "add" || args[1] == "once") && len(args) == 5 {
		var task ScheduledTask
		request := map[string]any{"name": args[2], "prompt": args[4], "catch_up": true}
		if args[1] == "once" {
			request["at"] = args[3]
		} else {
			request["cron"] = args[3]
		}
		if err := localRequest(ctx, dir, "POST", "/v1/tasks", request, &task); err != nil {
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
