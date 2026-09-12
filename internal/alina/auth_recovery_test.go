package alina

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatGPTRecoversRejectedUnexpiredToken(t *testing.T) {
	for _, kind := range []string{"chat", "search", "catalog", "rejected-again", "forbidden", "account-changed"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			old := Credential{Access: "old", Refresh: "refresh", AccountID: "account", Expires: time.Now().Add(7 * 24 * time.Hour).Unix()}
			if err := writeJSON(filepath.Join(dir, "chatgpt.json"), old); err != nil {
				t.Fatal(err)
			}
			renewals, requests := 0, 0
			id := "account"
			if kind == "account-changed" {
				id = "another"
			}
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, id))) + ".signature"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					renewals++
					r.ParseForm()
					if r.Form.Get("refresh_token") != "refresh" {
						t.Error("wrong refresh token")
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3600})
					return
				}
				requests++
				if kind == "forbidden" {
					w.WriteHeader(403)
					return
				}
				if r.Header.Get("Authorization") == "Bearer old" || kind == "rejected-again" {
					w.WriteHeader(401)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("renewed token not used")
				}
				if kind == "catalog" {
					fmt.Fprint(w, `{"models":[{"slug":"gpt-6-astra","display_name":"Astra","supported_reasoning_levels":[{"effort":"low"}],"default_reasoning_level":"low","context_window":272000}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			defer srv.Close()
			client := srv.Client()
			transport := client.Transport
			client.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme = "http"
				r.URL.Host = strings.TrimPrefix(srv.URL, "http://")
				return transport.RoundTrip(r)
			})
			p := &Provider{Config: DefaultConfig(), Auth: &Auth{Dir: dir, Client: client}, Client: client, BaseURL: srv.URL + "/responses"}
			ctx := context.Background()
			var err error
			switch kind {
			case "search":
				s := Search{Config: p.Config.Search, Provider: p, Client: client}
				_, err = s.Run(ctx, "test", "", "test")
			case "catalog":
				_, err = p.fetchModelCatalog(ctx)
			default:
				_, err = p.Complete(ctx, "test", []Message{{Role: "user", Content: "test"}}, nil, nil)
			}
			if kind == "forbidden" {
				if err == nil || requests != 1 || renewals != 0 {
					t.Fatal(err, requests, renewals)
				}
				return
			}
			if kind == "account-changed" {
				if err == nil || requests != 1 || renewals != 1 {
					t.Fatal(err, requests, renewals)
				}
				return
			}
			if kind == "rejected-again" {
				if err == nil || !strings.Contains(err.Error(), "alina setup login") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || renewals != 1 {
				t.Fatal("retry was not bounded", requests, renewals)
			}
			// A concurrent request that used the old token must reuse the renewal.
			saved, err := p.Auth.get(ctx, old.Access)
			if err != nil || saved.Access != token || saved.Refresh != "refresh" || renewals != 1 {
				t.Fatal("renewal not persisted/reused", err, renewals)
			}
		})
	}
}
