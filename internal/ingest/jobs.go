// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/sdk/plugin"
)

// SyncJobs shows every provider's syncs as jobs, one per provider and
// ingestion kind ("sync.<provider>.<kind>"), recorded in sync_runs.
type SyncJobs struct {
	r *Runner
}

// NewSyncJobs returns the sync jobs of the runner's providers.
func NewSyncJobs(r *Runner) *SyncJobs { return &SyncJobs{r: r} }

// Jobs lists a job per provider and kind: the kinds it has a schedule for
// and the sync kinds the plugin reported (dump when it wasn't reached yet).
func (s *SyncJobs) Jobs() []jobs.Job {
	var out []jobs.Job
	for _, p := range s.r.host.List() {
		var kinds []string
		for _, k := range []string{plugin.KindDump, plugin.KindDelta} {
			if _, scheduled := p.Config().Schedules[k]; scheduled || p.Supports(k) {
				kinds = append(kinds, k)
			}
		}
		for k := range p.Config().Schedules {
			if !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) == 0 {
			kinds = []string{plugin.KindDump}
		}
		for _, k := range kinds {
			out = append(out, &syncJob{r: s.r, provider: p.ID(), kind: k, schedule: p.Config().Schedules[k]})
		}
	}
	return out
}

// FindRun returns a sync run as a job run.
func (s *SyncJobs) FindRun(ctx context.Context, id uuid.UUID) (jobs.Run, bool, error) {
	run, err := s.r.st.GetSyncRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Run{}, false, nil
	}
	if err != nil {
		return jobs.Run{}, false, err
	}
	return JobRun(run), true, nil
}

type syncJob struct {
	r        *Runner
	provider string
	kind     string
	schedule string
}

func (j *syncJob) Def() jobs.Def {
	desc := "Reads the provider's full dump and stores every record."
	if j.kind == plugin.KindDelta {
		desc = "Reads what changed at the provider since the last sync."
	}
	return jobs.Def{
		ID:          JobID(j.provider, j.kind),
		Name:        fmt.Sprintf("Sync %s (%s)", j.provider, j.kind),
		Description: desc + " One sync per provider runs at a time.",
		Group:       "Providers",
		Schedule: jobs.Schedule{
			Cron: j.schedule, Enabled: j.schedule != "",
			Source: fmt.Sprintf("providers[%s].schedules.%s", j.provider, j.kind),
		},
		Params: []jobs.Param{{
			Name: "force", Type: jobs.ParamBool, Default: "false",
			Description: "Read everything even if the provider reports nothing changed",
		}},
		Cancellable: true,
	}
}

func (j *syncJob) Start(base context.Context, req jobs.Request) (jobs.Summary, error) {
	run, err := j.r.StartBy(base, j.provider, j.kind, req.Trigger, req.By, jobs.Bool(req.Params, "force"))
	if errors.Is(err, ErrRunning) {
		busy := &jobs.RunningError{JobID: JobID(j.provider, j.kind), Elsewhere: strings.Contains(err.Error(), "another meta process")}
		if id, ok := j.r.Running(j.provider); ok {
			busy.RunID = id.String()
		} else if cur, err := j.r.st.RunningSyncRun(base, j.provider); err == nil {
			busy.RunID = cur.ID.String()
		}
		return jobs.Summary{}, busy
	}
	if err != nil {
		return jobs.Summary{}, err
	}
	return JobRun(run).Summary, nil
}

// Current returns the provider's running sync of this kind, in any meta
// process.
func (j *syncJob) Current(ctx context.Context) (string, bool, error) {
	run, err := j.r.st.RunningSyncRun(ctx, j.provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if run.Kind != j.kind {
		return "", false, nil
	}
	return run.ID.String(), true, nil
}

func (j *syncJob) Cancel(_ context.Context, runID string) error {
	id, err := uuid.Parse(runID)
	if err != nil {
		return jobs.ErrNotFound
	}
	if err := j.r.Cancel(id); errors.Is(err, ErrNotRunning) {
		return jobs.ErrNotRunning
	} else if err != nil {
		return err
	}
	return nil
}

func (j *syncJob) Runs(ctx context.Context, status string, limit, offset int) ([]jobs.Summary, error) {
	p := db.ListSyncRunsOfKindParams{Provider: j.provider, Kind: j.kind, Lim: int32(limit), Off: int32(offset)} //nolint:gosec // page sizes are small
	if status != "" {
		p.Status = pgtype.Text{String: status, Valid: true}
	}
	rows, err := j.r.st.ListSyncRunsOfKind(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]jobs.Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, JobRun(r).Summary)
	}
	return out, nil
}

// JobRun maps a sync run onto the shared job run shape.
func JobRun(r db.SyncRun) jobs.Run {
	counts := map[string]int64{
		"upserts": r.Upserts, "changed": r.Changed, "removals": r.Removals,
		"merges": r.Merges, "skipped": r.Skipped, "marked_removed": r.MarkedRemoved,
	}
	msg := r.Error
	if msg == "" && r.Status == jobs.StatusSucceeded {
		msg = fmt.Sprintf("%d records, %d changed", r.Upserts, r.Changed)
		if r.MarkedRemoved > 0 {
			msg += fmt.Sprintf(", %d marked removed", r.MarkedRemoved)
		}
	}
	run := jobs.Run{
		Summary: jobs.Summary{
			ID: r.ID.String(), JobID: JobID(r.Provider, r.Kind), Status: r.Status, Trigger: r.Trigger,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Message: msg, Counts: counts, TriggeredBy: r.TriggeredBy,
		},
		Params: map[string]string{"force": fmt.Sprint(r.Force)},
	}
	var notices []notice
	_ = json.Unmarshal(r.Notices, &notices)
	for _, n := range notices {
		run.Notes = append(run.Notes, jobs.Note{At: n.At, Level: noteLevel(n.Level), Message: n.Message, Ref: n.Ref})
	}
	if r.Status == jobs.StatusRunning && len(r.Progress) > 0 {
		var p metadatav1.SyncProgress
		if protojson.Unmarshal(r.Progress, &p) == nil {
			run.Progress = &jobs.Progress{Phase: p.GetPhase(), Done: p.GetDone(), Total: p.GetTotal(), Unit: p.GetUnit()}
		}
	}
	return run
}

// noteLevel turns a plugin notice level ("NOTICE_LEVEL_WARNING" or
// "warning") into a job note level.
func noteLevel(l string) string {
	l = strings.ToLower(strings.TrimPrefix(l, "NOTICE_LEVEL_"))
	switch l {
	case "warning", "error":
		return l
	}
	return "info"
}
