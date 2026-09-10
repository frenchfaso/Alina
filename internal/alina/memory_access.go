package alina

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

// Keep the existing on-disk archives and their stable IDs. A single family's
// conversations can read the mind directly, without copying its history.
// Older multi-family installations retain their original boundaries.
func (e *Engine) sharedMind() bool { return len(e.global.scopes) <= 1 }

func (e *Engine) recall(ctx context.Context, query string) (MemoryResults, error) {
	r, err := e.Memory.Recall(ctx, query)
	if err != nil || e == e.global || !e.sharedMind() {
		return r, err
	}
	mind, err := e.global.Memory.Recall(ctx, query)
	if err != nil {
		return r, err
	}
	r.Hits = append(r.Hits, mind.Hits...)
	sort.SliceStable(r.Hits, func(i, j int) bool { return r.Hits[i].Score > r.Hits[j].Score })
	if len(r.Hits) > 8 {
		r.Hits = r.Hits[:8]
	}
	return r, nil
}

func (e *Engine) readMemory(ctx context.Context, part string, offset int, now time.Time) (MemoryPage, error) {
	if part == "dreams" && e != e.global {
		if !e.sharedMind() {
			return MemoryPage{}, errors.New("global dream history is shared only in a single-family installation")
		}
		return e.global.Memory.ReadPage(ctx, part, offset, now)
	}
	p, err := e.Memory.ReadPage(ctx, part, offset, now)
	if errors.Is(err, sql.ErrNoRows) && e != e.global && e.sharedMind() {
		return e.global.Memory.ReadPage(ctx, part, offset, now)
	}
	return p, err
}
