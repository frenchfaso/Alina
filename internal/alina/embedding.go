package alina

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

func readSmallFile(path string, limit int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) > limit {
		return "", errors.New("file exceeds limit")
	}
	return string(b), nil
}

func validateEmbedding(c MemoryConfig) error {
	u, err := url.Parse(c.EmbeddingURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid embedding endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return errors.New("embedding endpoint requires HTTPS (HTTP allowed only on loopback)")
	}
	if strings.TrimSpace(c.EmbeddingModel) == "" {
		return errors.New("embedding model required")
	}
	return nil
}
func (m *Memory) embedding(ctx context.Context, text string) ([]float32, error) {
	c := m.Config.Memory
	if c.EmbeddingURL == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	headers := map[string]string{}
	if c.EmbeddingKey != "" {
		headers["Authorization"] = "Bearer " + c.EmbeddingKey
	}
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	err := requestJSON(ctx, newHTTPClient(), http.MethodPost, c.EmbeddingURL, map[string]any{"model": c.EmbeddingModel, "input": text, "encoding_format": "float"}, headers, &response)
	if err != nil {
		return nil, err
	}
	if len(response.Data) != 1 || len(response.Data[0].Embedding) == 0 || len(response.Data[0].Embedding) > 8192 {
		return nil, errors.New("invalid embedding response")
	}
	vector := response.Data[0].Embedding
	norm := float64(0)
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, errors.New("non-finite embedding")
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return nil, errors.New("zero embedding")
	}
	return vector, nil
}
func (m *Memory) embeddingSpace() string {
	return contentID(m.Config.Memory.EmbeddingURL + "\n" + m.Config.Memory.EmbeddingModel)
}
func vectorBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}
func cosine(query []float32, data []byte) (float64, bool) {
	if len(query) == 0 || len(data) != len(query)*4 {
		return 0, false
	}
	var dot, a, b float64
	for i, x := range query {
		y := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])))
		if math.IsNaN(y) || math.IsInf(y, 0) {
			return 0, false
		}
		dot += float64(x) * y
		a += float64(x) * float64(x)
		b += y * y
	}
	if a == 0 || b == 0 {
		return 0, false
	}
	return dot / math.Sqrt(a*b), true
}
func (m *Memory) Reindex(ctx context.Context, limit int) (int, error) {
	if m.Config.Memory.EmbeddingURL == "" {
		return 0, nil
	}
	space := m.embeddingSpace()
	rows, err := m.DB.QueryContext(ctx, "SELECT id,text FROM memories WHERE vector IS NULL OR space<>? ORDER BY day,id LIMIT ?", space, limit)
	if err != nil {
		return 0, err
	}
	type item struct{ id, text string }
	var pending []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.text); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for n, i := range pending {
		vec, er := m.embedding(ctx, i.text)
		if er != nil {
			return n, er
		}
		if _, er = m.DB.ExecContext(ctx, "UPDATE memories SET vector=?,space=? WHERE id=?", vectorBytes(vec), space, i.id); er != nil {
			return n, er
		}
	}
	return len(pending), nil
}

type MemoryHit struct {
	ID      string  `json:"id"`
	Day     string  `json:"day"`
	Text    string  `json:"text"`
	Sources string  `json:"sources"`
	Score   float64 `json:"score"`
	Match   string  `json:"match"`
}
type MemoryResults struct {
	Hits   []MemoryHit `json:"hits"`
	Mode   string      `json:"mode"`
	Notice string      `json:"notice,omitempty"`
}

func (m *Memory) Recall(ctx context.Context, query string) (MemoryResults, error) {
	out := MemoryResults{Hits: []MemoryHit{}, Mode: "text"}
	if strings.TrimSpace(query) == "" || len(query) > 4000 {
		return out, errors.New("query must contain 1-4000 bytes")
	}
	vec, err := m.embedding(ctx, query)
	if err != nil {
		out.Notice = "Embedding unavailable; text search used."
		vec = nil
	}
	if len(vec) > 0 {
		out.Mode = "semantic+text"
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	rows, err := m.DB.QueryContext(ctx, "SELECT id,day,text,sources,vector,space FROM memories ORDER BY day DESC,id")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	compatible := 0
	for rows.Next() {
		var h MemoryHit
		var data []byte
		var space string
		if err = rows.Scan(&h.ID, &h.Day, &h.Text, &h.Sources, &data, &space); err != nil {
			return out, err
		}
		var matches float64
		for _, term := range terms {
			if strings.Contains(strings.ToLower(h.Text), term) {
				matches++
			}
		}
		if len(terms) > 0 {
			h.Score = matches / float64(len(terms))
			h.Match = "text"
		}
		if space == m.embeddingSpace() {
			if score, ok := cosine(vec, data); ok {
				compatible++
				h.Score = score + 0.1*h.Score
				h.Match = "semantic"
			}
		}
		if h.Score <= 0 {
			continue
		}
		out.Hits = append(out.Hits, h)
		sort.SliceStable(out.Hits, func(i, j int) bool { return out.Hits[i].Score > out.Hits[j].Score })
		if len(out.Hits) > 5 {
			out.Hits = out.Hits[:5]
		}
	}
	if len(vec) > 0 && compatible == 0 {
		out.Mode = "text"
		out.Notice = "No compatible indexed vectors; run alina memory reindex."
	}
	if out.Mode == "text" && out.Notice == "" {
		out.Notice = "Configure an embedding endpoint for semantic retrieval."
	}
	return out, rows.Err()
}
func (m *Memory) Read(ctx context.Context, part string, now time.Time) (string, error) {
	switch part {
	case "week":
		return m.week(ctx, now)
	case "soul":
		b, e := readSmallFile(m.Dir+"/soul.md", 1600)
		return b, e
	case "today":
		part = now.In(m.loc).Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", part); err != nil {
		return "", errors.New("use today, week, soul or YYYY-MM-DD")
	}
	entries, err := m.entries(ctx, part)
	if err != nil {
		return "", err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n", part)
	for _, e := range entries {
		s.WriteString(entryText(e))
	}
	if len(entries) == 0 {
		var summary string
		if err = m.DB.QueryRowContext(ctx, "SELECT summary FROM days WHERE day=?", part).Scan(&summary); err != nil {
			return "No stored records for this day.", nil
		}
		s.WriteString(summary)
	}
	return tailText(s.String(), 32000), nil
}
