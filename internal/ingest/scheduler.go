// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/panelarr/meta/internal/config"
)

// Scheduler starts syncs on the schedules in the provider configuration.
// Times are UTC.
type Scheduler struct {
	c   *cron.Cron
	log *slog.Logger
}

// NewScheduler registers every provider schedule with the runner.
func NewScheduler(ctx context.Context, cfg config.Config, r *Runner, log *slog.Logger) (*Scheduler, error) {
	c := cron.New(cron.WithParser(config.CronParser), cron.WithLocation(time.UTC))
	for _, p := range cfg.Providers {
		if p.Disabled {
			continue
		}
		for kind, spec := range p.Schedules {
			provider, kind := p.ID, kind
			if _, err := c.AddFunc(spec, func() {
				_, err := r.Start(ctx, provider, kind, TriggerSchedule, false)
				switch {
				case errors.Is(err, ErrRunning):
					log.Info("scheduled sync skipped: one is still running", "provider", provider, "kind", kind)
				case err != nil:
					log.Error("scheduled sync did not start", "provider", provider, "kind", kind, "err", err)
				}
			}); err != nil {
				return nil, err
			}
			log.Info("sync scheduled", "provider", provider, "kind", kind, "schedule", spec)
		}
	}
	return &Scheduler{c: c, log: log}, nil
}

// Start runs the schedules until Stop.
func (s *Scheduler) Start() { s.c.Start() }

// Stop stops scheduling; running syncs continue until their context ends.
func (s *Scheduler) Stop() { <-s.c.Stop().Done() }

// Next returns the next scheduled time of each entry, for display.
func (s *Scheduler) Next() []time.Time {
	var out []time.Time
	for _, e := range s.c.Entries() {
		out = append(out, e.Next)
	}
	return out
}
