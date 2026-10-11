// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaVersion is the version of Document. A change to the document or
// to an engine's index settings bumps it, and engines rebuild their index
// when theirs is older.
const SchemaVersion = 1

// Document is what an external engine indexes for one active entry. The
// engine returns entry IDs only; meta loads the entries from the database.
type Document struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	TitleSort string `json:"title_sort"`
	// Titles by language group; romanized titles (any "-Latn") are their
	// own group, so engines can tokenize each with the right locale.
	TitlesJa    []string `json:"titles_ja"`
	TitlesZh    []string `json:"titles_zh"`
	TitlesKo    []string `json:"titles_ko"`
	TitlesLatn  []string `json:"titles_latn"`
	TitlesEn    []string `json:"titles_en"`
	TitlesOther []string `json:"titles_other"`
	People      []string `json:"people"`
	// Genre keys ("genre/fantasy") and normalized names, for the any-of
	// genre filter.
	GenreKeys  []string `json:"genre_keys"`
	GenreNames []string `json:"genre_names"`
	// Keys and normalized names of every tag and genre, for the all-of tag
	// filter and the exclusion filter.
	TagKeys  []string `json:"tag_keys"`
	TagNames []string `json:"tag_names"`
	// Keys of non-genre tags that aren't spoilers, for the tags facet.
	FacetTags        []string `json:"facet_tags"`
	Type             string   `json:"type"`
	Status           string   `json:"status"`
	ContentRating    string   `json:"content_rating"`
	OriginalLanguage string   `json:"original_language"`
	// Every title language, lower-cased, with its base language too
	// ("ja-latn" and "ja").
	TitleLanguages []string `json:"title_languages"`
	Year           *int32   `json:"year,omitempty"`
	Rating         *float64 `json:"rating,omitempty"`
	Popularity     *float64 `json:"popularity,omitempty"`
	PopularityRank *int64   `json:"popularity_rank,omitempty"`
	UpdatedAt      int64    `json:"updated_at"`
}

// TitleGroups are the Document title fields by group, for engines that
// configure them one by one.
var TitleGroups = []string{"titles_ja", "titles_zh", "titles_ko", "titles_latn", "titles_en", "titles_other"}

// titleGroup sorts a title language into a Document field.
func titleGroup(lang string) string {
	l := strings.ToLower(lang)
	base, _, _ := strings.Cut(l, "-")
	switch {
	case strings.Contains(l, "-latn"):
		return "titles_latn"
	case base == "ja":
		return "titles_ja"
	case base == "zh":
		return "titles_zh"
	case base == "ko":
		return "titles_ko"
	case base == "en":
		return "titles_en"
	}
	return "titles_other"
}

func (d *Document) addTitle(text, lang string) {
	switch titleGroup(lang) {
	case "titles_latn":
		d.TitlesLatn = append(d.TitlesLatn, text)
	case "titles_ja":
		d.TitlesJa = append(d.TitlesJa, text)
	case "titles_zh":
		d.TitlesZh = append(d.TitlesZh, text)
	case "titles_ko":
		d.TitlesKo = append(d.TitlesKo, text)
	case "titles_en":
		d.TitlesEn = append(d.TitlesEn, text)
	default:
		d.TitlesOther = append(d.TitlesOther, text)
	}
}

// LoadDocuments builds the documents of the given entries. Entries that
// aren't active (or don't exist) come back in gone: engines delete them.
func LoadDocuments(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (docs []Document, gone []uuid.UUID, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.title, e.type, e.status, e.content_rating, e.original_language,
			e.year, e.rating, e.popularity, e.popularity_rank, e.updated_at,
			coalesce((SELECT array_agg(t.text ORDER BY t.position) FROM entry_titles t WHERE t.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(t.language ORDER BY t.position) FROM entry_titles t WHERE t.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(DISTINCT p.name) FROM entry_credits c JOIN persons p ON p.id = c.person_id WHERE c.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(tg.key ORDER BY et.position) FROM entry_tags et JOIN tags tg ON tg.id = et.tag_id WHERE et.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(tg.name_key ORDER BY et.position) FROM entry_tags et JOIN tags tg ON tg.id = et.tag_id WHERE et.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(tg.is_genre ORDER BY et.position) FROM entry_tags et JOIN tags tg ON tg.id = et.tag_id WHERE et.entry_id = e.id), '{}'),
			coalesce((SELECT array_agg(et.is_spoiler OR tg.is_spoiler ORDER BY et.position) FROM entry_tags et JOIN tags tg ON tg.id = et.tag_id WHERE et.entry_id = e.id), '{}')
		FROM entries e
		WHERE e.id = ANY($1) AND e.state = 'active'`, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("search: load documents: %w", err)
	}
	defer rows.Close()
	found := map[uuid.UUID]bool{}
	for rows.Next() {
		var (
			id                                 uuid.UUID
			d                                  Document
			year                               *int32
			rating, popularity                 *float64
			rank                               *int64
			updated                            time.Time
			titles, langs, people, keys, names []string
			genre, spoiler                     []bool
		)
		if err := rows.Scan(&id, &d.Title, &d.Type, &d.Status, &d.ContentRating, &d.OriginalLanguage,
			&year, &rating, &popularity, &rank, &updated,
			&titles, &langs, &people, &keys, &names, &genre, &spoiler); err != nil {
			return nil, nil, err
		}
		found[id] = true
		d.ID = id.String()
		d.TitleSort = strings.ToLower(d.Title)
		d.Year, d.Rating, d.Popularity, d.PopularityRank = year, rating, popularity, rank
		d.UpdatedAt = updated.UnixMilli()
		d.OriginalLanguage = strings.ToLower(d.OriginalLanguage)
		var tl []string
		for i, text := range titles {
			lang := langs[i]
			d.addTitle(text, lang)
			if l := strings.ToLower(lang); l != "" {
				tl = append(tl, l)
				if base, _, ok := strings.Cut(l, "-"); ok {
					tl = append(tl, base)
				}
			}
		}
		d.TitleLanguages = uniq(tl)
		d.People = people
		for i, k := range keys {
			d.TagKeys = append(d.TagKeys, k)
			d.TagNames = append(d.TagNames, names[i])
			switch {
			case genre[i]:
				d.GenreKeys = append(d.GenreKeys, k)
				d.GenreNames = append(d.GenreNames, names[i])
			case !spoiler[i]:
				d.FacetTags = append(d.FacetTags, k)
			}
		}
		d.TagNames, d.GenreNames = uniq(d.TagNames), uniq(d.GenreNames)
		docs = append(docs, d.normalized())
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		if !found[id] {
			gone = append(gone, id)
		}
	}
	return docs, gone, nil
}

// normalized replaces nil lists with empty ones: engines want every field.
func (d Document) normalized() Document {
	for _, l := range []*[]string{
		&d.TitlesJa, &d.TitlesZh, &d.TitlesKo, &d.TitlesLatn, &d.TitlesEn, &d.TitlesOther, &d.People,
		&d.GenreKeys, &d.GenreNames, &d.TagKeys, &d.TagNames, &d.FacetTags, &d.TitleLanguages,
	} {
		if *l == nil {
			*l = []string{}
		}
	}
	return d
}

func uniq(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
