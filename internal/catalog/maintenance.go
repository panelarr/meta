// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"context"
	"log/slog"
	"time"
)

// RunMaintenance runs the one-off jobs an upgrade queued (see the
// maintenance_jobs table), one meta process at a time. It returns the
// number of jobs that ran.
func (w *Writer) RunMaintenance(ctx context.Context, log *slog.Logger) (int, error) {
	jobs, err := w.st.PendingMaintenance(ctx)
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	conn, err := w.st.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('meta-maintenance'))`).Scan(&locked); err != nil || !locked {
		return 0, err // another meta process is on it
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('meta-maintenance'))`)
	}()
	ran := 0
	for _, j := range jobs {
		start := time.Now()
		log.Info("maintenance job started", "job", j.Name, "reason", j.Reason)
		switch j.Name {
		case "rebuild-entries":
			n, err := w.RebuildAll(ctx, 500, func(done int) {
				if done%20000 == 0 {
					log.Info("rebuilding entries", "done", done)
				}
			})
			if err != nil {
				return ran, err
			}
			log.Info("maintenance job finished", "job", j.Name, "entries", n, "took", time.Since(start).Round(time.Second))
		default:
			log.Warn("unknown maintenance job; leaving it", "job", j.Name)
			continue
		}
		if err := w.st.FinishMaintenance(ctx, j.Name); err != nil {
			return ran, err
		}
		ran++
	}
	return ran, nil
}
