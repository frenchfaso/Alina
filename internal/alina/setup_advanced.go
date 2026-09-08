package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

func SetupAdvanced(ctx context.Context, dir string, in *bufio.Reader, out io.Writer) error {
	if e := requireStopped(dir); e != nil {
		return e
	}
	c, e := LoadConfig(dir)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if os.IsNotExist(e) {
		c.NetworkPolicy = "declared"
	}
	w := &wizard{ctx: ctx, in: in, out: out}
	fmt.Fprintln(out, "Alina · configurazione\nI segreti restano nel file locale config.json (permessi 0600).\nNessuna modifica ai servizi o ai pacchetti del sistema.")
	choice := "1"
	if c.Provider == "opencode-go" {
		choice = "2"
	}
	choice = w.choice("Provider principale: 1 ChatGPT Plus/Pro, 2 OpenCode Go", choice, "1", "2")
	if choice != "1" && choice != "2" {
		return errors.New("choose provider 1 or 2")
	}
	if choice == "1" {
		if c.Provider != "chatgpt" {
			c.Model = defaultModel
			c.ContextTokens = astraContextTokens
		}
		c.Provider = "chatgpt"
	} else {
		if c.Provider != "opencode-go" {
			c.Model = "glm-5.1"
			c.ContextTokens = 32768
		}
		c.Provider = "opencode-go"
	}
	c.Model = w.ask("Model ID (deve essere disponibile nel tuo account)", c.Model)
	if c.Model == defaultModel {
		fmt.Fprintf(out, "Astra: contesto %d token; compattazione oltre il 95%%.\n", c.ContextTokens)
		c.ReasoningEffort = w.choice("Reasoning: low / medium / high / xhigh / max", c.ReasoningEffort, "low", "medium", "high", "xhigh", "max")
		c.Verbosity = w.choice("Verbosity: low / medium / high", c.Verbosity, "low", "medium", "high")
		tokens, err := strconv.Atoi(w.ask("Context window (token)", strconv.Itoa(c.ContextTokens)))
		if err != nil {
			return err
		}
		c.ContextTokens = tokens
	}
	c.WorkDir = w.ask("Directory di lavoro", c.WorkDir)
	if c.Provider == "opencode-go" || w.yes("Configurare anche OpenCode Go", c.OpenCodeKey != "") {
		c.OpenCodeKey = w.secret("Chiave OpenCode Go", c.OpenCodeKey)
		c.OpenCodeAPI = w.choice("Protocollo del modello OpenCode: chat / responses / messages", c.OpenCodeAPI, "chat", "responses", "messages")
	}
	login := w.yes("Eseguire ora il login ChatGPT dedicato ad Alina", false)
	fmt.Fprintln(out, "\nWeb search: puoi configurare tutti e tre i provider.")
	c.Search.Default = w.choice("Ricerca predefinita: openai / tavily / brave / none", c.Search.Default, "openai", "tavily", "brave", "none")
	if w.yes("Configurare Tavily", c.Search.TavilyKey != "") {
		c.Search.TavilyKey = w.secret("Chiave Tavily", c.Search.TavilyKey)
	}
	if w.yes("Configurare Brave Search", c.Search.BraveKey != "") {
		c.Search.BraveKey = w.secret("Chiave Brave Search", c.Search.BraveKey)
	}
	fmt.Fprintln(out, "OpenAI search è predefinito e riusa il login ChatGPT di Alina: non serve una seconda chiave. Una API key separata è facoltativa e usa fatturazione API distinta dall'abbonamento.")
	if w.yes("Configurare una API key OpenAI separata per search", c.Search.OpenAIKey != "") {
		c.Search.OpenAIKey = w.secret("OpenAI API key", c.Search.OpenAIKey)
	}
	c.Search.OpenAIModel = w.ask("Modello OpenAI per search (vuoto = modello ChatGPT; - ripristina)", c.Search.OpenAIModel)
	if c.Search.OpenAIModel == "-" {
		c.Search.OpenAIModel = ""
	}
	if e = w.telegram(&c.Telegram); e != nil {
		return e
	}
	c.Timezone = w.ask("Fuso orario IANA (es. Europe/Rome) oppure Local", c.Timezone)
	c.Location = w.ask("Località opzionale per il contesto (- cancella)", c.Location)
	if c.Location == "-" {
		c.Location = ""
	}
	c.Memory.Enabled = w.yes("Abilitare memoria condivisa: archivio SQLite e note essenziali", c.Memory.Enabled)
	if c.Memory.Enabled {
		c.Memory.Dream = w.yes("Abilitare dream notturno e riflessione sul soul", c.Memory.Dream)
		if c.Memory.Dream {
			c.Memory.DreamCron = w.ask("Orario dream (cron a 5 campi)", c.Memory.DreamCron)
			c.Memory.CatchUp = w.yes("Recuperare una sola esecuzione al risveglio se saltata", c.Memory.CatchUp)
		}
		fmt.Fprintln(out, "Gli embedding sono facoltativi. Inviano note e query al servizio scelto; API OpenAI con costo separato da ChatGPT. Senza embedding resta la ricerca testuale.")
		if w.yes("Configurare un endpoint embedding OpenAI-compatible", c.Memory.EmbeddingURL != "") {
			endpoint := c.Memory.EmbeddingURL
			if endpoint == "" {
				endpoint = "https://api.openai.com/v1/embeddings"
			}
			model := c.Memory.EmbeddingModel
			if model == "" {
				model = "text-embedding-3-small"
			}
			c.Memory.EmbeddingURL = w.ask("URL completo endpoint (- disabilita)", endpoint)
			if c.Memory.EmbeddingURL == "-" {
				c.Memory.EmbeddingURL, c.Memory.EmbeddingModel, c.Memory.EmbeddingKey = "", "", ""
			} else {
				c.Memory.EmbeddingModel = w.ask("Modello embedding", model)
				c.Memory.EmbeddingKey = w.secret("Chiave embedding (facoltativa per server locale)", c.Memory.EmbeddingKey)
			}
		}
	}

	fmt.Fprintln(out, "\nRete: declared chiede consenso per download/installazioni dichiarati o riconosciuti; si affida alla collaborazione dell'agente. strict isola la rete e chiede consenso per ogni uso dalla shell.")
	c.NetworkPolicy = w.choice("Politica rete: declared / strict", c.NetworkPolicy, "declared", "strict")
	c.Autonomy.Enabled = w.yes("Consentire iniziative personali locali entro un budget", c.Autonomy.Enabled)
	if c.Autonomy.Enabled {
		c.Autonomy.Scope = w.ask("Ambito delle iniziative personali", c.Autonomy.Scope)
		calls, err := strconv.Atoi(w.ask("Massimo chiamate al modello al giorno per iniziative", strconv.Itoa(c.Autonomy.MaxCalls)))
		if err != nil {
			return err
		}
		c.Autonomy.MaxCalls = calls
		minutes, err := strconv.Atoi(w.ask("Minuti massimi per iniziativa", strconv.Itoa(c.Autonomy.Minutes)))
		if err != nil {
			return err
		}
		c.Autonomy.Minutes = minutes
		c.Autonomy.Search = w.yes("Consentire websearch nelle iniziative personali", c.Autonomy.Search)
	}

	if w.err != nil {
		return fmt.Errorf("setup cancelled: %w", w.err)
	}
	if e = c.Validate(); e != nil {
		return e
	}
	fmt.Fprintf(out, "\nProvider: %s / %s\nDirectory: %s\nSearch: %s\nTelegram: %t\nConsensi: una volta / fino al riavvio / fino a revoca\n", c.Provider, c.Model, c.WorkDir, c.Search.Default, c.Telegram.Enabled)
	if !w.yes("Salvare", true) {
		return errors.New("setup cancelled")
	}
	if w.err != nil {
		return w.err
	}
	if e = SaveConfig(dir, c); e != nil {
		return e
	}
	fmt.Fprintln(out, "Salvato:", filepath.Join(dir, "config.json"))
	if login {
		a := Auth{Dir: dir, Client: newHTTPClient()}
		method := w.ask("Login: device / browser", "device")
		if w.err != nil {
			return w.err
		}
		if method == "browser" {
			e = a.LoginBrowser(ctx, in, out)
		} else {
			e = a.LoginDevice(ctx, out)
		}
		if e != nil {
			return fmt.Errorf("config saved, login incomplete: %w", e)
		}
	}
	fmt.Fprintln(out, "Salvato. Esegui alina setup per verificare le connessioni e avviare.")
	return nil
}
