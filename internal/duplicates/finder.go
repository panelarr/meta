// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FinderOptions tune candidate generation.
type FinderOptions struct {
	// MinScore keeps pairs scoring at least this. Default 0.6.
	MinScore float64
	// MaxPerEntry keeps the best pairs of each entry. Default 5.
	MaxPerEntry int
	// TitleSimilarity is the trigram similarity two titles need to be
	// compared at all. Default 0.6.
	TitleSimilarity float64
	// CommonTitle: a title key shared by more titles than this is too
	// common to pair on by itself ("love", "untitled"). Default 50.
	CommonTitle int
	// SimilarLimit: runs that look at no more entries than this also pair
	// on similar titles (trigrams), which costs tens of milliseconds per
	// title in a large catalogue. Larger runs (the first one) pair on
	// title keys and IDs only. Default 20000.
	SimilarLimit int64
	// Batch is how many entries are compared per round. Default 500.
	Batch int
	// Workers compare rounds in parallel. Default 4.
	Workers int
	Score   Options
}

func (o *FinderOptions) defaults() {
	if o.MinScore <= 0 {
		o.MinScore = 0.6
	}
	if o.MaxPerEntry <= 0 {
		o.MaxPerEntry = 5
	}
	if o.TitleSimilarity <= 0 {
		o.TitleSimilarity = 0.6
	}
	if o.CommonTitle <= 0 {
		o.CommonTitle = 50
	}
	if o.Batch <= 0 {
		o.Batch = 500
	}
	if o.SimilarLimit <= 0 {
		o.SimilarLimit = 20000
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
}

// Finder finds candidate pairs in the database.
type Finder struct {
	pool *pgxpool.Pool
	opts FinderOptions
}

// NewFinder returns a Finder.
func NewFinder(pool *pgxpool.Pool, opts FinderOptions) *Finder {
	opts.defaults()
	return &Finder{pool: pool, opts: opts}
}

// Report is what a run did.
type Report struct {
	Entries  int64
	Compared int64
	New      int64
	Updated  int64
	Removed  int64
	Skipped  map[string]int64
	// Similar: titles were also compared by trigram similarity.
	Similar bool
	// Time spent per step: finding partners by title, by similar title,
	// by ID, and scoring and storing.
	Took map[string]time.Duration
}

// Progress is told how far a run is.
type Progress func(done, total int64)

// JobID of the duplicates job, whose last successful run marks where the
// next incremental run starts.
const JobID = "duplicates"

// Since returns when the last successful run started; zero when there was
// none.
func (f *Finder) Since(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := f.pool.QueryRow(ctx, `SELECT started_at FROM job_runs WHERE job_id = $1 AND status = 'succeeded'
		ORDER BY started_at DESC LIMIT 1`, JobID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return t, err
}

// Run compares the active entries changed since since (all of them for the
// zero time) with their likely partners, and stores the pairs worth a
// look. Pairs a curator decided on are left alone.
func (f *Finder) Run(ctx context.Context, since time.Time, run uuid.UUID, progress Progress) (Report, error) {
	rep := Report{Skipped: map[string]int64{}, Took: map[string]time.Duration{}}
	// Pairs with an entry that is no longer active are moot.
	tag, err := f.pool.Exec(ctx, `DELETE FROM duplicate_candidates c WHERE c.status = 'open' AND EXISTS (
		SELECT 1 FROM entries e WHERE e.id IN (c.entry_a, c.entry_b) AND e.state <> 'active')`)
	if err != nil {
		return rep, err
	}
	rep.Removed += tag.RowsAffected()

	var total int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM entries WHERE state = 'active' AND updated_at > $1`, since).Scan(&total); err != nil {
		return rep, err
	}
	rep.Similar = total <= f.opts.SimilarLimit

	// One producer pages through the entries; workers compare the rounds.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	batches := make(chan []uuid.UUID)
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	for range f.opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ids := range batches {
				part := Report{Skipped: map[string]int64{}, Took: map[string]time.Duration{}, Similar: rep.Similar}
				if err := f.round(ctx, ids, run, &part); err != nil {
					fail(err)
					return
				}
				mu.Lock()
				rep.add(part)
				rep.Entries += int64(len(ids))
				done := rep.Entries
				mu.Unlock()
				if progress != nil {
					progress(done, total)
				}
			}
		}()
	}
	var after uuid.UUID
	for ctx.Err() == nil {
		rows, err := f.pool.Query(ctx, `SELECT id FROM entries WHERE state = 'active' AND updated_at > $1 AND id > $2
			ORDER BY id LIMIT $3`, since, after, f.opts.Batch)
		if err != nil {
			fail(err)
			break
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			fail(err)
			break
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		select {
		case batches <- ids:
		case <-ctx.Done():
		}
	}
	close(batches)
	wg.Wait()
	if firstErr != nil {
		return rep, firstErr
	}
	return rep, ctx.Err()
}

func (r *Report) add(o Report) {
	r.Compared += o.Compared
	r.New += o.New
	r.Updated += o.Updated
	r.Removed += o.Removed
	for k, v := range o.Skipped {
		r.Skipped[k] += v
	}
	for k, v := range o.Took {
		r.Took[k] += v
	}
}

type pair struct{ a, b uuid.UUID }

func ordered(x, y uuid.UUID) pair {
	if strings.Compare(x.String(), y.String()) > 0 {
		x, y = y, x
	}
	return pair{x, y}
}

// round compares one batch of entries with their partners.
func (f *Finder) round(ctx context.Context, ids []uuid.UUID, run uuid.UUID, rep *Report) error {
	partners, err := f.partners(ctx, ids, rep.Similar, rep.Took)
	if err != nil {
		return err
	}
	scoring := time.Now()
	defer func() { rep.Took["score"] += time.Since(scoring) }()
	if len(partners) == 0 {
		return nil
	}
	need := map[uuid.UUID]bool{}
	for p := range partners {
		need[p.a], need[p.b] = true, true
	}
	all := make([]uuid.UUID, 0, len(need))
	for id := range need {
		all = append(all, id)
	}
	feats, err := LoadFeatures(ctx, f.pool, all)
	if err != nil {
		return err
	}

	type scored struct {
		p     pair
		score float64
		sig   Signals
	}
	best := map[uuid.UUID][]scored{} // per entry of the batch
	inBatch := map[uuid.UUID]bool{}
	for _, id := range ids {
		inBatch[id] = true
	}
	var drop []pair
	for p := range partners {
		fa, fb := feats[p.a], feats[p.b]
		if fa == nil || fb == nil {
			continue
		}
		rep.Compared++
		score, sig, skip := Score(fa, fb, f.opts.Score)
		if skip != "" {
			rep.Skipped[skip]++
			drop = append(drop, p)
			continue
		}
		if score < f.opts.MinScore {
			drop = append(drop, p)
			continue
		}
		for _, side := range []uuid.UUID{p.a, p.b} {
			if inBatch[side] {
				best[side] = append(best[side], scored{p, score, sig})
			}
		}
	}
	keep := map[pair]scored{}
	for _, list := range best {
		slices.SortFunc(list, func(x, y scored) int {
			switch {
			case x.score > y.score:
				return -1
			case x.score < y.score:
				return 1
			}
			return strings.Compare(x.p.b.String(), y.p.b.String())
		})
		for _, s := range list[:min(len(list), f.opts.MaxPerEntry)] {
			keep[s.p] = s
		}
	}

	b := &pgx.Batch{}
	for _, s := range keep {
		sig, err := json.Marshal(s.sig)
		if err != nil {
			return err
		}
		b.Queue(`INSERT INTO duplicate_candidates (entry_a, entry_b, score, signals, pair_hash, run_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (entry_a, entry_b) DO UPDATE
			SET score = EXCLUDED.score, signals = EXCLUDED.signals,
			    verdict = CASE WHEN duplicate_candidates.pair_hash = EXCLUDED.pair_hash THEN duplicate_candidates.verdict END,
			    pair_hash = EXCLUDED.pair_hash, run_id = EXCLUDED.run_id, updated_at = now()
			WHERE duplicate_candidates.status = 'open'
			RETURNING (xmax = 0)`, s.p.a, s.p.b, s.score, sig, PairHash(feats[s.p.a], feats[s.p.b]), run).QueryRow(func(row pgx.Row) error {
			var inserted bool
			switch err := row.Scan(&inserted); {
			case errors.Is(err, pgx.ErrNoRows): // decided by a curator: left alone
			case err != nil:
				return err
			case inserted:
				rep.New++
			default:
				rep.Updated++
			}
			return nil
		})
	}
	// Open pairs that no longer qualify go.
	for _, p := range drop {
		b.Queue(`DELETE FROM duplicate_candidates WHERE entry_a = $1 AND entry_b = $2 AND status = 'open'`, p.a, p.b).Exec(func(tag pgconn.CommandTag) error {
			rep.Removed += tag.RowsAffected()
			return nil
		})
	}
	return f.pool.SendBatch(ctx, b).Close()
}

// partners finds, for a batch of entries, the entries worth comparing
// with: the same normalized title (unless it is too common), a similar
// title (trigrams), or an ID at another site in common.
func (f *Finder) partners(ctx context.Context, ids []uuid.UUID, similar bool, took map[string]time.Duration) (map[pair]bool, error) {
	out := map[pair]bool{}
	step := time.Now()
	timed := func(name string) {
		took[name] += time.Since(step)
		step = time.Now()
	}
	add := func(rows pgx.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b uuid.UUID
			if err := rows.Scan(&a, &b); err != nil {
				return err
			}
			if a != b {
				out[ordered(a, b)] = true
			}
		}
		return rows.Err()
	}
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL pg_trgm.similarity_threshold = %.2f", f.opts.TitleSimilarity)); err != nil {
			return err
		}
		if err := add(tx.Query(ctx, `
			SELECT DISTINCT s.entry_id, o.entry_id
			FROM (SELECT DISTINCT entry_id, meta_title_key(norm) AS k FROM entry_titles WHERE entry_id = ANY($1)) s
			JOIN entry_titles o ON meta_title_key(o.norm) = s.k AND o.entry_id <> s.entry_id
			JOIN entries e ON e.id = o.entry_id AND e.state = 'active'
			WHERE length(s.k) >= 2
			  AND (SELECT count(*) FROM (SELECT 1 FROM entry_titles c WHERE meta_title_key(c.norm) = s.k LIMIT $2 + 1) x) <= $2`,
			ids, f.opts.CommonTitle)); err != nil {
			return err
		}
		timed("same_title")
		if similar {
			// Similar titles: the display-worthy ones (primary, or long enough
			// to mean something), a few best matches each.
			if err := add(tx.Query(ctx, `
				SELECT s.entry_id, m.entry_id
				FROM (SELECT DISTINCT entry_id, norm FROM entry_titles
				      WHERE entry_id = ANY($1) AND (is_primary OR length(norm) >= 8)) s
				CROSS JOIN LATERAL (
					SELECT o.entry_id FROM entry_titles o
					JOIN entries e ON e.id = o.entry_id AND e.state = 'active'
					WHERE o.norm % s.norm AND o.entry_id <> s.entry_id
					ORDER BY similarity(o.norm, s.norm) DESC
					LIMIT 8
				) m`, ids)); err != nil {
				return err
			}
			timed("similar_title")
		}
		defer timed("shared_ids")
		return add(tx.Query(ctx, `
			SELECT DISTINCT x.entry_id, y.entry_id
			FROM entry_external_ids x
			JOIN entry_external_ids y ON y.provider = x.provider AND y.external_id = x.external_id AND y.entry_id <> x.entry_id
			JOIN entries e ON e.id = y.entry_id AND e.state = 'active'
			WHERE x.entry_id = ANY($1)`, ids))
	})
	return out, err
}

// LoadFeatures loads what scoring compares of the given entries.
func LoadFeatures(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (map[uuid.UUID]*Features, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.state, e.title, e.type, e.original_language, coalesce(e.year, 0), coalesce(e.content_hash, ''::bytea),
			coalesce((SELECT array_agg(DISTINCT t.norm) FROM entry_titles t WHERE t.entry_id = e.id AND t.norm <> ''), '{}'),
			coalesce((SELECT array_agg(DISTINCT p.name_key) FROM entry_credits c JOIN persons p ON p.id = c.person_id WHERE c.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(pe.provider || ':' || pe.external_id) FROM provider_entries pe WHERE pe.entry_id = e.id AND pe.state = 'active'), '{}'),
			coalesce((SELECT array_agg(x.provider || ':' || x.external_id) FROM entry_external_ids x WHERE x.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(r.target_provider || ':' || r.target_id) FROM entry_relations r WHERE r.entry_id = e.id), '{}')
		FROM entries e WHERE e.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*Features{}
	for rows.Next() {
		f := &Features{}
		var records, ext, rel []string
		if err := rows.Scan(&f.ID, &f.State, &f.Title, &f.Type, &f.Language, &f.Year, &f.ContentHash,
			&f.Titles, &f.People, &records, &ext, &rel); err != nil {
			return nil, err
		}
		f.Records, f.ExternalIDs, f.Relations = refs(records), refs(ext), refs(rel)
		out[f.ID] = f
	}
	return out, rows.Err()
}

func refs(vals []string) []Ref {
	out := make([]Ref, 0, len(vals))
	for _, v := range vals {
		p, id, ok := strings.Cut(v, ":")
		if ok {
			out = append(out, Ref{Provider: p, ID: id})
		}
	}
	return out
}
