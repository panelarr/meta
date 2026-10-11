// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/store/db"
)

// Job IDs of the jobs meta runs itself.
const (
	jobRebuildEntries = "rebuild-entries"
	jobStaleRefresh   = "stale-refresh"
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
	}, func(ctx context.Context, r *jobs.Recorder) error {
		return a.refreshStale(ctx, r, jobs.Int(r.Params(), "limit", int64(stale.Limit)))
	})
	return m
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
