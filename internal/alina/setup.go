package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

func setupCLI(ctx context.Context, dir string, args []string, in *bufio.Reader, out io.Writer) error {
	if len(args) > 0 && args[0] == "login" {
		if len(args) > 2 || len(args) == 2 && args[1] != "browser" {
			return errors.New("usage: alina setup login [browser]")
		}
		lock, err := lockDaemonState(dir)
		if err != nil {
			return err
		}
		defer lock.Close()
		auth := Auth{Dir: dir, Client: newHTTPClient()}
		if len(args) == 2 {
			return auth.LoginBrowser(ctx, in, out)
		}
		return auth.LoginDevice(ctx, out)
	}
	noStart, telegramOnly, advanced := false, false, false
	for _, arg := range args {
		switch arg {
		case "--no-start":
			noStart = true
		case "telegram":
			telegramOnly = true
		case "--advanced":
			advanced = true
		default:
			return errors.New("usage: alina setup [telegram | --advanced] [--no-start]")
		}
	}
	if advanced && telegramOnly {
		return errors.New("choose setup telegram or setup --advanced")
	}
	if advanced {
		return SetupAdvanced(ctx, dir, in, out)
	}
	if err := setupQuick(ctx, dir, in, out, newHTTPClient(), telegramOnly); err != nil {
		return err
	}
	if telegramOnly || noStart || !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(out, "Avvia: alina serve · Chat locale: alina chat")
		return nil
	}
	w := &wizard{dir: dir, ctx: ctx, in: in, out: out}
	if !w.yes("Avvia Alina ora", true) {
		fmt.Fprintln(out, "Quando vuoi: alina serve")
		return w.err
	}
	c, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Alina resta attiva in questo terminale; Ctrl-C la ferma.\nChat locale: alina chat in un altro terminale.")
	return Serve(ctx, dir, c)
}

func setupQuick(ctx context.Context, dir string, in *bufio.Reader, out io.Writer, client *http.Client, telegramOnly bool) error {
	lock, err := lockDaemonState(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	c, err := LoadConfig(dir)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return err
	}
	if fresh && telegramOnly {
		return errors.New("prima esegui alina setup")
	}
	w := &wizard{dir: dir, ctx: ctx, in: in, out: out}
	fmt.Fprintln(out, "Alina · setup")
	if fresh {
		c.NetworkPolicy = "declared"
		c.Autonomy.Enabled, c.Autonomy.Search = true, true
		c.Timezone = setupTimezone(ctx)
		for c.Timezone == "" && w.err == nil {
			zone := w.ask("Fuso orario (es. Europe/Rome)", "UTC")
			if _, err := time.LoadLocation(zone); err == nil {
				c.Timezone = zone
			} else {
				fmt.Fprintln(out, "Fuso orario non riconosciuto.")
			}
		}
	}
	if w.err != nil {
		return w.err
	}
	if telegramOnly {
		if err := w.connectTelegram(&c, client, true); err != nil {
			return err
		}
		if c.Telegram.Enabled {
			if err := w.people(&c, client, true); err != nil {
				return err
			}
		}
		if err := SaveConfig(dir, c); err != nil {
			return err
		}
		fmt.Fprintln(out, "Telegram e persone salvati.")
		return nil
	}
	info, err := os.Stat(c.WorkDir)
	if err != nil || !info.IsDir() {
		return errors.New("directory di lavoro non disponibile; modificala con alina setup --advanced")
	}
	if fresh {
		fmt.Fprintln(out, "ChatGPT + ricerca OpenAI. Memoria, dream e iniziative attivi.\nDownload e installazioni richiedono consenso; strumenti locali liberi.")
	}
	if c.Provider == "chatgpt" || c.Search.Default == "openai" && c.Search.OpenAIKey == "" {
		if err := w.connectChatGPT(dir, client); err != nil {
			return err
		}
	}
	if c.Provider == "opencode-go" && c.OpenCodeKey == "" {
		c.OpenCodeKey = w.secret("Chiave OpenCode Go", "")
		if c.OpenCodeKey == "" {
			return errors.New("serve una chiave OpenCode Go; riprendi con alina setup")
		}
	}
	if !fresh && !c.Telegram.Enabled && c.Telegram.Token != "" {
		c.Telegram.Enabled = w.yes("Riattivare Telegram già configurato", true)
	}
	if fresh || c.Telegram.Enabled {
		if err := w.connectTelegram(&c, client, fresh); err != nil {
			return err
		}
	}
	if w.err != nil {
		return w.err
	}
	if c.Telegram.Enabled && len(c.Users) == 0 {
		if err := w.people(&c, client, false); err != nil {
			return err
		}
	}
	// Save completed account choices before live checks, so a transient outage
	// does not force the user to repeat login or pairing.
	if err := SaveConfig(dir, c); err != nil {
		return err
	}
	p := &Provider{Config: c, Client: client, Auth: &Auth{Dir: dir, Client: client}}
	fmt.Fprintln(out, "Verifico le connessioni con piccole richieste ai provider…")
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		m, checkErr := p.Complete(checkCtx, "setup-"+randomID(), []Message{{Role: "system", Content: "Reply briefly."}, {Role: "user", Content: "Reply with ALINA_OK only."}}, nil, nil)
		cancel()
		if checkErr == nil && strings.TrimSpace(m.Content) == "ALINA_OK" && len(m.Calls) == 0 {
			break
		}
		fmt.Fprintln(out, "Modello non verificato. Controlla connessione e accesso al modello; login: alina setup login.")
		if checkErr != nil {
			fmt.Fprintln(out, truncate(checkErr.Error(), 200))
		}
		if !w.yes("Riprova", true) {
			return errors.New("configurazione salvata, modello non verificato; riprendi con alina setup")
		}
	}
	for c.Search.Default != "none" {
		switch c.Search.Default {
		case "tavily":
			if c.Search.TavilyKey == "" {
				c.Search.TavilyKey = w.secret("Chiave Tavily", "")
			}
		case "brave":
			if c.Search.BraveKey == "" {
				c.Search.BraveKey = w.secret("Chiave Brave Search", "")
			}
		}
		if w.err != nil {
			return w.err
		}
		s := Search{Config: c.Search, Provider: p, Client: client}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, checkErr := s.Run(checkCtx, "setup-"+randomID(), "", "Termux official website")
		cancel()
		if checkErr == nil && strings.TrimSpace(result) != "" {
			break
		}
		fmt.Fprintln(out, "Ricerca non verificata. L'accesso dipende anche dal provider e dall'account.")
		choice := w.choice("1 riprova · 2 Tavily · 3 Brave · 4 configura dopo", "1", "1", "2", "3", "4")
		if w.err != nil {
			return w.err
		}
		switch choice {
		case "2":
			c.Search.Default, c.Search.TavilyKey = "tavily", w.secret("Chiave Tavily", c.Search.TavilyKey)
		case "3":
			c.Search.Default, c.Search.BraveKey = "brave", w.secret("Chiave Brave Search", c.Search.BraveKey)
		case "4":
			c.Search.Default = "none"
		}
	}
	if w.err != nil {
		return w.err
	}
	if err := SaveConfig(dir, c); err != nil {
		return err
	}
	if c.Telegram.Enabled {
		if err := NewTelegram(dir, c.Telegram, nil, client).stateErr; err != nil {
			return fmt.Errorf("configurazione salvata, stato Telegram non leggibile: %w", err)
		}
	}
	// Initialize the same local resources as serve, without running any jobs.
	p.Config = c
	engine, err := NewEngine(dir, c, p, &Search{Config: c.Search, Provider: p, Client: client})
	if err != nil {
		return fmt.Errorf("configurazione salvata, inizializzazione incompleta: %w", err)
	}
	engine.Close()
	fmt.Fprintf(out, "Pronta · %s · ricerca %s\n", c.Model, c.Search.Default)
	if c.Memory.Enabled && c.Memory.Dream {
		if c.Memory.DreamCron == "0 3 * * *" {
			fmt.Fprintf(out, "Memoria attiva · dream alle 03:00 (%s).\n", c.Timezone)
		} else {
			fmt.Fprintf(out, "Memoria e dream attivi (%s, %s).\n", c.Memory.DreamCron, c.Timezone)
		}
	}
	if c.Autonomy.Enabled {
		fmt.Fprintf(out, "Iniziative: massimo %d chiamate/giorno, %d minuti ciascuna.\n", c.Autonomy.MaxCalls, c.Autonomy.Minutes)
	}
	if !c.Telegram.Enabled {
		fmt.Fprintln(out, "Telegram non collegato: alina setup telegram")
	}
	if c.Search.Default == "none" {
		fmt.Fprintln(out, "Ricerca disattivata: alina setup --advanced")
	}
	return nil
}

func (w *wizard) connectChatGPT(dir string, client *http.Client) error {
	a := &Auth{Dir: dir, Client: client}
	if _, err := a.Get(w.ctx); err == nil {
		fmt.Fprintln(w.out, "ChatGPT collegato.")
		return nil
	}
	w.ask("Invio per collegare ChatGPT (Plus/Pro)", "")
	if w.err != nil {
		return w.err
	}
	for {
		if err := a.LoginDevice(w.ctx, w.out); err == nil {
			return nil
		}
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		fmt.Fprintln(w.out, "Login non completato. Se necessario, abilita il login con codice nelle impostazioni di sicurezza di ChatGPT.")
		switch w.choice("1 riprova · 2 usa il browser · 3 esci", "1", "1", "2", "3") {
		case "2":
			return a.LoginBrowser(w.ctx, w.in, w.out)
		case "3":
			return errors.New("login incompleto; riprendi con alina setup")
		}
		if w.err != nil {
			return w.err
		}
	}
}

func (w *wizard) choice(label, def string, values ...string) string {
	for w.err == nil {
		value := w.ask(label, def)
		for _, valid := range values {
			if value == valid {
				return value
			}
		}
		fmt.Fprintln(w.out, "Scegli una delle opzioni indicate.")
	}
	return ""
}

func setupTimezone(ctx context.Context) string {
	candidates := []string{strings.TrimPrefix(os.Getenv("TZ"), ":")}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "/zoneinfo/"); ok {
			candidates = append(candidates, zone)
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(b)))
	}
	if runtime.GOOS == "android" {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		b, _ := exec.CommandContext(ctx, "/system/bin/getprop", "persist.sys.timezone").Output()
		candidates = append(candidates, strings.TrimSpace(string(b)))
	}
	for _, zone := range candidates {
		if zone != "" && zone != "Local" {
			if _, err := time.LoadLocation(zone); err == nil {
				return zone
			}
		}
	}
	if runtime.GOOS != "android" {
		return "Local"
	}
	return ""
}

// Set and restore terminal mode in the caller, including on cancellation.
// Terminal.ReadPassword handles Ctrl-C/Ctrl-D without echoing the secret.
func terminalSecret(ctx context.Context, in io.Reader, out io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, state)
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{in, out}, "")
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() { text, err := terminal.ReadPassword(""); done <- result{text, err} }()
	select {
	case r := <-done:
		return r.text, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
