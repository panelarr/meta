// SPDX-License-Identifier: GPL-3.0-or-later

// Package jobs runs meta's background jobs: it lists them, starts runs on a
// schedule, at startup or by hand, records every run, and cancels them. It
// follows the jobs contract every Panelarr admin shares.
//
// A job either runs a function here, with its runs in job_runs, or comes
// from a Source that runs and records it itself (provider syncs keep their
// sync_runs).
package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Run statuses.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusSkipped   = "skipped"
)

// Triggers.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerStartup  = "startup"
	// TriggerSystem: meta started the run on its own while running.
	TriggerSystem = "system"
)

// Param types.
const (
	ParamBool       = "bool"
	ParamInt        = "int"
	ParamString     = "string"
	ParamStringList = "string_list"
)

// MaxNotes caps the notes kept per run.
const MaxNotes = 200

// Param is a value RunJob accepts.
type Param struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Default     string
}

// Schedule is when a job runs on its own. With neither Cron nor Interval
// it runs only by hand (or at startup when something asks for it).
type Schedule struct {
	Cron     string
	Interval time.Duration
	// Enabled is false when a schedule is configured but switched off.
	Enabled bool
	// Source is the configuration key it comes from.
	Source string
}

// Def describes a job.
type Def struct {
	// ID is stable and URL-safe: ^[a-z0-9][a-z0-9._-]*$.
	ID          string
	Name        string
	Description string
	Group       string
	Schedule    Schedule
	Params      []Param
	Cancellable bool
}

// Summary is the outline of one run.
type Summary struct {
	ID          string
	JobID       string
	Status      string
	Trigger     string
	StartedAt   time.Time
	FinishedAt  *time.Time
	Message     string
	Counts      map[string]int64
	TriggeredBy string
}

// Note is a warning, error or notable event of a run.
type Note struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Ref     string    `json:"ref,omitempty"`
}

// Progress is what a running run last reported.
type Progress struct {
	Phase string `json:"phase,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

// Run is a run with its details.
type Run struct {
	Summary
	Params   map[string]string
	Notes    []Note
	Progress *Progress
}

// Request is how a run is started.
type Request struct {
	Trigger string
	// By is who started a manual run: "admin", a client's name or "cli".
	By     string
	Params map[string]string
}

// Job is one background job.
type Job interface {
	Def() Def
	// Start begins a run in the background and returns it. The run keeps
	// going after the caller's request; it stops when base ends. A job
	// already running answers a *RunningError.
	Start(base context.Context, req Request) (Summary, error)
	// Current returns the run in progress, in any meta process.
	Current(ctx context.Context) (string, bool, error)
	// Cancel stops a run of this job running in this process.
	Cancel(ctx context.Context, runID string) error
	// Runs lists runs newest first, optionally only those with a status.
	Runs(ctx context.Context, status string, limit, offset int) ([]Summary, error)
}

// Source provides jobs that come from elsewhere, e.g. one per provider.
type Source interface {
	Jobs() []Job
	// FindRun returns a run of one of its jobs by ID.
	FindRun(ctx context.Context, id uuid.UUID) (Run, bool, error)
}

// RunningError is returned when a job is already running.
type RunningError struct {
	JobID string
	RunID string
	// Elsewhere is set when the run is in another meta process.
	Elsewhere bool
}

func (e *RunningError) Error() string {
	where := ""
	if e.Elsewhere {
		where = " in another meta process"
	}
	if e.RunID == "" {
		return fmt.Sprintf("job %s is already running%s", e.JobID, where)
	}
	return fmt.Sprintf("job %s is already running%s (run %s)", e.JobID, where, e.RunID)
}

var (
	// ErrNotFound is returned for unknown jobs and runs.
	ErrNotFound = errors.New("not found")
	// ErrNotCancellable is returned when a job can't stop midway.
	ErrNotCancellable = errors.New("this job can't be cancelled")
	// ErrNotRunning is returned when cancelling a run that isn't running
	// in this process.
	ErrNotRunning = errors.New("the run isn't running in this meta process")
	// ErrInvalidParam is returned for parameters a job doesn't take or
	// values of the wrong type.
	ErrInvalidParam = errors.New("invalid parameter")
)

// Resolve checks params against a job's declared params and fills in
// defaults. Unknown names, missing required ones and values of the wrong
// type are errors wrapping ErrInvalidParam.
func Resolve(def Def, params map[string]string) (map[string]string, error) {
	out := map[string]string{}
	known := map[string]Param{}
	for _, p := range def.Params {
		known[p.Name] = p
	}
	for k, v := range params {
		p, ok := known[k]
		if !ok {
			return nil, fmt.Errorf("%w: job %s takes no %q", ErrInvalidParam, def.ID, k)
		}
		if err := check(p, v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	for _, p := range def.Params {
		if _, ok := out[p.Name]; ok {
			continue
		}
		if p.Required {
			return nil, fmt.Errorf("%w: %q is required", ErrInvalidParam, p.Name)
		}
		if p.Default != "" {
			out[p.Name] = p.Default
		}
	}
	return out, nil
}

func check(p Param, v string) error {
	var err error
	switch p.Type {
	case ParamBool:
		_, err = strconv.ParseBool(v)
	case ParamInt:
		_, err = strconv.ParseInt(v, 10, 64)
	}
	if err != nil {
		return fmt.Errorf("%w: %q must be a %s, not %q", ErrInvalidParam, p.Name, p.Type, v)
	}
	return nil
}

// Bool reads a bool parameter; anything unset or unparsable is false.
func Bool(params map[string]string, name string) bool {
	b, _ := strconv.ParseBool(params[name])
	return b
}

// Int reads an int parameter, or def when unset.
func Int(params map[string]string, name string, def int64) int64 {
	n, err := strconv.ParseInt(params[name], 10, 64)
	if err != nil {
		return def
	}
	return n
}

// List reads a string_list parameter (comma-separated).
func List(params map[string]string, name string) []string {
	var out []string
	for _, s := range strings.Split(params[name], ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
