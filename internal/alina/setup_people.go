package alina

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (w *wizard) personDetails(c *Config, u *User) error {
	for w.err == nil {
		u.Name = w.ask("Nome", u.Name)
		if w.err != nil {
			return w.err
		}
		families := []string{}
		for _, p := range c.Users {
			if p.Family != "" && !slices.Contains(families, p.Family) {
				families = append(families, p.Family)
			}
		}
		if len(families) > 0 {
			fmt.Fprintln(w.out, "Famiglie esistenti:", strings.Join(families, ", "))
		}
		u.Family = w.ask("Famiglia: ID esistente o nuovo; vuoto = personale, - rimuove", u.Family)
		if u.Family == "-" {
			u.Family = ""
		}
		check := Config{Users: []User{*u}}
		if err := check.validatePeople(); err == nil {
			return w.err
		}
		fmt.Fprintln(w.out, "Nome richiesto; ID famiglia: massimo 40 lettere ASCII, numeri, _ o -.")
	}
	return w.err
}

func (w *wizard) people(c *Config, client *http.Client, edit bool) error {
	fmt.Fprintln(w.out, "Persone · stessa famiglia = memoria condivisa; famiglia vuota = memoria personale.\nGli utenti autorizzati sono fidati e possono usare il dispositivo. Soul e introspezione restano unici.")
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
				family := u.Family
				if family == "" {
					family = "personale"
				}
				fmt.Fprintf(w.out, "%d · %s · %s\n", i+1, u.Name, family)
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
					fmt.Fprintln(w.out, "Cambiando famiglia, i ricordi precedenti restano nel loro spazio; non vengono trasferiti.")
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
