// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/panelarr/meta/internal/textnorm"
)

// MaxCandidates caps how many title matches and how many name matches a
// search considers. Results are ranked best first, so this only limits how
// deep a client can page into weak matches.
const MaxCandidates = 1000

// Postgres searches with the database's own tools: trigram similarity and
// substring matching (pg_trgm) over normalized titles and people's names.
// Browse and search share the filters and sort orders.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns the PostgreSQL backend.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Capabilities of the PostgreSQL backend.
func (p *Postgres) Capabilities() Capabilities {
	return Capabilities{Name: "postgres", TypoTolerance: true, Substring: true}
}

// Upsert does nothing: the backend reads the database directly.
func (p *Postgres) Upsert(context.Context, ...uuid.UUID) error { return nil }

// Delete does nothing: the backend reads the database directly.
func (p *Postgres) Delete(context.Context, ...uuid.UUID) error { return nil }

// Rebuild does nothing: entries are indexed as they are written.
func (p *Postgres) Rebuild(context.Context) error { return nil }

// builder collects SQL fragments and their arguments.
type builder struct {
	args  []any
	where []string
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// tagRefs splits tag filter values into tag keys ("genre/fantasy",
// "tag/settings/fantasy") and normalized names ("Fantasy" matches every
// tag or genre with that name).
func tagRefs(values []string) (keys, names []string) {
	keys, names = []string{}, []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(v, "genre/") || strings.HasPrefix(v, "tag/"):
			keys = append(keys, strings.ToLower(v))
		default:
			if k := textnorm.Key(v); k != "" {
				names = append(names, k)
			}
		}
	}
	return keys, names
}

func lower(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// filters adds the WHERE conditions of f for the entries alias e.
func (b *builder) filters(f Filter) {
	if !f.IncludeInactive {
		b.where = append(b.where, "e.state = 'active'")
	}
	if v := lower(f.Types); len(v) > 0 {
		b.where = append(b.where, "e.type = ANY("+b.arg(v)+"::text[])")
	}
	if v := lower(f.Statuses); len(v) > 0 {
		b.where = append(b.where, "e.status = ANY("+b.arg(v)+"::text[])")
	}
	if v := lower(f.ContentRatings); len(v) > 0 {
		b.where = append(b.where, "e.content_rating = ANY("+b.arg(v)+"::text[])")
	}
	if v := lower(f.Languages); len(v) > 0 {
		b.where = append(b.where, "lower(e.original_language) = ANY("+b.arg(v)+"::text[])")
	}
	if f.YearFrom > 0 {
		b.where = append(b.where, "e.year >= "+b.arg(f.YearFrom))
	}
	if f.YearTo > 0 {
		b.where = append(b.where, "e.year <= "+b.arg(f.YearTo))
	}
	if keys, names := tagRefs(f.Genres); len(keys)+len(names) > 0 {
		b.where = append(b.where, `EXISTS (SELECT 1 FROM entry_tags et JOIN tags t ON t.id = et.tag_id
			WHERE et.entry_id = e.id AND t.is_genre AND (t.key = ANY(`+b.arg(keys)+`::text[]) OR t.name_key = ANY(`+b.arg(names)+`::text[])))`)
	}
	// Every listed tag must be there.
	for _, ref := range f.Tags {
		keys, names := tagRefs([]string{ref})
		if len(keys)+len(names) == 0 {
			continue
		}
		b.where = append(b.where, `EXISTS (SELECT 1 FROM entry_tags et JOIN tags t ON t.id = et.tag_id
			WHERE et.entry_id = e.id AND (t.key = ANY(`+b.arg(keys)+`::text[]) OR t.name_key = ANY(`+b.arg(names)+`::text[])))`)
	}
	if keys, names := tagRefs(f.ExcludeTags); len(keys)+len(names) > 0 {
		b.where = append(b.where, `NOT EXISTS (SELECT 1 FROM entry_tags et JOIN tags t ON t.id = et.tag_id
			WHERE et.entry_id = e.id AND (t.key = ANY(`+b.arg(keys)+`::text[]) OR t.name_key = ANY(`+b.arg(names)+`::text[])))`)
	}
	if v := lower(f.TitleLanguages); len(v) > 0 {
		a := b.arg(v)
		b.where = append(b.where, `EXISTS (SELECT 1 FROM entry_titles tl WHERE tl.entry_id = e.id
			AND (lower(tl.language) = ANY(`+a+`::text[]) OR split_part(lower(tl.language), '-', 1) = ANY(`+a+`::text[])))`)
	}
}

func orderBy(s Sort, reverse bool) string {
	dir := func(natural string) string {
		if !reverse {
			return natural
		}
		if natural == "ASC" {
			return "DESC"
		}
		return "ASC"
	}
	switch s {
	case SortRating:
		return "e.rating " + dir("DESC") + " NULLS LAST, e.id"
	case SortUpdated:
		return "e.updated_at " + dir("DESC") + ", e.id"
	case SortYear:
		return "e.year " + dir("DESC") + " NULLS LAST, e.id"
	case SortTitle:
		return "lower(e.title) " + dir("ASC") + ", e.id"
	case SortRelevance:
		return "h.score " + dir("DESC") + ", e.popularity_rank ASC NULLS LAST, e.id"
	default: // popularity
		return "e.popularity_rank " + dir("ASC") + " NULLS LAST, e.popularity " + dir("DESC") + " NULLS LAST, e.id"
	}
}

// Query runs a browse or search.
func (p *Postgres) Query(ctx context.Context, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 24
	}
	text := textnorm.Key(q.Text)
	if strings.TrimSpace(q.Text) != "" && text == "" {
		return Result{}, nil // only punctuation: nothing can match
	}
	b := &builder{}
	var sql string
	if text == "" {
		b.filters(q.Filter)
		sort := q.Sort
		if sort == SortRelevance {
			sort = SortPopularity
		}
		sql = `SELECT e.id, 0::float8, '', '', count(*) OVER () FROM entries e` + where(b) +
			` ORDER BY ` + orderBy(sort, q.Reverse)
	} else {
		sort := q.Sort
		if sort == SortDefault {
			sort = SortRelevance
		}
		t := b.arg(text)
		like := b.arg("%" + text + "%")
		prefix := b.arg(text + "%")
		cap := b.arg(MaxCandidates)
		b.filters(q.Filter)
		sql = `WITH title_hits AS (
				SELECT DISTINCT ON (t.entry_id) t.entry_id, t.text AS matched, 'title' AS field,
					CASE WHEN t.norm = ` + t + ` THEN 1.0
					     WHEN t.norm LIKE ` + prefix + ` THEN 0.9
					     ELSE greatest(similarity(t.norm, ` + t + `), word_similarity(` + t + `, t.norm)) * 0.85
					END + CASE WHEN t.is_primary THEN 0.02 ELSE 0 END AS score
				FROM entry_titles t
				WHERE t.norm % ` + t + ` OR t.norm LIKE ` + like + ` 
				ORDER BY t.entry_id, score DESC
			), title_top AS (
				SELECT * FROM title_hits ORDER BY score DESC LIMIT ` + cap + `
			), person_hits AS (
				SELECT DISTINCT ON (c.entry_id) c.entry_id, pe.name AS matched, 'person' AS field,
					CASE WHEN pe.name_key = ` + t + ` THEN 0.8
					     ELSE greatest(similarity(pe.name_key, ` + t + `), word_similarity(` + t + `, pe.name_key)) * 0.7
					END AS score
				FROM persons pe
				JOIN entry_credits c ON c.person_id = pe.id
				WHERE pe.name_key % ` + t + ` OR pe.name_key LIKE ` + like + ` 
				ORDER BY c.entry_id, score DESC
			), person_top AS (
				SELECT * FROM person_hits ORDER BY score DESC LIMIT ` + cap + `
			), hits AS (
				SELECT DISTINCT ON (entry_id) entry_id, matched, field, score
				FROM (SELECT * FROM title_top UNION ALL SELECT * FROM person_top) u
				ORDER BY entry_id, score DESC
			)
			SELECT e.id, h.score::float8, h.matched, h.field, count(*) OVER ()
			FROM hits h JOIN entries e ON e.id = h.entry_id` + where(b) +
			` ORDER BY ` + orderBy(sort, q.Reverse)
	}
	sql += ` LIMIT ` + b.arg(q.Limit) + ` OFFSET ` + b.arg(q.Offset)

	rows, err := p.pool.Query(ctx, sql, b.args...)
	if err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var res Result
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.Score, &h.Matched, &h.Field, &res.Total); err != nil {
			return Result{}, err
		}
		res.Hits = append(res.Hits, h)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return res, nil
}

func where(b *builder) string {
	if len(b.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(b.where, " AND ")
}
