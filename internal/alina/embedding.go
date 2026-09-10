package alina

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func readSmallFile(path string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
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
	rows, err := m.DB.QueryContext(ctx, `SELECT r.id,r.text FROM current_recall r LEFT JOIN note_vectors v ON v.id=r.id WHERE r.note=1 AND (v.vector IS NULL OR v.space<>?) ORDER BY r.day,r.id LIMIT ?`, space, limit)
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
		vec, err := m.embedding(ctx, i.text)
		if err != nil {
			return n, err
		}
		if _, err = m.DB.ExecContext(ctx, `INSERT INTO note_vectors VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET vector=excluded.vector,space=excluded.space`, i.id, vectorBytes(vec), space); err != nil {
			return n, err
		}
	}
	return len(pending), nil
}

type MemoryHit struct {
	Time        string  `json:"time,omitempty"`
	Session     string  `json:"source,omitempty"`
	Job         string  `json:"job,omitempty"`
	Attention   float64 `json:"attention"`
	SourceCount int     `json:"source_count,omitempty"`
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	Day         string  `json:"day"`
	Text        string  `json:"text"`
	Sources     string  `json:"sources"`
	Score       float64 `json:"score"`
	Match       string  `json:"match"`
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
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	if len(terms) == 0 {
		return out, nil
	}
	if len(terms) > 32 {
		terms = terms[:32]
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	now := time.Now()
	hits := map[string]MemoryHit{}
	rows, err := m.DB.QueryContext(ctx, `SELECT r.id,r.day,r.stamp,r.session,r.job,r.kind,r.sources,
 snippet(recall_fts,1,'','',' … ',64),COALESCE(a.touched,r.stamp)
 FROM recall_fts JOIN current_recall r ON r.id=recall_fts.id LEFT JOIN attention a ON a.id=r.id
 WHERE recall_fts MATCH ? ORDER BY rank LIMIT 64`, strings.Join(quoted, " OR "))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var h MemoryHit
		var touched string
		if err = rows.Scan(&h.ID, &h.Day, &h.Time, &h.Session, &h.Job, &h.Kind, &h.Sources, &h.Text, &touched); err != nil {
			rows.Close()
			return out, err
		}
		matches := 0.0
		for _, term := range terms {
			if strings.Contains(strings.ToLower(h.Text), term) {
				matches++
			}
		}
		h.Score = matches / float64(len(terms))
		h.Attention = attentionWeight(touched, now)
		h.Match = "text"
		hits[h.ID] = h
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	vec, err := m.embedding(ctx, query)
	if err != nil {
		out.Notice = "Embedding unavailable; text search used."
		vec = nil
	}
	compatible := 0
	if len(vec) > 0 {
		rows, err = m.DB.QueryContext(ctx, `SELECT r.id,r.day,r.stamp,r.session,r.job,r.kind,r.sources,r.text,COALESCE(a.touched,r.stamp),v.vector FROM note_vectors v JOIN current_recall r ON r.id=v.id LEFT JOIN attention a ON a.id=r.id WHERE v.space=?`, m.embeddingSpace())
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var h MemoryHit
			var touched string
			var data []byte
			if err = rows.Scan(&h.ID, &h.Day, &h.Time, &h.Session, &h.Job, &h.Kind, &h.Sources, &h.Text, &touched, &data); err != nil {
				rows.Close()
				return out, err
			}
			score, ok := cosine(vec, data)
			if !ok {
				continue
			}
			compatible++
			if score <= 0 {
				continue
			}
			h.Score = math.Max(hits[h.ID].Score, score)
			h.Match = "semantic"
			h.Attention = attentionWeight(touched, now)
			hits[h.ID] = h
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	for _, h := range hits {
		h.Score = 0.9*h.Score + 0.1*h.Attention
		var ids []string
		if json.Unmarshal([]byte(h.Sources), &ids) == nil {
			h.SourceCount = len(ids)
			if len(ids) > 8 {
				h.Sources = jsonText(ids[:8])
			}
		}
		h.Text = truncate(h.Text, 1800)
		out.Hits = append(out.Hits, h)
	}
	sort.Slice(out.Hits, func(i, j int) bool {
		if out.Hits[i].Score == out.Hits[j].Score {
			return out.Hits[i].ID < out.Hits[j].ID
		}
		return out.Hits[i].Score > out.Hits[j].Score
	})
	if len(out.Hits) > 5 {
		out.Hits = out.Hits[:5]
	}
	if compatible > 0 {
		out.Mode = "semantic+text"
	} else if len(vec) > 0 {
		out.Notice = "No compatible indexed notes; use POST /v1/memory/jobs with kind=reindex through alina api."
	}
	return out, nil
}

type MemoryPage struct {
	Text string `json:"text"`
	Next int    `json:"next_offset,omitempty"`
}

func (m *Memory) Read(ctx context.Context, part string, now time.Time) (string, error) {
	page, err := m.ReadPage(ctx, part, 0, now)
	if page.Next > 0 {
		page.Text += fmt.Sprintf("\n[continued: memory read part=%s offset=%d]", part, page.Next)
	}
	return page.Text, err
}
func (m *Memory) ReadPage(ctx context.Context, part string, offset int, now time.Time) (MemoryPage, error) {
	if offset < 0 {
		return MemoryPage{}, errors.New("offset must be non-negative")
	}
	const limit = 16000
	var b strings.Builder
	position := 0
	appendPart := func(s string) {
		end := position + len(s)
		if end > offset && b.Len() < limit+4 {
			start := max(0, offset-position)
			remaining := limit + 4 - b.Len()
			b.WriteString(s[start:min(len(s), start+remaining)])
		}
		position = end
	}
	switch part {
	case "dreams":
		rows, err := m.DB.QueryContext(ctx, `SELECT payload FROM jobs WHERE json_extract(payload,'$.kind')='dream' ORDER BY created DESC,id`)
		if err != nil {
			return MemoryPage{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			var job Job
			if err = rows.Scan(&raw); err != nil {
				return MemoryPage{}, err
			}
			if err = json.Unmarshal([]byte(raw), &job); err != nil {
				return MemoryPage{}, err
			}
			outcome := job.Status
			if job.Skipped {
				outcome = "skipped"
			}
			appendPart(fmt.Sprintf("\n## %s · %s · read part=%s\n%s\n%s\n", job.Created.In(m.loc).Format(time.RFC3339), outcome, job.ID, job.Output, job.Error))
			if b.Len() > limit {
				break
			}
		}
		if err = rows.Err(); err != nil {
			return MemoryPage{}, err
		}
	case "soul":
		text, notice := m.Soul()
		appendPart(text + "\n" + notice)
	case "focus":
		text, err := m.FocusContext(ctx, now)
		if err != nil {
			return MemoryPage{}, err
		}
		appendPart(text)
	case "recent":
		text, err := m.recentContext(ctx, "", 0)
		if err != nil {
			return MemoryPage{}, err
		}
		appendPart(text)
	default:
		var job Job
		var raw string
		err := m.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=?", part).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return MemoryPage{}, err
		}
		isJob := err == nil
		if isJob {
			if err = json.Unmarshal([]byte(raw), &job); err != nil {
				return MemoryPage{}, err
			}
			appendPart(fmt.Sprintf("# %s · %s · %s\n%s\n%s\n", job.Kind, job.Created.In(m.loc).Format(time.RFC3339), job.Status, job.Output, job.Error))
		}
		if part == "today" {
			part = now.In(m.loc).Format("2006-01-02")
		}
		after := int64(-1)
		if strings.HasPrefix(part, "after-") {
			var err error
			after, err = strconv.ParseInt(strings.TrimPrefix(part, "after-"), 10, 64)
			if err != nil || after < 0 {
				return MemoryPage{}, errors.New("invalid archive cursor")
			}
		}
		if _, err := time.Parse("2006-01-02", part); err == nil || part == "archive" || part == "week" || after >= 0 || isJob {
			query := "SELECT id,stamp,session,job,role,content FROM journal"
			var args []any
			order := " ORDER BY rowid"
			switch {
			case isJob:
				query += " WHERE job=?"
				args = []any{job.ID}
			case part == "week":
				appendPart("# Previous seven days · archive view\n")
				query += " WHERE day>=? AND day<?"
				args = []any{now.In(m.loc).AddDate(0, 0, -7).Format("2006-01-02"), now.In(m.loc).Format("2006-01-02")}
				order = " ORDER BY day,rowid"
			case after >= 0:
				query += " WHERE rowid>?"
				args = []any{after}
			case part != "archive":
				query += " WHERE day=?"
				args = []any{part}
			}
			if part != "week" {
				appendPart("# " + part + "\n")
			}
			rows, err := m.DB.QueryContext(ctx, query+order, args...)
			if err != nil {
				return MemoryPage{}, err
			}
			count := 0
			for rows.Next() {
				var entry MemoryEntry
				if err = rows.Scan(&entry.ID, &entry.Time, &entry.Session, &entry.Job, &entry.Role, &entry.Content); err != nil {
					rows.Close()
					return MemoryPage{}, err
				}
				count++
				appendPart(entryText(entry))
				if b.Len() > limit {
					break
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return MemoryPage{}, err
			}
			if count == 0 && part != "archive" && part != "week" && after < 0 && !isJob {
				var text string
				if err = m.DB.QueryRowContext(ctx, "SELECT summary FROM days WHERE day=?", part).Scan(&text); err != nil {
					return MemoryPage{}, err
				}
				appendPart(text)
			}
		} else {
			if !safeID(part) {
				return MemoryPage{}, errors.New("use focus/recent/archive/soul/YYYY-MM-DD or a memory/source ID")
			}
			var text string
			err := m.DB.QueryRowContext(ctx, `SELECT text FROM (
 SELECT '[' || kind || '] ' || stamp || ' · source: ' || session || ' · job: ' || job || char(10) || text || char(10) || 'Sources: ' || sources AS text FROM recall_items WHERE id=?
 UNION ALL SELECT content FROM evidence WHERE id=?
 ) LIMIT 1`, part, part).Scan(&text)
			if errors.Is(err, sql.ErrNoRows) {
				var stamp, session, job, role string
				err = m.DB.QueryRowContext(ctx, "SELECT stamp,session,job,role FROM sources WHERE id=?", part).Scan(&stamp, &session, &job, &role)
				text = "Archived source metadata (raw content unavailable): " + stamp + " " + session + " " + job + " " + role
			}
			if err != nil {
				return MemoryPage{}, err
			}
			var correction string
			er := m.DB.QueryRowContext(ctx, "SELECT id FROM facts WHERE supersedes=? ORDER BY stamp DESC LIMIT 1", part).Scan(&correction)
			if er == nil {
				text = "SUPERSEDED by " + correction + "\n" + text
			} else if !errors.Is(er, sql.ErrNoRows) {
				return MemoryPage{}, er
			}
			if offset == 0 {
				if err = m.touchRead(ctx, part, now); err != nil {
					return MemoryPage{}, err
				}
			}
			appendPart(text)
		}
	}
	text := b.String()
	next := 0
	if len(text) > limit {
		n := limit
		for n > 0 && text[n]&0xc0 == 0x80 {
			n--
		}
		text = text[:n]
		next = offset + n
	}
	return MemoryPage{Text: text, Next: next}, nil
}
