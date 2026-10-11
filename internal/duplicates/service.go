// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/secret"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Errors the API maps to codes.
var (
	ErrInvalid   = errors.New("invalid")
	ErrNotFound  = errors.New("not found")
	ErrDecided   = errors.New("the pair was already decided")
	ErrNoSecrets = errors.New("secrets unavailable")
)

// Service runs the duplicates job and the curator's decisions.
type Service struct {
	st  *store.Store
	q   *db.Queries
	w   *catalog.Writer
	box *secret.Box
	cfg config.Duplicates
}

// NewService returns the service.
func NewService(st *store.Store, w *catalog.Writer, box *secret.Box, cfg config.Duplicates) *Service {
	return &Service{st: st, q: db.New(st.Pool), w: w, box: box, cfg: cfg}
}

// Finder returns the finder with the configured options.
func (s *Service) Finder() *Finder {
	return NewFinder(s.st.Pool, FinderOptions{
		MinScore: s.cfg.MinScore, MaxPerEntry: s.cfg.MaxPerEntry,
		Score: Options{SkipSameProvider: s.cfg.SkipSameProvider},
	})
}

// Run is the duplicates job: compare what changed since the last
// successful run (everything with full), then let the decision model
// judge the best open pairs, if one is on.
func (s *Service) Run(ctx context.Context, r *jobs.Recorder, full, judge bool) error {
	run := r.RunID()
	f := s.Finder()
	since := time.Time{}
	if !full {
		var err error
		if since, err = f.Since(ctx); err != nil {
			return err
		}
	}
	rep, err := f.Run(ctx, since, run, func(done, total int64) { r.Progress(ctx, "compare", done, total, "entries") })
	r.Set("entries", rep.Entries)
	r.Set("compared", rep.Compared)
	r.Set("new", rep.New)
	r.Set("updated", rep.Updated)
	r.Set("removed", rep.Removed)
	for k, v := range rep.Skipped {
		r.Set("skipped_"+k, v)
	}
	if err != nil {
		return err
	}
	if !rep.Similar {
		r.Note("info", fmt.Sprintf("%d entries to compare: similar titles were left out (more than %d); equal title keys and IDs were compared",
			rep.Entries, f.opts.SimilarLimit), "")
	}
	msg := fmt.Sprintf("compared %d entries: %d new pairs, %d updated", rep.Entries, rep.New, rep.Updated)

	js, err := s.JudgeSettings(ctx)
	if err != nil {
		return err
	}
	if judge && js.Enabled {
		j, err := NewJudge(js.config())
		if err != nil {
			return err
		}
		jr, err := JudgeOpen(ctx, s.st.Pool, j, JudgeOptions{
			MinScore: s.cfg.Judge.MinScore, MaxPairs: s.cfg.Judge.MaxPairs, MaxTokens: s.cfg.Judge.MaxTokens, Covers: js.Covers,
		}, r.Note)
		r.Set("judged", jr.Judged)
		r.Set("judged_cached", jr.Cached)
		r.Set("judge_failed", jr.Failed)
		r.Set("judge_tokens", jr.Tokens)
		if err != nil {
			return err
		}
		if jr.Stopped != "" {
			r.Note("warning", "judging stopped: "+jr.Stopped, "")
		}
		msg += fmt.Sprintf("; %d judged (%d from cache)", jr.Judged, jr.Cached)
	}
	r.Message("%s", msg)
	return nil
}

// Confirm merges the pair as the curator decided: keep is the entry that
// stays ("a" or "b"; empty keeps the more popular one). It returns the
// pair and the entry that stays.
func (s *Service) Confirm(ctx context.Context, id int64, keep, by, note string) (db.DuplicateCandidate, uuid.UUID, error) {
	c, err := s.get(ctx, id)
	if err != nil {
		return c, uuid.Nil, err
	}
	if c.Status != "open" {
		return c, uuid.Nil, ErrDecided
	}
	into, from := c.EntryA, c.EntryB
	switch keep {
	case "a":
	case "b":
		into, from = c.EntryB, c.EntryA
	case "":
		var swap bool
		if err := s.st.Pool.QueryRow(ctx, `SELECT coalesce(b.popularity_rank, 1e18) < coalesce(a.popularity_rank, 1e18)
			FROM entries a, entries b WHERE a.id = $1 AND b.id = $2`, c.EntryA, c.EntryB).Scan(&swap); err != nil {
			return c, uuid.Nil, err
		}
		if swap {
			into, from = c.EntryB, c.EntryA
		}
	default:
		return c, uuid.Nil, fmt.Errorf("%w: keep must be a or b", ErrInvalid)
	}
	if _, err := s.w.MergeEntries(ctx, from, into, "confirmed duplicate"); err != nil {
		if errors.Is(err, catalog.ErrNotActive) {
			return c, uuid.Nil, fmt.Errorf("%w: %w", ErrDecided, err)
		}
		return c, uuid.Nil, err
	}
	c, err = s.q.DecideDuplicate(ctx, db.DecideDuplicateParams{ID: id, Status: "confirmed", DecidedBy: by, Note: note})
	if err != nil {
		return c, uuid.Nil, err
	}
	if err := s.q.DropOpenDuplicatesOf(ctx, from); err != nil {
		return c, uuid.Nil, err
	}
	return c, into, nil
}

// Dismiss records that the pair are different works; it never comes back.
func (s *Service) Dismiss(ctx context.Context, id int64, by, note string) (db.DuplicateCandidate, error) {
	if _, err := s.get(ctx, id); err != nil {
		return db.DuplicateCandidate{}, err
	}
	c, err := s.q.DecideDuplicate(ctx, db.DecideDuplicateParams{ID: id, Status: "dismissed", DecidedBy: by, Note: note})
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrDecided
	}
	return c, err
}

// Reopen takes back a dismissal.
func (s *Service) Reopen(ctx context.Context, id int64) (db.DuplicateCandidate, error) {
	if _, err := s.get(ctx, id); err != nil {
		return db.DuplicateCandidate{}, err
	}
	c, err := s.q.ReopenDuplicate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("%w: only dismissed pairs can be reopened", ErrDecided)
	}
	return c, err
}

func (s *Service) get(ctx context.Context, id int64) (db.DuplicateCandidate, error) {
	c, err := s.q.GetDuplicateCandidate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("%w: no duplicate pair %d", ErrNotFound, id)
	}
	return c, err
}

// JudgeSettings choose and reach the decision model.
type JudgeSettings struct {
	Enabled bool
	Kind    string
	URL     string
	Model   string
	APIKey  string
	Covers  bool
	// Origin: "config" or "admin".
	Origin string
}

func (js JudgeSettings) config() JudgeConfig {
	return JudgeConfig{Kind: js.Kind, URL: js.URL, Model: js.Model, APIKey: js.APIKey, Covers: js.Covers}
}

// JudgeSettings returns the settings in force: the admin's when saved,
// else the configuration's. The key is included; never return it.
func (s *Service) JudgeSettings(ctx context.Context) (JudgeSettings, error) {
	row, err := s.q.GetJudgeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		j := s.cfg.Judge
		return JudgeSettings{Enabled: j.Enabled, Kind: j.Kind, URL: j.URL, Model: j.Model, APIKey: j.APIKey, Covers: j.Covers, Origin: "config"}, nil
	}
	if err != nil {
		return JudgeSettings{}, err
	}
	js := JudgeSettings{Enabled: row.Enabled, Kind: row.Kind, URL: row.Url, Model: row.Model, Covers: row.Covers, Origin: "admin"}
	if row.ApiKeySealed.Valid {
		if js.APIKey, err = s.box.Open(row.ApiKeySealed.String); err != nil {
			return JudgeSettings{}, fmt.Errorf("decision model key: %w", err)
		}
	}
	return js, nil
}

func validJudge(js JudgeSettings) error {
	if js.Kind == "" && !js.Enabled {
		return nil
	}
	if _, err := NewJudge(js.config()); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if js.URL != "" {
		if u, err := url.Parse(js.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("%w: the URL must be an http(s) URL", ErrInvalid)
		}
	}
	if js.Enabled && js.APIKey == "" {
		return fmt.Errorf("%w: an API key is needed to turn the model on", ErrInvalid)
	}
	return nil
}

// SaveJudge stores judge settings made in the admin. An empty key keeps
// the stored one when the kind and URL stay the same.
func (s *Service) SaveJudge(ctx context.Context, js JudgeSettings) error {
	if js.APIKey == "" {
		if cur, err := s.JudgeSettings(ctx); err == nil && cur.Kind == js.Kind && cur.URL == js.URL {
			js.APIKey = cur.APIKey
		}
	}
	if err := validJudge(js); err != nil {
		return err
	}
	var sealed pgtype.Text
	if js.APIKey != "" {
		v, err := s.box.Seal(js.APIKey)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNoSecrets, err)
		}
		sealed = pgtype.Text{String: v, Valid: true}
	}
	return s.q.PutJudgeSettings(ctx, db.PutJudgeSettingsParams{
		Enabled: js.Enabled, Kind: js.Kind, Url: js.URL, Model: js.Model, ApiKeySealed: sealed, Covers: js.Covers,
	})
}

// UseConfigJudge forgets the admin's judge settings.
func (s *Service) UseConfigJudge(ctx context.Context) error { return s.q.DeleteJudgeSettings(ctx) }

// TestJudge asks the model about a made-up pair, without saving. An empty
// key uses the stored one for the same kind and URL.
func (s *Service) TestJudge(ctx context.Context, js JudgeSettings) (Verdict, error) {
	if js.APIKey == "" {
		if cur, err := s.JudgeSettings(ctx); err == nil && cur.Kind == js.Kind && cur.URL == js.URL {
			js.APIKey = cur.APIKey
		}
	}
	js.Enabled = true
	if err := validJudge(js); err != nil {
		return Verdict{}, err
	}
	j, err := NewJudge(js.config())
	if err != nil {
		return Verdict{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	a := Side{Titles: []TitleText{{Text: "The Lantern Keeper", Language: "en"}}, People: []string{"Hana Imaginary"}, Year: 2012, Type: "manga"}
	b := Side{Titles: []TitleText{{Text: "Lantern Keeper", Language: "en"}, {Text: "Toudai Mori", Language: "ja-Latn"}}, People: []string{"Hana Imaginary"}, Year: 2012, Type: "manga"}
	return j.Judge(ctx, a, b)
}
