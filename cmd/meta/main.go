// SPDX-License-Identifier: GPL-3.0-or-later

// Command meta runs the Panelarr metadata service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/buildinfo"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/duplicates"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/integrations"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/searchsvc"
	"github.com/panelarr/meta/internal/secret"
	"github.com/panelarr/meta/internal/store"
)

const usage = `meta: the Panelarr metadata service

Usage:
  meta serve                          run the service (API, plugins, scheduled syncs)
  meta sync PROVIDER [KIND] [--force] run one sync in the foreground and print the result
  meta migrate [up|down|status]       apply, roll back or list database migrations
  meta healthcheck [--url URL]        probe a running meta (for container health checks)
  meta admin-token reset              generate a new admin token (when META_ADMIN_TOKEN isn't set)
  meta rebuild                        rebuild every entry from the stored provider records
  meta version                        print the version

Flags (all commands):
  --config FILE         configuration file (env META_CONFIG)
  --database-url URL    PostgreSQL URL (env META_DATABASE_URL)

See docs/configuration.md for every setting.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "meta:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "version":
		_, err := fmt.Fprintln(out, buildinfo.Version)
		return err
	case "migrate":
		return migrate(ctx, args[1:], out)
	case "serve":
		return serve(ctx, args[1:], out)
	case "sync":
		return syncOnce(ctx, args[1:], out)
	case "rebuild":
		return rebuild(ctx, args[1:], out)
	case "admin-token":
		return adminToken(ctx, args[1:], out)
	case "healthcheck":
		if code := runHealthcheck(args[1:], os.Stderr); code != 0 {
			return fmt.Errorf("unhealthy (exit %d)", code)
		}
		return nil
	default:
		return errUsage
	}
}

type common struct {
	configPath  string
	databaseURL string
}

func (c *common) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "configuration file (env META_CONFIG)")
	fs.StringVar(&c.databaseURL, "database-url", "", "PostgreSQL URL (env META_DATABASE_URL)")
}

func (c *common) load() (config.Config, error) {
	// Flags win over the file and the environment.
	return config.LoadWith(c.configPath, func(cfg *config.Config) {
		if c.databaseURL != "" {
			cfg.DatabaseURL = c.databaseURL
		}
	})
}

// parse parses flags that may come before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newLogger(cfg config.Log, w io.Writer) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Format, "text") {
		return slog.New(slog.NewTextHandler(w, opts)).With("service", "meta")
	}
	return slog.New(slog.NewJSONHandler(w, opts)).With("service", "meta")
}

func migrate(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.flags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	cmd := "up"
	if len(pos) > 0 {
		cmd = pos[0]
	}
	if cmd != "up" && cmd != "down" && cmd != "status" {
		return errUsage
	}
	cfg, err := c.load()
	if err != nil && cfg.DatabaseURL == "" {
		return err
	}
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	switch cmd {
	case "up":
		res, err := s.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(res) == 0 {
			_, err = fmt.Fprintln(out, "database is up to date")
			return err
		}
		for _, r := range res {
			if _, err := fmt.Fprintf(out, "applied %d %s\n", r.Version, r.Name); err != nil {
				return err
			}
		}
	case "down":
		res, err := s.MigrateDown(ctx)
		if err != nil {
			return err
		}
		for _, r := range res {
			if _, err := fmt.Fprintf(out, "rolled back %d %s\n", r.Version, r.Name); err != nil {
				return err
			}
		}
	case "status":
		st, err := s.Status(ctx)
		if err != nil {
			return err
		}
		for _, m := range st {
			state := "pending"
			if m.Applied {
				state = "applied"
			}
			if _, err := fmt.Fprintf(out, "%-8s %d %s\n", state, m.Version, m.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// app is everything serve and sync share.
type app struct {
	cfg     config.Config
	log     *slog.Logger
	st      *store.Store
	host    *host.Host
	writer  *catalog.Writer
	runner  *ingest.Runner
	integ   *integrations.Service
	metrics *metrics.Metrics
	search  *searchsvc.Service
	dups    *duplicates.Service
}

func open(ctx context.Context, c common, logOut io.Writer) (*app, error) {
	cfg, err := c.load()
	if err != nil {
		return nil, err
	}
	log := newLogger(cfg.Log, logOut)
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	applied, err := st.Migrate(ctx)
	if err != nil {
		st.Close()
		return nil, err
	}
	for _, m := range applied {
		log.Info("migration applied", "version", m.Version, "name", m.Name)
	}
	if n, err := st.FailInterruptedSyncRuns(ctx); err != nil {
		st.Close()
		return nil, err
	} else if n > 0 {
		log.Warn("marked interrupted sync runs as failed", "runs", n)
	}
	m := metrics.New()
	h := host.New(cfg, st, log.With("component", "host"), nil)
	h.SetMetrics(m)
	w := catalog.NewWriter(st, catalog.Options{
		Priority:        catalog.Priority{PerField: cfg.Merge.Priority, Fallback: cfg.ProviderOrder()},
		DisplayLanguage: cfg.Merge.DisplayLanguage,
	})
	r := ingest.NewRunner(st, h, w, log.With("component", "ingest"), cfg.Ingest.BatchSize)
	r.SetMetrics(m)
	// Plugins get their network profiles from the integrations, in serve
	// and in one-off syncs alike.
	box := secret.NewBox(cfg.SecretKey, filepath.Join(cfg.DataDir, "secret.key"))
	integ := integrations.New(st, box, func() []integrations.ProviderInfo {
		var out []integrations.ProviderInfo
		for _, p := range h.List() {
			out = append(out, integrations.ProviderInfo{ID: p.ID(), Supported: p.UsesNetworkProfiles()})
		}
		return out
	})
	h.SetProfileSource(func(ctx context.Context, provider string) (*metadatav1.NetworkProfile, error) {
		np, _, err := integ.Effective(ctx, provider)
		return np, err
	})
	ss := searchsvc.New(st, box, w, cfg.Search, log.With("component", "search"), m)
	// With a search engine in use, every process records entry changes
	// for it, one-off syncs and rebuilds included.
	if set, err := ss.Settings(ctx); err != nil {
		log.Warn("search settings", "err", err)
	} else {
		w.SetSearchOutbox(set.Backend != searchsvc.Postgres)
	}
	dups := duplicates.NewService(st, w, box, cfg.Duplicates)
	return &app{cfg: cfg, log: log, st: st, host: h, writer: w, runner: r, integ: integ, metrics: m, search: ss, dups: dups}, nil
}

func (a *app) close() {
	a.host.Close()
	a.st.Close()
}

func serve(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.flags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := open(ctx, c, os.Stderr)
	if err != nil {
		return err
	}
	defer a.close()
	log := a.log

	if a.host.Empty() {
		log.Warn("no providers configured: the catalogue stays empty until you add a metadata plugin")
	}
	// Syncs started by the scheduler or the API stop when meta stops.
	syncCtx, stopSyncs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSyncs()
	a.integ.OnChange = func() { go a.host.PushProfiles(syncCtx) }
	if unused, err := a.integ.UnusedSolvers(ctx); err == nil && len(unused) > 0 {
		log.Warn("solvers not used by any network profile, so no provider uses them; add them to a profile", "solvers", unused)
	}
	authn, err := auth.New(ctx, a.st, auth.Options{AdminToken: a.cfg.AdminToken, ConfigKeys: a.cfg.APIKeys, Out: out, Log: log})
	if err != nil {
		return err
	}
	a.host.Start(syncCtx, time.Minute)
	jm := a.newJobs()
	if err := jm.Start(syncCtx); err != nil {
		return err
	}
	// One-off jobs an upgrade queued (e.g. rebuilding entries) run in the
	// background; the API serves meanwhile.
	a.startMaintenance(syncCtx, jm)

	a.search.StartRebuild = func(trigger, why string) {
		if _, err := jm.RunJob(syncCtx, jobSearchRebuild, jobs.Request{Trigger: trigger}); err != nil {
			log.Info("search rebuild not started", "why", why, "err", err)
		} else {
			log.Info("search rebuild started", "why", why)
		}
	}
	if err := a.search.Start(syncCtx); err != nil {
		return err
	}
	defer a.search.Stop()
	idx := a.search.Index()
	admin := api.NewAdmin(syncCtx, a.st, a.host, a.runner, a.writer)
	admin.SetIndex(idx)
	apiOpts := api.Options{Auth: authn, Ready: func(ctx context.Context) error { return a.st.Pool.Ping(ctx) }, Log: log.With("component", "api"), Metrics: a.metrics}
	var metricsSrv *http.Server
	if a.cfg.Metrics.Enabled {
		handler := promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{})
		if a.cfg.Metrics.Listen == "" {
			apiOpts.MetricsHandler = handler
			log.Info("metrics served on the API port at /metrics (metrics:read scope)")
		} else {
			mux := http.NewServeMux()
			mux.Handle("GET /metrics", handler)
			metricsSrv = &http.Server{Addr: a.cfg.Metrics.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			mln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", a.cfg.Metrics.Listen)
			if err != nil {
				return fmt.Errorf("metrics listener: %w", err)
			}
			log.Info("metrics listening", "addr", mln.Addr().String())
			go func() {
				if err := metricsSrv.Serve(mln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("metrics server", "err", err)
				}
			}()
		}
	}
	integAPI := api.NewIntegrations(a.integ)
	integAPI.SetSearch(a.search)
	metaAPI := api.NewMeta(a.st, a.host, a.writer, api.MetaOptions{
		DisplayLanguage: a.cfg.Merge.DisplayLanguage, StaleAfter: a.cfg.Ingest.StaleAfter,
		Index: idx, Log: log, Base: syncCtx,
	})
	srv, err := api.New(api.Services{
		Meta:         metaAPI,
		Duplicates:   api.NewDuplicates(a.st, a.dups, metaAPI),
		Admin:        admin,
		Clients:      api.NewClients(a.st),
		Integrations: integAPI,
		Jobs:         api.NewJobs(syncCtx, jm),
	}, apiOpts)
	if err != nil {
		return err
	}
	httpSrv := srv.HTTPServer(a.cfg.Listen)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", a.cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("meta listening", "addr", ln.Addr().String(), "version", buildinfo.Version, "providers", len(a.host.List()))
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	jm.Stop()
	stopSyncs()
	a.runner.Wait()
	jm.Wait()
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(shutdownCtx)
	}
	if serr := httpSrv.Shutdown(shutdownCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func syncOnce(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.flags(fs)
	force := fs.Bool("force", false, "send every record even if the provider reports no changes")
	pos, err := parse(fs, args)
	if err != nil || len(pos) == 0 || len(pos) > 2 {
		return errUsage
	}
	a, err := open(ctx, c, os.Stderr)
	if err != nil {
		return err
	}
	defer a.close()
	kind := ""
	if len(pos) == 2 {
		kind = pos[1]
	}
	start := time.Now()
	run, err := a.runner.Run(ctx, pos[0], kind, ingest.TriggerManual, *force)
	if run.ID.String() != "00000000-0000-0000-0000-000000000000" {
		var entries int64
		_ = a.st.Pool.QueryRow(context.WithoutCancel(ctx), `SELECT count(*) FROM entries WHERE state = 'active'`).Scan(&entries)
		var size string
		_ = a.st.Pool.QueryRow(context.WithoutCancel(ctx), `SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&size)
		fmt.Fprintf(out, "run %s: %s in %s\n", run.ID, run.Status, time.Since(start).Round(time.Millisecond))
		fmt.Fprintf(out, "  records %d (changed %d), merges %d, removals %d, skipped %d, marked removed %d\n",
			run.Upserts, run.Changed, run.Merges, run.Removals, run.Skipped, run.MarkedRemoved)
		fmt.Fprintf(out, "  active entries %d, database size %s\n", entries, size)
		if run.Error != "" {
			fmt.Fprintf(out, "  error: %s\n", run.Error)
		}
	}
	return err
}

func adminToken(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("admin-token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.flags(fs)
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 || pos[0] != "reset" {
		return errUsage
	}
	cfg, err := c.load()
	if err != nil && cfg.DatabaseURL == "" {
		return err
	}
	if cfg.AdminToken != "" {
		return errors.New("META_ADMIN_TOKEN (or admin_token) is set and wins over a stored token: change it there instead")
	}
	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.Migrate(ctx); err != nil {
		return err
	}
	token, err := auth.ResetAdminToken(ctx, s)
	if err != nil {
		return err
	}
	auth.PrintAdminToken(out, token)
	_, err = fmt.Fprintln(out, "Restart meta for the new token to take effect.")
	return err
}

func rebuild(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.flags(fs)
	if pos, err := parse(fs, args); err != nil || len(pos) != 0 {
		return errUsage
	}
	a, err := open(ctx, c, os.Stderr)
	if err != nil {
		return err
	}
	defer a.close()
	start := time.Now()
	run, err := a.newJobs().RunAndWait(ctx, jobRebuildEntries, jobs.Request{Trigger: jobs.TriggerManual, By: "cli"})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s in %s\n", run.Message, time.Since(start).Round(time.Second))
	return err
}

// startMaintenance starts the jobs an upgrade queued in maintenance_jobs.
func (a *app) startMaintenance(ctx context.Context, jm *jobs.Manager) {
	pending, err := a.st.PendingMaintenance(ctx)
	if err != nil {
		a.log.Error("maintenance", "err", err)
		return
	}
	for _, p := range pending {
		if _, ok := jm.Job(p.Name); !ok {
			a.log.Warn("unknown maintenance job; leaving it", "job", p.Name)
			continue
		}
		a.log.Info("maintenance job queued by an upgrade", "job", p.Name, "reason", p.Reason)
		if _, err := jm.RunJob(ctx, p.Name, jobs.Request{Trigger: jobs.TriggerStartup}); err != nil {
			a.log.Info("maintenance job not started", "job", p.Name, "err", err)
		}
	}
}
