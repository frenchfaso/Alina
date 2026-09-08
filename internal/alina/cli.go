package alina

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	if len(args) == 0 {
		args = []string{"help"}
	}
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(out, Version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprintln(out, `Alina — start simple, stay simple.

  alina setup                  Collega, verifica e avvia
  alina setup telegram         Collega o cambia il bot
  alina setup --advanced       Impostazioni avanzate
  alina login [device|browser]  Login ChatGPT dedicato
  alina serve                  Avvia il servizio in foreground
  alina chat [session]         Chat testuale con il servizio
  alina ask [-session ID] [-detach] "richiesta"
  alina status                 Stato e lavori
  alina job ID                 Segui un lavoro / rispondi al consenso
  alina cancel ID              Interrompi un lavoro
  alina steer ID "correzione"  Aggiorna un lavoro in corso
  alina resume ID              Riprendi verificando lo stato attuale
  alina intentions             Intenzioni personali e domande aperte
  alina approve JOB APPROVAL once|restart|always|deny
  alina permissions            Elenca i consensi
  alina revoke ID              Revoca un consenso
  alina dream                  Un momento di riflessione
  alina memory read focus|recent|archive|soul|YYYY-MM-DD|ID [offset]
  alina memory focus ID [pin|unpin]  Riporta una nota in primo piano
  alina memory search "query"   Ricerca in tutta la memoria
  alina memory reindex          Prepara gli embedding mancanti
  alina tasks                  Elenca i task ricorrenti
  alina tasks add "nome" "cron" "richiesta"
  alina tasks once "nome" "data RFC3339" "richiesta"
  alina tasks pause|resume|remove ID
  alina doctor [--live]        Diagnostica (live usa gli account configurati)

ALINA_HOME cambia la directory di configurazione e stato.
Esegui setup con il servizio fermo; --no-start configura senza avviare.`)
		return nil
	case "intentions":
		var r []Intention
		if err := localRequest(ctx, dir, "GET", "/v1/intentions", nil, &r); err != nil {
			return err
		}
		fmt.Fprintln(out, jsonText(r))
		return nil
	case "memory", "dream", "tasks":
		return stateCLI(ctx, dir, args, in, out)
	case "setup":
		return setupCLI(ctx, dir, args[1:], in, out)
	case "login":
		if e := requireStopped(dir); e != nil {
			return e
		}
		a := Auth{Dir: dir, Client: newHTTPClient()}
		if len(args) > 1 && args[1] == "browser" {
			return a.LoginBrowser(ctx, in, out)
		}
		return a.LoginDevice(ctx, out)
	case "serve":
		c, e := LoadConfig(dir)
		if e != nil {
			return configError(e)
		}
		return Serve(ctx, dir, c)
	case "doctor":
		return doctor(ctx, dir, len(args) > 1 && args[1] == "--live", out)
	case "status":
		var r struct {
			Version, Provider, Model string
			Jobs                     []Job
		}
		if e := localRequest(ctx, dir, "GET", "/v1/status", nil, &r); e != nil {
			return e
		}
		fmt.Fprintf(out, "Alina %s · %s / %s\n%s", r.Version, r.Provider, r.Model, formatJobs(r.Jobs))
		return nil
	case "permissions":
		var grants []Grant
		if e := localRequest(ctx, dir, "GET", "/v1/grants", nil, &grants); e != nil {
			return e
		}
		fmt.Fprintln(out, formatGrants(grants))
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: alina revoke ID")
		}
		return localRequest(ctx, dir, "DELETE", "/v1/grants/"+args[1], nil, &map[string]any{})
	case "resume":
		if len(args) != 2 {
			return errors.New("usage: alina resume ID")
		}
		var j Job
		if err := localRequest(ctx, dir, "POST", "/v1/jobs/"+args[1]+"/resume", nil, &j); err != nil {
			return err
		}
		return waitJob(ctx, dir, j.ID, in, out)
	case "steer":
		if len(args) < 3 || !safeID(args[1]) {
			return errors.New("usage: alina steer ID message")
		}
		var j Job
		if err := localRequest(ctx, dir, "POST", "/v1/jobs/"+args[1]+"/steer", map[string]string{"message": strings.Join(args[2:], " "), "request_id": randomID()}, &j); err != nil {
			return err
		}
		fmt.Fprintln(out, "Messaggio aggiunto al lavoro ·", j.ID)
		return nil
	case "cancel":
		if len(args) != 2 {
			return errors.New("usage: alina cancel ID")
		}
		return localRequest(ctx, dir, "POST", "/v1/jobs/"+args[1]+"/cancel", nil, &map[string]any{})
	case "approve":
		if len(args) != 4 {
			return errors.New("usage: alina approve JOB APPROVAL once|restart|always|deny")
		}
		return localRequest(ctx, dir, "POST", "/v1/jobs/"+args[1]+"/approve", map[string]string{"approval_id": args[2], "scope": args[3]}, &map[string]any{})
	case "job":
		if len(args) != 2 {
			return errors.New("usage: alina job ID")
		}
		return waitJob(ctx, dir, args[1], in, out)
	case "ask":
		f := flag.NewFlagSet("ask", flag.ContinueOnError)
		session := f.String("session", "local", "session ID")
		detach := f.Bool("detach", false, "return job ID immediately")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		text := strings.Join(f.Args(), " ")
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			b, e := io.ReadAll(io.LimitReader(in, 32001))
			if e != nil {
				return e
			}
			if len(b) > 32000 {
				return errors.New("stdin exceeds 32000 bytes")
			}
			if len(b) > 0 {
				text += "\n\n" + string(b)
			}
		}
		var j Job
		if e := localRequest(ctx, dir, "POST", "/v1/jobs", map[string]string{"session": *session, "message": strings.TrimSpace(text), "request_id": randomID()}, &j); e != nil {
			return e
		}
		if *detach {
			fmt.Fprintln(out, j.ID)
			return nil
		}
		return waitJob(ctx, dir, j.ID, in, out)
	case "chat":
		session := "local"
		if len(args) > 1 {
			session = args[1]
		}
		return chat(ctx, dir, session, in, out)
	default:
		return fmt.Errorf("unknown command %q; use alina help", args[0])
	}
}
func localRequest(ctx context.Context, dir, method, path string, body, out any) error {
	return requestJSON(ctx, LocalClient(dir), method, "http://alina"+path, body, nil, out)
}
func waitJob(ctx context.Context, dir, id string, in *bufio.Reader, out io.Writer) error {
	last := ""
	for {
		if ctx.Err() != nil {
			c, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_ = localRequest(c, dir, "POST", "/v1/jobs/"+id+"/cancel", nil, &map[string]any{})
			return ctx.Err()
		}
		var j Job
		if e := localRequest(ctx, dir, "GET", "/v1/jobs/"+id, nil, &j); e != nil {
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
				fmt.Fprintf(out, "In attesa: alina approve %s %s once|restart|always|deny\n", id, a.ID)
				return errors.New("approval requires an interactive terminal; job remains pending")
			}
			fmt.Fprint(out, "> ")
			line, e := readLine(ctx, in)
			if e != nil {
				if ctx.Err() != nil {
					c, stop := context.WithTimeout(context.Background(), 3*time.Second)
					defer stop()
					_ = localRequest(c, dir, "POST", "/v1/jobs/"+id+"/cancel", nil, &map[string]any{})
				}
				return e
			}
			scope := map[string]string{"1": "once", "2": "restart", "3": "always", "4": "deny"}[strings.TrimSpace(line)]
			if scope == "" {
				continue
			}
			if e = localRequest(ctx, dir, "POST", "/v1/jobs/"+id+"/approve", map[string]string{"approval_id": a.ID, "scope": scope}, &map[string]any{}); e != nil {
				return e
			}
		}
		if terminalStatus(j.Status) {
			if j.PendingSteering > 0 {
				fmt.Fprintln(out, "Messaggi salvati in attesa: alina resume", j.ID)
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
		fmt.Fprintf(&b, "%s · %s · %s", j.ID, j.Status, truncate(j.Input, 100))
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
func requireStopped(dir string) error {
	conn, e := net.DialTimeout("unix", socketPath(dir), time.Second)
	if e == nil {
		conn.Close()
		return errors.New("stop 'alina serve' before changing configuration or login")
	}
	return nil
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
func doctor(ctx context.Context, dir string, live bool, out io.Writer) error {
	c, e := LoadConfig(dir)
	if e != nil {
		return configError(e)
	}
	fmt.Fprintf(out, "Alina %s\nProvider: %s / %s\nDirectory: %s\nNetwork sandbox: %t\n", Version, c.Provider, c.Model, c.WorkDir, sandboxAvailable())
	fmt.Fprintf(out, "Context: %d tokens; compact above %d (95%%)\nReasoning: %s; dream: %s; checkpoint: %s; verbosity: %s; model timeout: %ds\n", c.ContextTokens, c.ContextTokens*95/100, c.ReasoningEffort, c.DreamEffort, c.CheckpointEffort, c.Verbosity, c.ModelTimeout)
	fmt.Fprintf(out, "Personal exploration: %t · %d model calls/day · %d minutes/run · network policy: %s\n", c.Autonomy.Enabled, c.Autonomy.MaxCalls, c.Autonomy.Minutes, c.NetworkPolicy)
	fmt.Fprintf(out, "Memory: %t · dream: %t (%s, %s) · embeddings: %t\n", c.Memory.Enabled, c.Memory.Dream, c.Memory.DreamCron, c.Timezone, c.Memory.EmbeddingURL != "")
	markitdown, _ := exec.LookPath("markitdown")
	if markitdown == "" {
		markitdown = "not on PATH (optional; see workspace/procedures/markitdown.md)"
	}
	fmt.Fprintln(out, "MarkItDown:", markitdown)
	_, authErr := os.Stat(filepath.Join(dir, "chatgpt.json"))
	fmt.Fprintf(out, "ChatGPT login file: %t\nOpenCode key: %t\nTavily key: %t\nBrave key: %t\nOpenAI search API key: %t\nTelegram enabled: %t\n", authErr == nil, c.OpenCodeKey != "", c.Search.TavilyKey != "", c.Search.BraveKey != "", c.Search.OpenAIKey != "", c.Telegram.Enabled)
	if !live {
		return nil
	}
	client := newHTTPClient()
	p := &Provider{Config: c, Client: client, Auth: &Auth{Dir: dir, Client: client}}
	failed := false
	m, er := p.Complete(ctx, "doctor-"+randomID(), []Message{{Role: "system", Content: "Reply briefly."}, {Role: "user", Content: "Reply with ALINA_OK only."}}, nil, nil)
	if er != nil {
		fmt.Fprintln(out, "Model FAIL:", er)
		failed = true
	} else {
		fmt.Fprintln(out, "Model OK:", truncate(m.Content, 150))
		if m.Usage != nil {
			fmt.Fprintln(out, "Model usage:", jsonText(m.Usage))
		}
	}
	s := Search{Config: c.Search, Provider: p, Client: client}
	for _, name := range []string{"openai", "tavily", "brave"} {
		enabled := name == "openai" && (authErr == nil || c.Search.OpenAIKey != "") || name == "tavily" && c.Search.TavilyKey != "" || name == "brave" && c.Search.BraveKey != ""
		if !enabled {
			continue
		}
		r, er := s.Run(ctx, "doctor-"+randomID(), name, "Termux official website")
		if er != nil {
			fmt.Fprintln(out, name, "FAIL:", er)
			failed = true
		} else {
			fmt.Fprintln(out, name, "OK:", truncate(r, 300))
		}
	}
	if c.Telegram.Enabled {
		t := NewTelegram(dir, c.Telegram, nil, client)
		var me struct {
			Username string `json:"username"`
		}
		if er := t.api(ctx, "getMe", nil, &me); er != nil {
			fmt.Fprintln(out, "Telegram FAIL:", er)
			failed = true
		} else {
			fmt.Fprintln(out, "Telegram bot OK:", me.Username)
		}
	}
	if c.Memory.Enabled && c.Memory.EmbeddingURL != "" {
		memory := Memory{Config: c}
		if vec, er := memory.embedding(ctx, "Alina embedding connectivity check"); er != nil {
			fmt.Fprintln(out, "Embedding FAIL:", er)
			failed = true
		} else {
			fmt.Fprintln(out, "Embedding OK · dimensions:", len(vec))
		}
	}
	if failed {
		return errors.New("one or more live checks failed")
	}
	return nil
}
