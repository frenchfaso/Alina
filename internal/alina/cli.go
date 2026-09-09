package alina

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func Main(args []string) error {
	if len(args) > 0 && args[0] == "__sandbox" {
		return sandboxExec(args[1:])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir := Home()
	if len(args) == 1 && args[0] == "__daemon" {
		pipe := os.NewFile(3, "readiness")
		if pipe == nil {
			return errors.New("missing daemon readiness pipe")
		}
		defer pipe.Close()
		if info, err := pipe.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return errors.New("invalid daemon readiness pipe")
		}
		err := Serve(ctx, dir, Config{}, func() { fmt.Fprintln(pipe, "ready"); pipe.Close() })
		if err != nil {
			fmt.Fprintln(pipe, err.Error())
		}
		return err
	}
	if len(args) == 0 {
		args = []string{"help"}
	}
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	return commandCLI(ctx, dir, args, in, out, term.IsTerminal(int(os.Stdin.Fd())))
}

const cliHelp = `Alina — start simple, stay simple.

  alina setup                 Connect accounts and get started
  alina serve [stop|restart]  Start in background, stop or restart
  alina chat ["message"]       Chat, or send one request (also accepts stdin)
  alina config [check|apply]   Show redacted JSON, validate or patch via stdin
  alina status [JOB]           Service or job status as JSON
  alina doctor [--live|--fix]  Diagnose; repair only safe local setup issues
  alina logs [--follow]        Read operational logs as JSON Lines
  alina api METHOD PATH [JSON] Access the local API; no arguments list endpoints

Use --version for the version. ALINA_HOME selects the state directory.
Config, status, doctor and api output JSON; errors go to stderr, exit code 1.
setup --no-start configures without starting; setup telegram pairs the bot.
setup login [browser] reconnects ChatGPT; setup --advanced edits preferences.
serve --foreground runs attached for debugging or an external service manager.
logs accepts --job ID, --level ERROR, --lines 1-1000 (default 100).
Config patches are JSON objects on stdin: omitted fields keep their values.
`

func commandCLI(ctx context.Context, dir string, args []string, in *bufio.Reader, out io.Writer, interactive bool) error {
	if len(args) == 0 {
		args = []string{"help"}
	}
	switch args[0] {
	case "--version":
		if len(args) != 1 {
			return errors.New("usage: alina --version")
		}
		fmt.Fprintln(out, Version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(out, cliHelp)
		return nil
	case "setup":
		return setupCLI(ctx, dir, args[1:], in, out)
	case "serve":
		return serveCLI(ctx, dir, args[1:], out)
	case "config":
		return configCLI(ctx, dir, args[1:], in, out)
	case "doctor":
		return doctorCLI(ctx, dir, args[1:], out)
	case "logs":
		return logsCLI(ctx, dir, args[1:], out)
	case "api":
		return apiCLI(ctx, dir, args[1:], in, out)
	case "status":
		if len(args) > 2 {
			return errors.New("usage: alina status [JOB]")
		}
		path := "/v1/status"
		if len(args) == 2 {
			if !safeID(args[1]) {
				return errors.New("invalid job ID")
			}
			path = "/v1/jobs/" + args[1]
		}
		var result any
		if err := localRequest(ctx, dir, "GET", path, nil, &result); err != nil {
			return err
		}
		return printJSON(out, result)
	case "chat":
		if len(args) == 1 && interactive {
			return chat(ctx, dir, "local", in, out)
		}
		text := strings.Join(args[1:], " ")
		if !interactive {
			b, err := io.ReadAll(io.LimitReader(in, 32001))
			if err != nil {
				return err
			}
			if len(b) > 32000 {
				return errors.New("stdin exceeds 32000 bytes")
			}
			if len(b) > 0 {
				text += "\n\n" + string(b)
			}
		}
		text = strings.TrimSpace(text)
		if len(text) == 0 || len(text) > 32000 {
			return errors.New("message must be 1-32000 bytes")
		}
		var j Job
		if err := localRequest(ctx, dir, "POST", "/v1/jobs", map[string]any{"session": "local", "message": text, "request_id": randomID(), "interactive": true}, &j); err != nil {
			return err
		}
		return waitJob(ctx, dir, j.ID, in, out)
	default:
		return errors.New("unknown command; run alina help")
	}
}

func printJSON(out io.Writer, v any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

func localRequest(ctx context.Context, dir, method, path string, body, out any) error {
	client := LocalClient(dir)
	defer client.CloseIdleConnections()
	return requestLocalJSON(ctx, client, method, path, body, out)
}
func waitJob(ctx context.Context, dir, id string, in *bufio.Reader, out io.Writer) error {
	client := LocalClient(dir)
	defer client.CloseIdleConnections()
	last := ""
	for {
		if ctx.Err() != nil {
			c, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_ = requestLocalJSON(c, client, "POST", "/v1/jobs/"+id+"/cancel", nil, &map[string]any{})
			return ctx.Err()
		}
		var j Job
		if e := requestLocalJSON(ctx, client, "GET", "/v1/jobs/"+id, nil, &j); e != nil {
			if ctx.Err() != nil {
				continue
			}
			return e
		}
		if j.Activity != "" && j.Activity != last {
			fmt.Fprintln(out, "·", j.Activity)
			last = j.Activity
		}
		if j.Approval != nil {
			a := j.Approval
			fmt.Fprintf(out, "\nConsenso · %s\nDirectory: %s\n%s\n\n1) Solo una volta\n2) Fino al riavvio di Alina\n3) Fino a revoca\n4) Nega\n", a.Action.Reason, a.Action.Directory, a.Action.Command)
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintln(out, "In attesa:", approvalCommand(id, a.ID))
				return errors.New("approval requires an interactive terminal; job remains pending")
			}
			fmt.Fprint(out, "> ")
			line, e := readLine(ctx, in)
			if e != nil {
				if ctx.Err() != nil {
					c, stop := context.WithTimeout(context.Background(), 3*time.Second)
					defer stop()
					_ = requestLocalJSON(c, client, "POST", "/v1/jobs/"+id+"/cancel", nil, &map[string]any{})
				}
				return e
			}
			scope := map[string]string{"1": "once", "2": "restart", "3": "always", "4": "deny"}[strings.TrimSpace(line)]
			if scope == "" {
				continue
			}
			if e = requestLocalJSON(ctx, client, "POST", "/v1/jobs/"+id+"/approve", map[string]string{"approval_id": a.ID, "scope": scope}, &map[string]any{}); e != nil {
				return e
			}
		}
		if terminalStatus(j.Status) {
			if j.PendingSteering > 0 {
				fmt.Fprintln(out, "Messaggi salvati in attesa:", resumeCommand(j.ID))
			}
			if j.Output != "" {
				fmt.Fprintln(out, j.Output)
			}
			if j.Error != "" {
				return errors.New(j.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			continue
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func formatJobs(jobs []Job) string {
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Created.After(jobs[j].Created) })
	if len(jobs) == 0 {
		return "Nessun lavoro.\n"
	}
	if len(jobs) > 12 {
		jobs = jobs[:12]
	}
	var b strings.Builder
	for _, j := range jobs {
		label := j.Kind
		if j.Input != "" {
			label = truncate(j.Input, 100)
		}
		fmt.Fprintf(&b, "%s · %s · %s", j.ID, j.Status, label)
		if j.PendingSteering > 0 {
			fmt.Fprintf(&b, " · %d messaggi in attesa", j.PendingSteering)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
func formatGrants(gs []Grant) string {
	if len(gs) == 0 {
		return "Nessun consenso riutilizzabile."
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].ID < gs[j].ID })
	var b strings.Builder
	for _, g := range gs {
		scope := "fino al riavvio"
		if g.Scope == "always" {
			scope = "fino a revoca"
		}
		fmt.Fprintf(&b, "%s\n%s · %s\n%s\n\n", g.ID, scope, g.Action.Directory, g.Action.Command)
	}
	return b.String()
}
func readLine(ctx context.Context, in *bufio.Reader) (string, error) {
	if ctx == nil {
		return in.ReadString('\n')
	}
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() { s, e := in.ReadString('\n'); ch <- result{s, e} }()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type wizard struct {
	dir string
	ctx context.Context
	in  *bufio.Reader
	out io.Writer
	err error
}

func (w *wizard) ask(label, def string) string {
	if w.err != nil {
		return def
	}
	if def != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprint(w.out, label+": ")
	}
	s, e := readLine(w.ctx, w.in)
	if e != nil {
		w.err = e
		return def
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}
func (w *wizard) yes(label string, def bool) bool {
	d := "n"
	if def {
		d = "s"
	}
	for w.err == nil {
		switch strings.ToLower(w.ask(label+" (s/n)", d)) {
		case "s", "si", "sì", "y", "yes":
			return w.err == nil
		case "n", "no":
			return false
		}
		fmt.Fprintln(w.out, "Scrivi s oppure n.")
	}
	return false
}
func (w *wizard) secret(label, existing string) string {
	if w.err != nil {
		return existing
	}
	hint := "Invio per dopo"
	if existing != "" {
		hint = "Invio mantiene; - cancella"
	}
	fmt.Fprintf(w.out, "%s (%s): ", label, hint)
	var s string
	if w.out == os.Stdout && term.IsTerminal(int(os.Stdin.Fd())) {
		line, e := terminalSecret(w.ctx, w.in, w.out)
		if e != nil {
			fmt.Fprintln(w.out)
			w.err = e
			return existing
		}
		s = line
	} else {
		line, e := readLine(w.ctx, w.in)
		if e != nil {
			w.err = e
			return existing
		}
		s = line
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return existing
	}
	if s == "-" {
		return ""
	}
	return s
}

func approvalCommand(job, id string) string {
	if !safeID(job) || !safeID(id) {
		return "alina api"
	}
	body, _ := json.Marshal(map[string]string{"approval_id": id, "scope": "once"})
	return fmt.Sprintf("alina api POST /v1/jobs/%s/approve '%s'", job, body)
}
func resumeCommand(id string) string {
	if !safeID(id) {
		return "alina api"
	}
	return "alina api POST /v1/jobs/" + id + "/resume"
}

func WriteError(out io.Writer, err error) {
	info := errorInfo(err)
	info["message"] = err.Error()
	_ = json.NewEncoder(out).Encode(map[string]any{"ok": false, "error": info})
}
