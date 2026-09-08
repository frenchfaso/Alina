package alina

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Action struct {
	Tool      string `json:"tool"`
	Command   string `json:"command"`
	Directory string `json:"directory"`
	Network   bool   `json:"network"`
	Reason    string `json:"reason"`
}

func (a Action) Key() string {
	a.Reason = ""
	b, _ := json.Marshal(a)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type Grant struct {
	ID      string    `json:"id"`
	Action  Action    `json:"action"`
	Scope   string    `json:"scope"`
	Created time.Time `json:"created"`
}
type Permissions struct {
	mu     sync.Mutex
	path   string
	grants map[string]Grant
}

func NewPermissions(dir string) (*Permissions, error) {
	p := &Permissions{path: filepath.Join(dir, "grants.json"), grants: map[string]Grant{}}
	b, e := os.ReadFile(p.path)
	if os.IsNotExist(e) {
		return p, nil
	}
	if e != nil {
		return nil, e
	}
	var list []Grant
	if e = json.Unmarshal(b, &list); e != nil {
		return nil, e
	}
	for _, g := range list {
		if g.Scope != "always" || g.ID != g.Action.Key() {
			return nil, errors.New("invalid persisted grant")
		}
		p.grants[g.ID] = g
	}
	return p, nil
}
func (p *Permissions) Has(a Action) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.grants[a.Key()]
	return ok
}
func (p *Permissions) List() []Grant {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Grant{}
	for _, g := range p.grants {
		out = append(out, g)
	}
	return out
}
func (p *Permissions) saveLocked() error {
	out := []Grant{}
	for _, g := range p.grants {
		if g.Scope == "always" {
			out = append(out, g)
		}
	}
	return writeJSON(p.path, out)
}
func (p *Permissions) Add(a Action, scope string) error {
	if scope == "once" {
		return nil
	}
	if scope != "restart" && scope != "always" {
		return errors.New("scope must be once, restart or always")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id := a.Key()
	previous, existed := p.grants[id]
	p.grants[id] = Grant{id, a, scope, time.Now().UTC()}
	if e := p.saveLocked(); e != nil {
		if existed {
			p.grants[id] = previous
		} else {
			delete(p.grants, id)
		}
		return e
	}
	return nil
}
func (p *Permissions) Revoke(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.grants[id]
	if !ok {
		return errors.New("grant not found")
	}
	delete(p.grants, id)
	if e := p.saveLocked(); e != nil {
		p.grants[id] = g
		return e
	}
	return nil
}
