// SPDX-License-Identifier: GPL-3.0-or-later

// Package searchsvc runs meta's search backend: PostgreSQL, or an optional
// engine (Typesense, Meilisearch) chosen in the configuration or in the
// admin. It switches backends while meta runs, keeps the engine's index
// fed through the outbox, and rebuilds it when needed.
package searchsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/secret"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Backends.
const (
	Postgres    = "postgres"
	Typesense   = "typesense"
	Meilisearch = "meilisearch"
)

// Settings choose and reach the search backend.
type Settings struct {
	Backend     string
	URL         string
	APIKey      string
	IndexPrefix string
	Browse      string
	// Origin is where they come from: "config" or "admin".
	Origin string
}

// Service owns the search backend.
type Service struct {
	st      *store.Store
	q       *db.Queries
	box     *secret.Box
	pg      *search.Postgres
	w       *catalog.Writer
	cfg     config.Search
	log     *slog.Logger
	metrics *metrics.Metrics
	sw      *search.Switch

	// StartRebuild starts the search-rebuild job with a trigger
	// ("startup" or "system"); set before Start.
	StartRebuild func(trigger, why string)

	starting bool

	mu     sync.Mutex
	base   context.Context
	cur    Settings
	ext    *search.External
	ix     *search.Indexer
	stopIx context.CancelFunc
	ixDone chan struct{}
}

// New returns the service. It serves PostgreSQL until Start.
func New(st *store.Store, box *secret.Box, w *catalog.Writer, cfg config.Search, log *slog.Logger, m *metrics.Metrics) *Service {
	pg := search.NewPostgres(st.Pool)
	return &Service{
		st: st, q: db.New(st.Pool), box: box, pg: pg, w: w, cfg: cfg, log: log, metrics: m,
		sw: search.NewSwitch(search.Instrumented(pg, m)), cur: Settings{Backend: Postgres, Origin: "config"},
	}
}

// Index is the search backend callers query; it follows setting changes.
func (s *Service) Index() *search.Switch { return s.sw }

// Start applies the saved settings (the admin's, else the configuration)
// and keeps the engine fed until base ends.
func (s *Service) Start(base context.Context) error {
	s.mu.Lock()
	s.base, s.starting = base, true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
	}()
	set, err := s.Settings(base)
	if err != nil {
		return err
	}
	return s.apply(base, set)
}

// Stop stops the indexer.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopIndexer()
}

func (s *Service) stopIndexer() {
	if s.stopIx != nil {
		s.stopIx()
		<-s.ixDone
		s.stopIx, s.ixDone = nil, nil
	}
}

// fromConfig returns the configuration's settings.
func (s *Service) fromConfig() Settings {
	set := Settings{Backend: s.cfg.Backend, Browse: s.cfg.Browse, Origin: "config"}
	var e config.Engine
	switch set.Backend {
	case Typesense:
		e = s.cfg.Typesense
	case Meilisearch:
		e = s.cfg.Meilisearch
	}
	set.URL, set.APIKey, set.IndexPrefix = e.URL, e.APIKey, e.IndexPrefix
	return set.withDefaults()
}

func (set Settings) withDefaults() Settings {
	if set.Backend == "" {
		set.Backend = Postgres
	}
	if set.IndexPrefix == "" {
		set.IndexPrefix = "meta"
	}
	if set.Browse == "" {
		set.Browse = search.BrowseFacets
	}
	return set
}

// Settings returns the settings in force: the admin's when saved, else
// the configuration's. The API key is included; callers must not return
// it.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	row, err := s.q.GetSearchSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.fromConfig(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	set := Settings{Backend: row.Backend, URL: row.Url, IndexPrefix: row.IndexPrefix, Browse: row.Browse, Origin: "admin"}
	if row.ApiKeySealed.Valid {
		if set.APIKey, err = s.box.Open(row.ApiKeySealed.String); err != nil {
			return Settings{}, fmt.Errorf("search engine key: %w", err)
		}
	}
	return set.withDefaults(), nil
}

var prefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// Validate checks settings.
func Validate(set Settings) error {
	switch set.Backend {
	case Postgres:
		return nil
	case Typesense, Meilisearch:
	default:
		return fmt.Errorf("unknown search backend %q (use postgres, typesense or meilisearch)", set.Backend)
	}
	u, err := url.Parse(set.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the %s URL must be an http(s) URL, e.g. http://%s:%s", set.Backend, set.Backend, map[string]string{Typesense: "8108", Meilisearch: "7700"}[set.Backend])
	}
	if !prefixRE.MatchString(set.IndexPrefix) {
		return errors.New("the index prefix must be 1–31 lower-case letters, digits, '-' or '_'")
	}
	if !slices.Contains([]string{search.BrowseFacets, search.BrowseEngine, search.BrowsePostgres}, set.Browse) {
		return fmt.Errorf("browse must be facets, engine or postgres, not %q", set.Browse)
	}
	return nil
}

func engineFor(set Settings) search.Engine {
	switch set.Backend {
	case Typesense:
		return search.NewTypesense(set.URL, set.APIKey)
	case Meilisearch:
		return search.NewMeilisearch(set.URL, set.APIKey)
	}
	return nil
}

// Test reaches the engine of set with its key. An empty key uses the one
// in force when the backend and URL are the same.
func (s *Service) Test(ctx context.Context, set Settings) error {
	set = set.withDefaults()
	if set.Backend == Postgres {
		_, err := s.pg.Query(ctx, search.Query{Limit: 1})
		return err
	}
	if err := Validate(set); err != nil {
		return err
	}
	if set.APIKey == "" {
		set.APIKey = s.storedKey(ctx, set)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return engineFor(set).Check(ctx)
}

// storedKey is the key in force for the same engine and URL, if any.
func (s *Service) storedKey(ctx context.Context, set Settings) string {
	cur, err := s.Settings(ctx)
	if err == nil && cur.Backend == set.Backend && cur.URL == set.URL {
		return cur.APIKey
	}
	return ""
}

// Save stores settings made in the admin and switches to them. An empty
// API key keeps the stored one (for the same engine and URL).
func (s *Service) Save(ctx context.Context, set Settings) error {
	set = set.withDefaults()
	if err := Validate(set); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if set.APIKey == "" {
		set.APIKey = s.storedKey(ctx, set)
	}
	var sealed pgtype.Text
	if set.APIKey != "" {
		v, err := s.box.Seal(set.APIKey)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNoSecrets, err)
		}
		sealed = pgtype.Text{String: v, Valid: true}
	}
	if err := s.q.PutSearchSettings(ctx, db.PutSearchSettingsParams{
		Backend: set.Backend, Url: set.URL, ApiKeySealed: sealed, IndexPrefix: set.IndexPrefix, Browse: set.Browse,
	}); err != nil {
		return err
	}
	set.Origin = "admin"
	return s.apply(ctx, set)
}

// UseConfig forgets the admin's settings and goes back to the
// configuration's.
func (s *Service) UseConfig(ctx context.Context) error {
	if err := s.q.DeleteSearchSettings(ctx); err != nil {
		return err
	}
	return s.apply(ctx, s.fromConfig())
}

// apply switches to set: PostgreSQL alone, or an engine fed by the
// outbox. An engine whose index is missing or outdated is rebuilt.
func (s *Service) apply(ctx context.Context, set Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopIndexer()
	s.cur, s.ext, s.ix = set, nil, nil
	if set.Backend == Postgres {
		// Nothing feeds an engine now, so no engine index stays valid.
		s.w.SetSearchOutbox(false)
		s.sw.Set(search.Instrumented(s.pg, s.metrics))
		if err := s.q.ForgetSearchIndexesExcept(ctx, Postgres); err != nil {
			return err
		}
		if err := s.q.ClearSearchOutbox(ctx); err != nil {
			return err
		}
		s.log.Info("search backend", "backend", Postgres, "from", set.Origin)
		return nil
	}
	x := search.NewExternal(engineFor(set), s.pg, s.st.Pool, search.ExternalOptions{
		Prefix: set.IndexPrefix, Browse: set.Browse, Timeout: s.cfg.Timeout, Log: s.log, Metrics: s.metrics,
	})
	s.w.SetSearchOutbox(true)
	if err := s.q.ForgetSearchIndexesExcept(ctx, set.Backend); err != nil {
		return err
	}
	s.ext = x
	s.sw.Set(search.Instrumented(x, s.metrics))
	ready, err := x.Load(ctx)
	if err != nil {
		s.log.Warn("search engine unreachable; PostgreSQL answers until it is back", "backend", set.Backend, "err", err)
	}
	s.ix = search.NewIndexer(x, search.IndexerOptions{
		Batch: s.cfg.Outbox.Batch, Interval: s.cfg.Outbox.Interval, RebuildThreshold: s.cfg.Outbox.RebuildThreshold,
		OnFlood: func() { s.rebuildSoon("system", "the outbox has more changes than outbox.rebuild_threshold, or no index exists") },
		Log:     s.log, Metrics: s.metrics,
	})
	base := s.base
	if base == nil {
		base = context.Background()
	}
	ixCtx, stop := context.WithCancel(base)
	done := make(chan struct{})
	s.stopIx, s.ixDone = stop, done
	go func() {
		defer close(done)
		s.ix.Run(ixCtx)
	}()
	s.log.Info("search backend", "backend", set.Backend, "url", set.URL, "from", set.Origin, "index_ready", ready)
	if err == nil && !ready {
		trigger := "system"
		if s.starting {
			trigger = "startup"
		}
		s.rebuildSoon(trigger, "no current index (first use, or the document schema changed)")
	}
	return nil
}

func (s *Service) rebuildSoon(trigger, why string) {
	if s.StartRebuild != nil {
		go s.StartRebuild(trigger, why)
	}
}

// Current returns the settings in force (with the key) and the engine
// backend, if any.
func (s *Service) Current() (Settings, *search.External, *search.Indexer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.ext, s.ix
}

// Errors the API maps to codes.
var (
	ErrInvalid   = errors.New("invalid search settings")
	ErrNoSecrets = errors.New("secrets unavailable")
)

// ErrNoEngine is returned for engine work while PostgreSQL is the backend.
var ErrNoEngine = errors.New("no search engine is configured: PostgreSQL searches the database directly and needs no index")

// Rebuild rebuilds the engine's index.
func (s *Service) Rebuild(ctx context.Context, progress func(done, total int64)) (int64, error) {
	_, x, _ := s.Current()
	if x == nil {
		return 0, ErrNoEngine
	}
	n, err := x.RebuildWith(ctx, progress)
	if err == nil {
		s.metrics.SearchIndexDocuments(x.Engine().Name(), n)
	}
	return n, err
}

// Drain applies every waiting change now.
func (s *Service) Drain(ctx context.Context, progress func(done int64)) (int64, error) {
	_, _, ix := s.Current()
	if ix == nil {
		return 0, ErrNoEngine
	}
	return ix.Drain(ctx, progress)
}

// Status is the backend's state for the admin.
type Status struct {
	// Settings in force, without the API key.
	Settings     Settings
	KeySet       bool
	Capabilities search.Capabilities
	Engine       *search.Status
	Indexer      *search.IndexerStatus
	Reachable    bool
	ReachErr     string
}

// Status reports the backend, its index and the outbox.
func (s *Service) Status(ctx context.Context) (Status, error) {
	set, x, ix := s.Current()
	st := Status{Settings: set, KeySet: set.APIKey != "", Capabilities: s.sw.Capabilities(), Reachable: true}
	st.Settings.APIKey = ""
	if x == nil {
		return st, nil
	}
	es, err := x.Status(ctx)
	if err != nil {
		return st, err
	}
	st.Engine = &es
	is := ix.Status()
	st.Indexer = &is
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := x.Check(cctx); err != nil {
		st.Reachable, st.ReachErr = false, err.Error()
	}
	return st, nil
}
