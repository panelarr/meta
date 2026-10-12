// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/duplicates"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/searchsvc"
	"github.com/panelarr/meta/internal/store/db"
)

// Job IDs of the jobs meta runs itself.
const (
	jobRebuildEntries = "rebuild-entries"
	jobStaleRefresh   = "stale-refresh"
	jobSearchRebuild  = "search-rebuild"
	jobSearchIndexer  = "search-indexer"
	jobDuplicates     = duplicates.JobID
)

// newJobs registers every background job: a sync job per provider and
// kind, the entry rebuild and the stale-entry refresh.
func (a *app) newJobs() *jobs.Manager {
	m := jobs.New(a.st, a.log.With("component", "jobs"), a.cfg.Jobs.Retention)
	m.SetMetrics(a.metrics)
	a.runner.SetRetention(a.cfg.Jobs.Retention)
	m.AddSource(ingest.NewSyncJobs(a.runner))

	m.Add(jobs.Def{
		ID:   jobRebuildEntries,
		Name: "Rebuild entries",
		Description: "Rebuilds every merged entry from the stored provider records, downloading nothing. " +
			"Runs on its own after an upgrade that changes how entries are merged.",
		Group:       "Maintenance",
		Cancellable: true,
	}, func(ctx context.Context, r *jobs.Recorder) error {
		total, err := a.st.CountActiveEntries(ctx)
		if err != nil {
			return err
		}
		n, err := a.writer.RebuildAll(ctx, 500, func(done int) {
			r.Progress(ctx, "rebuild", int64(done), total, "entries")
		})
		r.Set("entries", int64(n))
		if err != nil {
			return err
		}
		r.Message("rebuilt %d entries", n)
		return a.st.FinishMaintenance(ctx, jobRebuildEntries)
	})

	stale := a.cfg.Jobs.StaleRefresh
	m.Add(jobs.Def{
		ID:   jobStaleRefresh,
		Name: "Refresh stale entries",
		Description: fmt.Sprintf("Looks up again the entries whose provider records are older than %s, oldest first, "+
			"from providers that can look records up. Dump-only providers are left to their syncs.", age(a.cfg.Ingest.StaleAfter)),
		Group: "Maintenance",
		Schedule: jobs.Schedule{
			Cron: stale.Schedule, Enabled: stale.Schedule != "", Source: "jobs.stale_refresh.schedule",
		},
		Params: []jobs.Param{{
			Name: "limit", Type: jobs.ParamInt, Default: fmt.Sprint(stale.Limit),
			Description: "Most entries to refresh in this run",
		}},
		Cancellable: true,
		Disabled:    a.staleRefreshDisabled,
	}, func(ctx context.Context, r *jobs.Recorder) error {
		return a.refreshStale(ctx, r, jobs.Int(r.Params(), "limit", int64(stale.Limit)))
	})
	m.Add(jobs.Def{
		ID:   jobSearchRebuild,
		Name: "Rebuild the search index",
		Description: "Builds a new index in the search engine from every active entry and swaps it in at once; searches use " +
			"the old index until then. Runs on its own on first use, when the document schema changes and when too many " +
			"changes pile up. Nothing to do with PostgreSQL alone.",
		Group:       "Search",
		Cancellable: true,
		Disabled:    a.searchJobDisabled,
	}, func(ctx context.Context, r *jobs.Recorder) error {
		n, err := a.search.Rebuild(ctx, func(done, total int64) {
			r.Progress(ctx, "index", done, total, "entries")
		})
		if errors.Is(err, searchsvc.ErrNoEngine) {
			r.Skip(err.Error())
			return nil
		}
		r.Set("documents", n)
		if err != nil {
			return err
		}
		r.Message("indexed %d entries", n)
		return nil
	})
	m.Add(jobs.Def{
		ID:   jobSearchIndexer,
		Name: "Apply waiting search changes",
		Description: "Sends every entry change waiting in the outbox to the search engine now. The indexer does this on its " +
			"own every second; run it after the engine was down to see it catch up.",
		Group:       "Search",
		Cancellable: true,
		Disabled:    a.searchJobDisabled,
	}, func(ctx context.Context, r *jobs.Recorder) error {
		n, err := a.search.Drain(ctx, func(done int64) { r.Progress(ctx, "apply", done, 0, "changes") })
		if errors.Is(err, searchsvc.ErrNoEngine) {
			r.Skip(err.Error())
			return nil
		}
		r.Set("changes", n)
		if err != nil {
			return err
		}
		r.Message("applied %d changes", n)
		return nil
	})
	m.Add(jobs.Def{
		ID:   jobDuplicates,
		Name: "Find duplicates",
		Description: "Compares the entries that changed since the last run with likely partners (equal title keys, " +
			"similar titles, IDs at other sites in common) and keeps the pairs worth a look for a curator, under Duplicates. " +
			"With a decision model on, it also rates the best pairs. Nothing is merged without a curator.",
		Group: "Catalogue",
		Schedule: jobs.Schedule{
			Cron: a.cfg.Duplicates.Schedule, Enabled: a.cfg.Duplicates.Schedule != "", Source: "duplicates.schedule",
		},
		Params: []jobs.Param{
			{Name: "full", Type: jobs.ParamBool, Default: "false", Description: "Compare every entry, not only those changed since the last run"},
			{Name: "judge", Type: jobs.ParamBool, Default: "true", Description: "Let the decision model rate the best pairs, when one is on"},
		},
		Cancellable: true,
	}, func(ctx context.Context, r *jobs.Recorder) error {
		return a.dups.Run(ctx, r, jobs.Bool(r.Params(), "full"), jobs.Bool(r.Params(), "judge"))
	})
	return m
}

// searchJobDisabled: the search index jobs only do something with a search
// engine.
func (a *app) searchJobDisabled(context.Context) string {
	if _, x, ix := a.search.Current(); x == nil || ix == nil {
		return "Only with a search engine (Typesense or Meilisearch): PostgreSQL searches the database directly and needs no index."
	}
	return ""
}

// staleRefreshDisabled: the refresh needs a provider that can look records
// up. A provider meta hasn't reached yet may be one, so it doesn't disable
// the job; a run then skips if none can.
func (a *app) staleRefreshDisabled(context.Context) string {
	for _, p := range a.host.List() {
		if can, known := p.KnownLookup(); can || !known {
			return ""
		}
	}
	return "No provider can look records up: dump-only providers are kept current by their syncs."
}

func (a *app) refreshStale(ctx context.Context, r *jobs.Recorder, limit int64) error {
	var providers []string
	for _, p := range a.host.List() {
		if p.CanLookup(ctx) {
			providers = append(providers, p.ID())
		}
	}
	if len(providers) == 0 {
		r.Skip("no provider can look records up")
		return nil
	}
	if limit <= 0 || limit > 100000 {
		return fmt.Errorf("%w: limit must be 1..100000", jobs.ErrInvalidParam)
	}
	stale, err := a.st.ListStaleEntries(ctx, db.ListStaleEntriesParams{
		Providers: providers, Before: time.Now().Add(-a.cfg.Ingest.StaleAfter), Lim: int32(limit),
	})
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		r.Message("nothing is stale")
		return nil
	}
	for i, e := range stale {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := api.Refresh(ctx, a.st, a.host, a.writer, e.EntryID)
		if err != nil {
			r.Add("failed", 1)
			r.Note("warning", err.Error(), e.EntryID.String())
		} else {
			r.Add("refreshed", int64(len(res.GetRefreshed())))
			for provider, why := range res.GetFailed() {
				r.Add("failed", 1)
				r.Note("warning", provider+": "+why, e.EntryID.String())
			}
		}
		r.Set("entries", int64(i+1))
		r.Progress(ctx, "refresh", int64(i+1), int64(len(stale)), "entries")
	}
	r.Message("looked up %d stale entries", len(stale))
	return nil
}

// age is a duration for people: "7 days", or Go's form ("90m0s").
func age(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d == day:
		return "a day"
	case d > 0 && d%day == 0:
		return fmt.Sprintf("%d days", d/day)
	}
	return d.String()
}
