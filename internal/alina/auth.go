package alina

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Public OAuth client and wire protocol also used by Pi. See docs/sources.md.
const oauthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const authBase = "https://auth.openai.com"
const redirectURI = "http://localhost:1455/auth/callback"

type Credential struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"account_id"`
}
type Auth struct {
	mu     sync.Mutex
	Dir    string
	Client *http.Client
}

func accountID(token string) string {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return ""
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return ""
	}
	var j struct {
		Auth struct {
			ID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	_ = json.Unmarshal(b, &j)
	return j.Auth.ID
}
func (a *Auth) token(ctx context.Context, form url.Values) (Credential, error) {
	req, e := http.NewRequestWithContext(ctx, "POST", authBase+"/oauth/token", strings.NewReader(form.Encode()))
	if e != nil {
		return Credential{}, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, e := a.Client.Do(req)
	if e != nil {
		return Credential{}, &networkFailure{cause: e}
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return Credential{}, fmt.Errorf("OAuth token exchange: %w", &remoteHTTPError{Status: r.StatusCode})
	}
	var j struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
	}
	if e = decodeLimited(r.Body, &j); e != nil {
		return Credential{}, e
	}
	if j.Access == "" || j.Expires <= 0 {
		return Credential{}, errors.New("incomplete OAuth response")
	}
	c := Credential{Access: j.Access, Refresh: j.Refresh, Expires: time.Now().Unix() + j.Expires, AccountID: accountID(j.Access)}
	if c.AccountID == "" {
		return c, errors.New("ChatGPT account ID missing from token")
	}
	return c, nil
}
func (a *Auth) Get(ctx context.Context) (Credential, error) {
	return a.get(ctx, "")
}

// Serialize refreshes and reuse a token already renewed by another request.
func (a *Auth) get(ctx context.Context, rejected string) (Credential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var c Credential
	b, e := os.ReadFile(filepath.Join(a.Dir, "chatgpt.json"))
	if e != nil {
		return c, errors.New("ChatGPT login required: alina setup login")
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Expires > time.Now().Unix()+60 && c.Access != "" && c.AccountID != "" && c.Access != rejected {
		return c, nil
	}
	if c.Refresh == "" {
		return c, errors.New("ChatGPT login expired: alina setup login")
	}
	n, e := a.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}, "client_id": {oauthClientID}})
	if e != nil {
		return c, e
	}
	if n.Refresh == "" {
		n.Refresh = c.Refresh
	}
	return n, writeJSON(filepath.Join(a.Dir, "chatgpt.json"), n)
}
func (a *Auth) LoginDevice(ctx context.Context, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var d struct {
		ID       string          `json:"device_auth_id"`
		Code     string          `json:"user_code"`
		Interval json.RawMessage `json:"interval"`
	}
	if e := requestJSON(ctx, a.Client, "POST", authBase+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": oauthClientID}, nil, &d); e != nil {
		return e
	}
	if d.ID == "" || d.Code == "" {
		return errors.New("incomplete device login response")
	}
	interval := 5
	_ = json.Unmarshal(d.Interval, &interval)
	if interval < 2 {
		interval = 2
	}
	fmt.Fprintf(w, "Apri %s/codex/device e inserisci il codice: %s\nIn attesa del login (Ctrl-C per annullare)...\n", authBase, d.Code)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		b, _ := json.Marshal(map[string]string{"device_auth_id": d.ID, "user_code": d.Code})
		req, _ := http.NewRequestWithContext(ctx, "POST", authBase+"/api/accounts/deviceauth/token", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		r, e := a.Client.Do(req)
		if e != nil {
			return errors.New("device login request failed")
		}
		var j struct {
			Code     string          `json:"authorization_code"`
			Verifier string          `json:"code_verifier"`
			Error    json.RawMessage `json:"error"`
		}
		e = decodeLimited(r.Body, &j)
		r.Body.Close()
		if r.StatusCode == 403 || r.StatusCode == 404 {
			continue
		}
		if r.StatusCode == 429 || strings.Contains(string(j.Error), "slow_down") {
			interval += 5
			continue
		}
		if r.StatusCode != 200 {
			return fmt.Errorf("device login: HTTP %d; try alina setup login browser", r.StatusCode)
		}
		if e != nil {
			return e
		}
		if j.Code == "" || j.Verifier == "" {
			return errors.New("incomplete device authorization")
		}
		c, e := a.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {oauthClientID}, "code": {j.Code}, "code_verifier": {j.Verifier}, "redirect_uri": {authBase + "/deviceauth/callback"}})
		if e != nil {
			return e
		}
		return writeJSON(filepath.Join(a.Dir, "chatgpt.json"), c)
	}
}
func (a *Auth) LoginBrowser(ctx context.Context, in *bufio.Reader, w io.Writer) error {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return e
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	state := randomID()
	q := url.Values{"response_type": {"code"}, "client_id": {oauthClientID}, "redirect_uri": {redirectURI}, "scope": {"openid profile email offline_access"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {state}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "originator": {"alina"}}
	fmt.Fprintln(w, "Apri nel browser:\n"+authBase+"/oauth/authorize?"+q.Encode())
	codes := make(chan string, 1)
	ln, e := net.Listen("tcp", "127.0.0.1:1455")
	if e == nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("state") != state || r.URL.Query().Get("code") == "" {
				http.Error(w, "Invalid callback", 400)
				return
			}
			select {
			case codes <- r.URL.Query().Get("code"):
			default:
			}
			fmt.Fprintln(w, "Alina: login ricevuto. Puoi chiudere questa pagina.")
		})
		s := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		defer s.Close()
		go s.Serve(ln)
	}
	fmt.Fprintln(w, "Dopo il login premi Invio, oppure incolla l'intero URL di callback se usi un browser su un altro dispositivo:")
	line, e := readLine(ctx, in)
	if e != nil {
		return e
	}
	var code string
	if strings.TrimSpace(line) != "" {
		u, e := url.Parse(strings.TrimSpace(line))
		if e != nil || u.Query().Get("state") != state {
			return errors.New("OAuth state mismatch")
		}
		code = u.Query().Get("code")
	} else {
		select {
		case code = <-codes:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Minute):
			return errors.New("OAuth callback timeout")
		}
	}
	if code == "" {
		return errors.New("missing OAuth code")
	}
	c, e := a.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {oauthClientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI}})
	if e != nil {
		return e
	}
	return writeJSON(filepath.Join(a.Dir, "chatgpt.json"), c)
}

// Retry only an HTTP authentication rejection, never an interrupted stream or
// an ambiguous network failure. A rejected request has produced no tool work.
func authenticatedRequest[T any](ctx context.Context, a *Auth, call func(Credential) (T, error)) (T, error) {
	var zero T
	c, err := a.Get(ctx)
	if err != nil {
		return zero, err
	}
	out, err := call(c)
	var remote *remoteHTTPError
	if !errors.As(err, &remote) || remote.Status != http.StatusUnauthorized {
		return out, err
	}
	next, err := a.get(ctx, c.Access)
	if err != nil {
		return zero, fmt.Errorf("ChatGPT credential renewal failed; run alina setup login if access remains unavailable: %w", err)
	}
	if next.AccountID != c.AccountID {
		return zero, errors.New("ChatGPT account changed; retry the request with the current account")
	}
	out, err = call(next)
	if errors.As(err, &remote) && remote.Status == http.StatusUnauthorized {
		return zero, fmt.Errorf("ChatGPT login rejected after renewal: alina setup login: %w", err)
	}
	return out, err
}
