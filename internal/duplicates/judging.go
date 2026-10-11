// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JudgeOptions limit a judging round.
type JudgeOptions struct {
	// MinScore: only pairs scoring at least this are sent. Default 0.6.
	MinScore float64
	// MaxPairs per run. Default 200.
	MaxPairs int
	// MaxTokens per run (input tokens; the APIs charge for those). Default
	// 500,000.
	MaxTokens int64
	// Covers sends cover images to judges that take them.
	Covers bool
}

func (o *JudgeOptions) defaults() {
	if o.MinScore <= 0 {
		o.MinScore = 0.6
	}
	if o.MaxPairs <= 0 {
		o.MaxPairs = 200
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 500000
	}
}

// JudgeReport is what a judging round did.
type JudgeReport struct {
	Judged int64
	Cached int64
	Failed int64
	Tokens int64
	// Stopped says why the round stopped early, if it did.
	Stopped string
}

// JudgeOpen asks j about the best open pairs that have no verdict yet. An
// unchanged pair already judged by the same judge and model reuses that
// verdict instead of a new request.
func JudgeOpen(ctx context.Context, pool *pgxpool.Pool, j Judge, opts JudgeOptions, note func(level, msg, ref string)) (JudgeReport, error) {
	opts.defaults()
	var rep JudgeReport
	rows, err := pool.Query(ctx, `SELECT id, entry_a, entry_b, pair_hash FROM duplicate_candidates
		WHERE status = 'open' AND verdict IS NULL AND score >= $1
		ORDER BY score DESC, id LIMIT $2`, opts.MinScore, opts.MaxPairs)
	if err != nil {
		return rep, err
	}
	type cand struct {
		id   int64
		a, b uuid.UUID
		hash []byte
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		err := r.Scan(&c.id, &c.a, &c.b, &c.hash)
		return c, err
	})
	if err != nil {
		return rep, err
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	for _, c := range list {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		var cached []byte
		err := pool.QueryRow(ctx, `SELECT verdict FROM duplicate_verdicts WHERE pair_hash = $1 AND judge = $2`, c.hash, j.Key()).Scan(&cached)
		switch {
		case err == nil:
			if _, err := pool.Exec(ctx, `UPDATE duplicate_candidates SET verdict = $2, updated_at = now() WHERE id = $1`, c.id, cached); err != nil {
				return rep, err
			}
			rep.Cached++
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return rep, err
		}
		if rep.Tokens >= opts.MaxTokens {
			rep.Stopped = "the token limit of the run (judge.max_tokens) was reached"
			return rep, nil
		}
		a, err := LoadSide(ctx, pool, hc, c.a, opts.Covers)
		if err != nil {
			return rep, err
		}
		b, err := LoadSide(ctx, pool, hc, c.b, opts.Covers)
		if err != nil {
			return rep, err
		}
		v, err := j.Judge(ctx, a, b)
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			rep.Failed++
			if note != nil {
				note("warning", err.Error(), c.a.String()+" "+c.b.String())
			}
			// Several failures in a row: the API is down or the key wrong.
			if rep.Failed >= 5 && rep.Judged == 0 {
				rep.Stopped = "the decision model failed five times: " + err.Error()
				return rep, nil
			}
			continue
		}
		rep.Judged++
		rep.Tokens += v.Tokens
		raw, err := json.Marshal(v)
		if err != nil {
			return rep, err
		}
		if _, err := pool.Exec(ctx, `INSERT INTO duplicate_verdicts (pair_hash, judge, verdict) VALUES ($1, $2, $3)
			ON CONFLICT (pair_hash, judge) DO UPDATE SET verdict = EXCLUDED.verdict, created_at = now()`, c.hash, j.Key(), raw); err != nil {
			return rep, err
		}
		if _, err := pool.Exec(ctx, `UPDATE duplicate_candidates SET verdict = $2, updated_at = now() WHERE id = $1`, c.id, raw); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// LoadSide loads what a judge is told about an entry. With covers, the
// cover image is fetched (at most 2 MB).
func LoadSide(ctx context.Context, pool *pgxpool.Pool, hc *http.Client, id uuid.UUID, covers bool) (Side, error) {
	var s Side
	var year *int32
	var cover []byte
	var titles, langs []string
	err := pool.QueryRow(ctx, `
		SELECT e.type, e.status, e.original_language, e.year, coalesce(e.cover, 'null'),
			coalesce((SELECT array_agg(t.text ORDER BY t.position) FROM entry_titles t WHERE t.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(t.language ORDER BY t.position) FROM entry_titles t WHERE t.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(DISTINCT p.name) FROM entry_credits c JOIN persons p ON p.id = c.person_id WHERE c.entry_id = e.id), '{}'),
			coalesce((SELECT d.text FROM entry_descriptions d WHERE d.entry_id = e.id
				ORDER BY (d.language = 'en') DESC, d.position LIMIT 1), '')
		FROM entries e WHERE e.id = $1`, id).Scan(&s.Type, &s.Status, &s.Language, &year, &cover, &titles, &langs, &s.People, &s.Description)
	if err != nil {
		return s, err
	}
	if year != nil {
		s.Year = int(*year)
	}
	for i, t := range titles {
		s.Titles = append(s.Titles, TitleText{Text: t, Language: langs[i]})
	}
	s.Description = excerpt(s.Description, 400)
	if covers {
		var img struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(cover, &img) == nil && strings.HasPrefix(img.URL, "http") {
			s.Cover, s.CoverType = fetch(ctx, hc, img.URL)
		}
	}
	return s, nil
}

// excerpt cuts text to about n bytes at a word boundary.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// fetch downloads an image; failures just leave it out.
func fetch(ctx context.Context, hc *http.Client, url string) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ""
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, ""
	}
	defer res.Body.Close()
	typ := res.Header.Get("Content-Type")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(typ, "image/") {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return nil, ""
	}
	return data, typ
}
