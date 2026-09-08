package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// One input reader and one event loop keep the terminal usable while a job
// runs, without a TUI library or competing readers for approval responses.
func chat(ctx context.Context, dir, session string, in *bufio.Reader, out io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	client := LocalClient(dir)
	defer client.CloseIdleConnections()
	type lineResult struct {
		text string
		err  error
	}
	lines := make(chan lineResult)
	go func() {
		for {
			text, err := in.ReadString('\n')
			select {
			case lines <- lineResult{text, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	watch := map[string]string{}
	defer func() {
		if ctx.Err() == nil {
			return
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for id := range watch {
			_ = requestLocalJSON(cancelCtx, client, "POST", "/v1/jobs/"+id+"/cancel", nil, &map[string]any{})
		}
	}()
	approvals := map[string]*Approval{}
	fmt.Fprintf(out, "Alina · conversazione %s\nScrivi anche durante un lavoro: il messaggio lo aggiorna al prossimo punto sicuro.\n/new /status /permissions /cancel ID /approve 1|2|3|4 /quit\nCtrl-C interrompe i lavori seguiti; /quit lascia il servizio al lavoro.\n\ntu> ", session)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-lines:
			if r.err != nil && !errors.Is(r.err, io.EOF) {
				return r.err
			}
			if r.err != nil {
				lines = nil
			}
			line := strings.TrimSpace(r.text)
			if line == "" {
				if lines == nil && len(approvals) > 0 {
					return errors.New("input closed; approval remains pending")
				}
				if lines == nil && len(watch) == 0 {
					return nil
				}
				continue
			}
			fields := strings.Fields(line)
			var err error
			switch fields[0] {
			case "/quit":
				return nil
			case "/new":
				session = "local-" + randomID()
				fmt.Fprintln(out, "Conversazione (memoria condivisa):", session)
			case "/status":
				var status struct{ Jobs []Job }
				err = requestLocalJSON(ctx, client, "GET", "/v1/status", nil, &status)
				if err == nil {
					fmt.Fprint(out, formatJobs(status.Jobs))
				}
			case "/permissions":
				var grants []Grant
				err = requestLocalJSON(ctx, client, "GET", "/v1/grants", nil, &grants)
				if err == nil {
					fmt.Fprintln(out, formatGrants(grants))
				}
			case "/revoke", "/cancel":
				if len(fields) != 2 || !safeID(fields[1]) {
					err = errors.New("usage: /revoke ID or /cancel ID")
					break
				}
				method, path := "DELETE", "/v1/grants/"+fields[1]
				if fields[0] == "/cancel" {
					method, path = "POST", "/v1/jobs/"+fields[1]+"/cancel"
				}
				err = requestLocalJSON(ctx, client, method, path, nil, &map[string]any{})
			case "/approve":
				if len(fields) != 2 || len(approvals) != 1 {
					err = errors.New("use /approve 1|2|3|4 with one displayed approval; otherwise use the approval API command printed with the job")
					break
				}
				scope := map[string]string{"1": "once", "2": "restart", "3": "always", "4": "deny"}[fields[1]]
				if scope == "" {
					err = errors.New("approval choice must be 1, 2, 3 or 4")
					break
				}
				for id, approval := range approvals {
					err = requestLocalJSON(ctx, client, "POST", "/v1/jobs/"+id+"/approve", map[string]string{"approval_id": approval.ID, "scope": scope}, &map[string]any{})
					if err == nil {
						delete(approvals, id)
					}
				}
			default:
				var j Job
				err = requestLocalJSON(ctx, client, "POST", "/v1/jobs", map[string]any{"session": session, "message": line, "request_id": randomID(), "interactive": true}, &j)
				if err == nil {
					if _, exists := watch[j.ID]; exists {
						fmt.Fprintln(out, "Messaggio aggiunto ·", j.ID)
					} else {
						fmt.Fprintln(out, "Avviato ·", j.ID)
						watch[j.ID] = ""
					}
					delete(approvals, j.ID)
				}
			}
			if err != nil {
				fmt.Fprintln(out, "Errore:", err)
			}
			fmt.Fprint(out, "\ntu> ")
		case <-ticker.C:
			ids := make([]string, 0, len(watch))
			for id := range watch {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				var j Job
				if err := requestLocalJSON(ctx, client, "GET", "/v1/jobs/"+id, nil, &j); err != nil {
					return err
				}
				stamp := j.Status + ":" + j.Activity
				if j.Approval != nil {
					stamp += ":" + j.Approval.ID
				}
				if watch[id] == stamp {
					continue
				}
				watch[id] = stamp
				delete(approvals, id)
				if j.Approval != nil {
					a := j.Approval
					approvals[id] = a
					fmt.Fprintf(out, "\nConsenso · %s · %s\nDirectory: %s\n%s\n1) Solo una volta\n2) Fino al riavvio di Alina\n3) Fino a revoca\n4) Nega\n/approve 1|2|3|4 oppure scrivi una correzione.\n%s\n", id, a.Action.Reason, a.Action.Directory, a.Action.Command, approvalCommand(id, a.ID))
					if lines == nil {
						return errors.New("input closed; approval remains pending")
					}
				} else if terminalStatus(j.Status) {
					fmt.Fprintf(out, "\nalina · %s> %s\n", id, j.Output)
					if j.Error != "" {
						fmt.Fprintln(out, "Errore:", j.Error)
					}
					if j.PendingSteering > 0 {
						fmt.Fprintln(out, "Messaggi salvati in attesa:", resumeCommand(id))
					}
					delete(watch, id)
				} else if j.Activity != "" {
					fmt.Fprintf(out, "\n· %s · %s\n", id, j.Activity)
				}
				fmt.Fprint(out, "tu> ")
			}
			if lines == nil && len(watch) == 0 {
				return nil
			}
		}
	}
}
