package alina

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSharedRecallReusesQueryEmbedding(t *testing.T) {
	for _, tc := range []struct {
		name, indexed             string
		different, fail, disabled bool
		want                      int32
	}{
		{name: "matching", want: 1},
		{name: "global-index-only", indexed: "global", want: 1},
		{name: "family-index-only", indexed: "family", want: 1},
		{name: "different", different: true, want: 2},
		{name: "unavailable", fail: true, want: 1},
		{name: "text-only", disabled: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
			}))
			defer server.Close()
			c := DefaultConfig()
			c.WorkDir = t.TempDir()
			c.Users = []User{{ID: "owner", Name: "Owner", TelegramID: 42, Family: "home"}}
			c.LocalUser = "owner"
			if !tc.disabled {
				c.Memory.EmbeddingURL, c.Memory.EmbeddingModel = server.URL, "fixture"
			}
			e, err := NewEngine(c.WorkDir, c, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			family := e.localEngine()
			if tc.different {
				e.Memory.Config.Memory.EmbeddingModel = "other-fixture"
			}
			for i, m := range []*Memory{family.Memory, e.Memory} {
				id := fmt.Sprintf("note-%d", i)
				if _, err = m.DB.Exec(`INSERT INTO memories(id,day,text,sources) VALUES(?,?,?,?)`, id, "2026-09-30", "orchid fact", "[]"); err != nil {
					t.Fatal(err)
				}
				if tc.indexed == "global" && i == 0 || tc.indexed == "family" && i == 1 {
					continue
				}
				if _, err = m.DB.Exec(`INSERT INTO note_vectors(id,space,vector) VALUES(?,?,?)`, id, m.embeddingSpace(), vectorBytes([]float32{1, 0})); err != nil {
					t.Fatal(err)
				}
			}
			result, err := family.recall(context.Background(), "orchid")
			if err != nil || calls.Load() != tc.want || len(result.Hits) != 2 {
				t.Fatal("incorrect shared recall", calls.Load(), result, err)
			}
			if tc.fail && (result.Notice == "" || result.Mode != "text") {
				t.Fatal("lost text fallback", result)
			}
			if !tc.fail && !tc.disabled && (result.Mode != "semantic+text" || result.Notice != "") {
				t.Fatal("semantic mode lost or false reindex notice", result)
			}
			calls.Store(0)
			for _, query := range []string{"", "?!"} {
				_, _ = family.recall(context.Background(), query)
			}
			if calls.Load() != 0 {
				t.Fatal("invalid queries called embedding endpoint")
			}
		})
	}
}
