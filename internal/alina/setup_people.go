package alina

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
)

func (w *wizard) personDetails(c *Config, u *User) error {
	for w.err == nil {
		u.Name = w.ask("Nome", u.Name)
		if w.err != nil {
			return w.err
		}
		if len(c.Users) == 0 {
			u.Family = "home"
		} else if !slices.ContainsFunc(c.Users, func(p User) bool { return p.ID == u.ID }) {
			u.Family = c.localUser().Family
			if u.Family == "" {
				return errors.New("legacy personal archive: configure a shared family with alina config apply before adding people")
			}
		}
		check := Config{Users: []User{*u}}
		if err := check.validatePeople(); err == nil {
			return w.err
		}
		fmt.Fprintln(w.out, "Nome richiesto, massimo 80 caratteri.")
	}
	return w.err
}

func (w *wizard) people(c *Config, client *http.Client, edit bool) error {
	fmt.Fprintln(w.out, "Una famiglia, più persone riconoscibili. Memoria e dream sono condivisi.\nGli utenti autorizzati sono fidati e possono usare il dispositivo.")
	// A legacy personal owner becomes the first member of their household.
	// NewEngine retains that owner's archive in place when the config is saved.
	if len(c.Users) == 1 && c.Users[0].Family == "" {
		c.Users[0].Family = c.Users[0].ID
	}
	first := len(c.Users) == 0
	if first {
		u := User{ID: "owner", TelegramID: c.Telegram.OwnerID}
		if err := w.personDetails(c, &u); err != nil {
			return err
		}
		c.Users = append(c.Users, u)
		c.LocalUser = u.ID
	}
	for w.err == nil {
		if first || !edit {
			if !w.yes("Aggiungere un'altra persona Telegram", false) {
				return w.err
			}
		} else {
			for i, u := range c.Users {
				fmt.Fprintf(w.out, "%d · %s\n", i+1, u.Name)
			}
			action := w.choice("1 aggiungi · 2 modifica · 3 rimuovi · 4 fine", "4", "1", "2", "3", "4")
			switch action {
			case "4":
				return w.err
			case "2", "3":
				// Names may repeat; the numbered selection resolves one exact user.
				n, err := strconv.Atoi(w.ask("Numero persona", ""))
				index := n - 1
				if err != nil || index < 0 || index >= len(c.Users) {
					fmt.Fprintln(w.out, "Persona non trovata.")
					continue
				}
				if action == "3" {
					if c.localUser().ID == c.Users[index].ID {
						fmt.Fprintln(w.out, "Cambia prima local_user con alina config apply.")
						continue
					}
					c.Users = slices.Delete(c.Users, index, index+1)
				} else {
					if err := w.personDetails(c, &c.Users[index]); err != nil {
						return err
					}
				}
				continue
			}
		}
		if len(c.Users) >= 32 {
			return errors.New("maximum 32 configured users")
		}
		tg := NewTelegram(w.dir, c.Telegram, nil, client)
		if tg.stateErr != nil {
			return tg.stateErr
		}
		var me struct {
			Username string `json:"username"`
		}
		if err := tg.api(w.ctx, "getMe", nil, &me); err != nil {
			return err
		}
		code := "alina-" + randomID()
		fmt.Fprintf(w.out, "Fai aprire alla persona https://t.me/%s?start=%s e premere Avvia (entro 2 minuti).\n", me.Username, code)
		ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
		id, err := pairTelegram(ctx, tg, code, c.Users)
		cancel()
		if err != nil {
			return err
		}
		if _, exists := c.telegramUser(id); exists {
			fmt.Fprintln(w.out, "Questa persona è già autorizzata.")
			continue
		}
		u := User{ID: "user-" + randomID(), TelegramID: id}
		if err := w.personDetails(c, &u); err != nil {
			return err
		}
		c.Users = append(c.Users, u)
	}
	return w.err
}
