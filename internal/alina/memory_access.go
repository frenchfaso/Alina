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
	if e == e.global || !e.sharedMind() {
		return e.Memory.Recall(ctx, query)
	}
	// Lazy and local to this recall: invalid queries/text-only installations make
	// no HTTP request, and matching stores reuse both the vector and any failure.
	var vector []float32
	var embeddingErr error
	var embedded bool
	embed := func() ([]float32, error) {
		if !embedded {
			vector, embeddingErr = e.Memory.embedding(ctx, query)
			embedded = true
		}
		return vector, embeddingErr
	}
	r, err := e.Memory.recall(ctx, query, embed)
	if err != nil {
		return r, err
	}
	mindEmbed := embed
	if e.Memory.embeddingSpace() != e.global.Memory.embeddingSpace() {
		mindEmbed = func() ([]float32, error) { return e.global.Memory.embedding(ctx, query) }
	}
	mind, err := e.global.Memory.recall(ctx, query, mindEmbed)
	if err != nil {
		return r, err
	}
	r.Hits = append(r.Hits, mind.Hits...)
	if mind.Mode == "semantic+text" {
		r.Mode = mind.Mode
	}
	if r.Notice == "" {
		r.Notice = mind.Notice
	}
	if r.Mode == "semantic+text" && r.Notice == noIndexedNotesNotice {
		r.Notice = ""
	}
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
