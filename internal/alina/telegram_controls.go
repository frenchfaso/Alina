package alina

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var telegramMenu = []map[string]string{
	{"command": "think", "description": "Livello di ragionamento"},
	{"command": "model", "description": "Scegli il modello"},
	{"command": "status", "description": "Modello, contesto e attività"},
	{"command": "stop", "description": "Interrompi il lavoro corrente"},
	{"command": "resume", "description": "Riprendi un lavoro interrotto"},
	{"command": "help", "description": "Comandi essenziali"},
}

const telegramHelp = "Scrivi una richiesta; nuovi messaggi aggiornano il lavoro in corso.\n/think · ragionamento\n/model · modello\n/status · stato\n/stop · interrompi\n/resume · riprendi\n\n/new cambia contesto, senza cancellare memoria.\n/permissions e /revoke ID gestiscono i consensi.\n/think default e /model default ripristinano le impostazioni generali."

func (t *Telegram) selectorToken(e *Engine, owner, kind string, c modelCatalog) string {
	key := owner + "\n" + c.Key + "\n" + jsonText(c.Models)
	if kind == "think" {
		m, _ := e.selectedModel(context.Background(), owner, c)
		key += "\n" + m.ID
	}
	return contentID(key)[:12]
}
func (t *Telegram) modelControl(ctx context.Context, id int64, e *Engine, owner, kind, value string, page int) error {
	c, err := e.models(ctx, true)
	if err != nil {
		return t.sendTo(ctx, id, "Catalogo del provider non disponibile. Nessuna impostazione modificata; riprova più tardi. Per OpenCode Go usa alina setup --advanced.", nil)
	}
	if value != "" {
		if err = e.setModelPreference(ctx, owner, kind, value, c); err != nil {
			return t.sendTo(ctx, id, "Scelta non disponibile per il modello corrente. Usa /"+kind+" per le opzioni aggiornate.", nil)
		}
	}
	m, effort := e.selectedModel(ctx, owner, c)
	text := fmt.Sprintf("Modello: %s\nReasoning: %s", m.ID, effort)
	if effort == "" {
		text = fmt.Sprintf("Modello: %s\nReasoning non configurabile.", m.ID)
	}
	if value != "" {
		text += "\nPreferenza personale salvata; vale dalla prossima richiesta al modello."
	}
	if c.Stale {
		text += "\nCatalogo in cache: " + c.Fetched.Format("2006-01-02")
	}
	rows := [][]map[string]string{}
	token := t.selectorToken(e, owner, kind, c)
	button := func(label, choice string) map[string]string {
		return map[string]string{"text": label, "callback_data": "p:" + token + ":" + kind + ":" + choice}
	}
	if kind == "think" {
		for _, level := range m.Levels {
			rows = append(rows, []map[string]string{button(level, level)})
		}
	} else {
		const size = 6
		if page < 0 || page > (len(c.Models)-1)/size {
			page = 0
		}
		for i := page * size; i < min((page+1)*size, len(c.Models)); i++ {
			rows = append(rows, []map[string]string{button(c.Models[i].ID, strconv.Itoa(i))})
		}
		navigation := []map[string]string{}
		if page > 0 {
			navigation = append(navigation, button("‹", "page"+strconv.Itoa(page-1)))
		}
		if (page+1)*size < len(c.Models) {
			navigation = append(navigation, button("›", "page"+strconv.Itoa(page+1)))
		}
		if len(navigation) > 0 {
			rows = append(rows, navigation)
		}
	}
	rows = append(rows, []map[string]string{button("Default", "default")})
	return t.sendTo(ctx, id, text, map[string]any{"inline_keyboard": rows})
}
func (t *Telegram) controlCallback(ctx context.Context, id int64, e *Engine, owner string, u tgUpdate) error {
	parts := strings.Split(u.Callback.Data, ":")
	// Acknowledge the button immediately; a catalog refresh may take seconds.
	if err := t.api(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, nil); err != nil {
		var apiErr *telegramAPIError
		if !errors.As(err, &apiErr) || apiErr.code != 400 {
			return err
		}
	}
	c, err := e.models(ctx, true)
	if err != nil || len(parts) != 4 || (parts[2] != "think" && parts[2] != "model") || parts[1] != t.selectorToken(e, owner, parts[2], c) {
		return t.sendTo(ctx, id, "Menu scaduto. Riapri /model o /think.", nil)
	}
	value := parts[3]
	page := 0
	if parts[2] == "model" && value != "default" {
		if suffix, ok := strings.CutPrefix(value, "page"); ok {
			page, err = strconv.Atoi(suffix)
			value = ""
			if err != nil || page < 0 || page > (len(c.Models)-1)/6 {
				return t.sendTo(ctx, id, "Pagina non disponibile. Riapri /model.", nil)
			}
		} else {
			index, err := strconv.Atoi(value)
			if err != nil || index < 0 || index >= len(c.Models) {
				return t.sendTo(ctx, id, "Modello non disponibile. Riapri /model.", nil)
			}
			value = c.Models[index].ID
		}
	}
	return t.modelControl(ctx, id, e, owner, parts[2], value, page)
}
func (t *Telegram) briefStatus(ctx context.Context, id int64, e *Engine, owner string) error {
	model, effort := e.Config.Model, e.Config.ReasoningEffort
	if c, err := e.models(ctx, false); err == nil {
		m, r := e.selectedModel(ctx, owner, c)
		model, effort = m.ID, r
	}
	text := fmt.Sprintf("%s · reasoning %s", model, effort)
	jobs := e.Jobs(owner)
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Created.After(jobs[j].Created) })
	active := []Job{}
	for _, j := range jobs {
		if !terminalStatus(j.Status) {
			active = append(active, j)
		}
	}
	if len(active) == 0 {
		text += "\nNessun lavoro in corso."
	} else {
		text += "\n" + strings.TrimSpace(formatJobs(active))
	}
	t.mu.Lock()
	session := t.state.Sessions[strconv.FormatInt(id, 10)]
	t.mu.Unlock()
	if safeID(session) {
		var history []Message
		raw, err := os.ReadFile(filepath.Join(e.Dir, "sessions", session+".json"))
		if err == nil && json.Unmarshal(raw, &history) == nil {
			budget := e.Config.ContextTokens
			if c, err := e.models(ctx, false); err == nil {
				if m, ok := c.model(model); ok && model != e.Config.Model {
					budget = min(budget, m.Context)
				}
			}
			used := e.historyTokens(history, model) + estimatedTokens([]Message{{Role: "system", Content: e.prompt(nil)}}) + estimatedTokens(e.toolsFor(&runningJob{}))
			text += fmt.Sprintf("\nContesto ≈ %d / %d token (%.0f%%)", used, budget, 100*float64(used)/float64(budget))
		}
	}
	return t.sendTo(ctx, id, text, nil)
}
func (t *Telegram) jobControl(ctx context.Context, id int64, e *Engine, owner, action, key string) error {
	// Persist the target before the side effect: retries cannot stop/resume newer work.
	stateKey := "telegram-control:" + key
	var target string
	err := e.Memory.DB.QueryRowContext(ctx, "SELECT value FROM memory_state WHERE key=?", stateKey).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		jobs := e.Jobs(owner)
		candidates := []Job{}
		for _, j := range jobs {
			if (j.Kind == "chat" || j.Kind == "") && ((action == "stop" && !terminalStatus(j.Status)) || (action == "resume" && (j.Status == "failed" || j.Status == "interrupted" || j.Status == "cancelled"))) {
				candidates = append(candidates, j)
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Created.After(candidates[j].Created) })
		if action == "stop" && len(candidates) > 1 {
			return t.sendTo(ctx, id, "Più lavori attivi. Usa /cancel ID:\n"+formatJobs(candidates), nil)
		}
		target = "none"
		if len(candidates) > 0 {
			target = candidates[0].ID
		}
		if _, err = e.Memory.DB.ExecContext(ctx, "INSERT INTO memory_state VALUES(?,?)", stateKey, target); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if target == "none" {
		return t.sendTo(ctx, id, "Nessun lavoro da "+map[string]string{"stop": "interrompere", "resume": "riprendere"}[action]+".", nil)
	}
	old, ok := e.Get(target)
	if !ok || old.Owner != owner {
		return t.sendTo(ctx, id, "Lavoro non disponibile.", nil)
	}
	if action == "stop" {
		if !terminalStatus(old.Status) {
			if err = e.Cancel(target, owner); err != nil {
				return err
			}
		}
		return t.sendTo(ctx, id, "Interruzione richiesta.", nil)
	}
	j, err := e.resumeKey(target, owner, key)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.state.Sessions[strconv.FormatInt(id, 10)] = j.Session
	err = t.saveLocked()
	t.mu.Unlock()
	if err != nil {
		return err
	}
	return t.sendTo(ctx, id, "Ripresa avviata.", nil)
}
