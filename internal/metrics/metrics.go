// SPDX-License-Identifier: GPL-3.0-or-later

// Package metrics defines meta's Prometheus metrics. Labels stay bounded:
// provider IDs, ingestion kinds, operations, procedures and status codes,
// never URLs, titles or credentials.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds every collector meta updates.
type Metrics struct {
	Registry *prometheus.Registry

	SyncRuns       *prometheus.CounterVec
	SyncRecords    *prometheus.CounterVec
	SyncDuration   *prometheus.HistogramVec
	SyncRunning    *prometheus.GaugeVec
	PluginCalls    *prometheus.CounterVec
	PluginDuration *prometheus.HistogramVec
	PluginUp       *prometheus.GaugeVec
	SearchDuration *prometheus.HistogramVec
	APIRequests    *prometheus.CounterVec
	APIDuration    *prometheus.HistogramVec
	EntriesChanged *prometheus.CounterVec
	JobRuns        *prometheus.CounterVec
	JobDuration    *prometheus.HistogramVec
	JobRunning     *prometheus.GaugeVec

	SearchFallbacks     *prometheus.CounterVec
	SearchOutboxPending prometheus.Gauge
	SearchOutboxLag     prometheus.Gauge
	SearchDocuments     *prometheus.GaugeVec
}

// New registers meta's metrics, plus the Go runtime and process ones, on a
// fresh registry.
func New() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := func(c prometheus.Collector) prometheus.Collector { r.MustRegister(c); return c }
	m := &Metrics{Registry: r}
	m.SyncRuns = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_sync_runs_total", Help: "Finished sync runs by provider, ingestion kind and outcome.",
	}, []string{"provider", "kind", "status"})).(*prometheus.CounterVec)
	m.SyncRecords = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_sync_records_total", Help: "Sync events received by provider and event (upsert, changed, merge, removal, skipped, marked_removed).",
	}, []string{"provider", "event"})).(*prometheus.CounterVec)
	m.SyncDuration = f(prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "meta_sync_duration_seconds", Help: "Duration of sync runs.",
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 7200},
	}, []string{"provider", "kind"})).(*prometheus.HistogramVec)
	m.SyncRunning = f(prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "meta_sync_running", Help: "1 while a sync of the provider is running.",
	}, []string{"provider"})).(*prometheus.GaugeVec)
	m.PluginCalls = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_plugin_calls_total", Help: "Calls to metadata plugins by provider, operation and status code.",
	}, []string{"provider", "operation", "code"})).(*prometheus.CounterVec)
	m.PluginDuration = f(prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "meta_plugin_call_duration_seconds", Help: "Duration of plugin calls (for sync: until the stream opens).",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "operation"})).(*prometheus.HistogramVec)
	m.PluginUp = f(prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "meta_plugin_up", Help: "1 when the plugin is connected and healthy, 0.5 when degraded, 0 otherwise.",
	}, []string{"provider"})).(*prometheus.GaugeVec)
	m.SearchDuration = f(prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "meta_search_duration_seconds", Help: "Search and browse latency by backend.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"backend", "kind"})).(*prometheus.HistogramVec)
	m.APIRequests = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_api_requests_total", Help: "API calls by procedure and status code.",
	}, []string{"procedure", "code"})).(*prometheus.CounterVec)
	m.APIDuration = f(prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "meta_api_request_duration_seconds", Help: "API call latency by procedure.",
		Buckets: prometheus.DefBuckets,
	}, []string{"procedure"})).(*prometheus.HistogramVec)
	m.EntriesChanged = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_entries_changed_total", Help: "Merged entries whose content or state changed, by provider of the change.",
	}, []string{"provider"})).(*prometheus.CounterVec)
	m.JobRuns = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_job_runs_total", Help: "Finished background job runs by job, outcome and trigger.",
	}, []string{"job", "status", "trigger"})).(*prometheus.CounterVec)
	m.JobDuration = f(prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "meta_job_duration_seconds", Help: "Duration of background job runs.",
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 7200},
	}, []string{"job"})).(*prometheus.HistogramVec)
	m.JobRunning = f(prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "meta_job_running", Help: "1 while a run of the job is going on in this process.",
	}, []string{"job"})).(*prometheus.GaugeVec)
	m.SearchFallbacks = f(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "meta_search_fallbacks_total", Help: "Queries a search engine failed and PostgreSQL answered instead.",
	}, []string{"backend"})).(*prometheus.CounterVec)
	m.SearchOutboxPending = f(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "meta_search_outbox_pending", Help: "Entry changes waiting for the search engine.",
	})).(prometheus.Gauge)
	m.SearchOutboxLag = f(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "meta_search_outbox_lag_seconds", Help: "Age of the oldest entry change waiting for the search engine.",
	})).(prometheus.Gauge)
	m.SearchDocuments = f(prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "meta_search_index_documents", Help: "Documents in the search engine's live index after the last rebuild.",
	}, []string{"backend"})).(*prometheus.GaugeVec)
	return m
}

// SearchFallback counts a query PostgreSQL answered for a failed engine.
// Safe on a nil *Metrics.
func (m *Metrics) SearchFallback(backend string) {
	if m == nil {
		return
	}
	m.SearchFallbacks.WithLabelValues(backend).Inc()
}

// SearchIndexDocuments records the documents of a freshly built index.
func (m *Metrics) SearchIndexDocuments(backend string, n int64) {
	if m == nil {
		return
	}
	m.SearchDocuments.WithLabelValues(backend).Set(float64(n))
}

// SearchOutbox records the outbox backlog.
func (m *Metrics) SearchOutbox(pending int64, lag time.Duration) {
	if m == nil {
		return
	}
	m.SearchOutboxPending.Set(float64(pending))
	m.SearchOutboxLag.Set(lag.Seconds())
}

// JobStarted marks a job as running. Safe on a nil *Metrics.
func (m *Metrics) JobStarted(job string) {
	if m == nil {
		return
	}
	m.JobRunning.WithLabelValues(job).Set(1)
}

// JobFinished records a finished (or skipped) run.
func (m *Metrics) JobFinished(job, status, trigger string, d time.Duration) {
	if m == nil {
		return
	}
	if status != "skipped" {
		m.JobRunning.WithLabelValues(job).Set(0)
		m.JobDuration.WithLabelValues(job).Observe(d.Seconds())
	}
	m.JobRuns.WithLabelValues(job, status, trigger).Inc()
}

// Code returns the status code name of err for labels ("ok" for nil).
func Code(err error) string {
	if err == nil {
		return "ok"
	}
	return codeOf(err)
}

// PluginCall records one plugin call. Safe on a nil *Metrics.
func (m *Metrics) PluginCall(provider, op string, start time.Time, err error) {
	if m == nil {
		return
	}
	m.PluginCalls.WithLabelValues(provider, op, Code(err)).Inc()
	m.PluginDuration.WithLabelValues(provider, op).Observe(time.Since(start).Seconds())
}

// PluginHealth records a plugin's health: 1 ok, 0.5 degraded, 0 down.
func (m *Metrics) PluginHealth(provider string, v float64) {
	if m == nil {
		return
	}
	m.PluginUp.WithLabelValues(provider).Set(v)
}

// SyncStarted marks a provider's sync as running.
func (m *Metrics) SyncStarted(provider string) {
	if m == nil {
		return
	}
	m.SyncRunning.WithLabelValues(provider).Set(1)
}

// SyncCounts are the totals of one run.
type SyncCounts struct {
	Upserts, Changed, Merges, Removals, Skipped, MarkedRemoved int64
}

// SyncFinished records a finished run.
func (m *Metrics) SyncFinished(provider, kind, status string, d time.Duration, c SyncCounts) {
	if m == nil {
		return
	}
	m.SyncRunning.WithLabelValues(provider).Set(0)
	m.SyncRuns.WithLabelValues(provider, kind, status).Inc()
	m.SyncDuration.WithLabelValues(provider, kind).Observe(d.Seconds())
	for ev, n := range map[string]int64{
		"upsert": c.Upserts, "changed": c.Changed, "merge": c.Merges,
		"removal": c.Removals, "skipped": c.Skipped, "marked_removed": c.MarkedRemoved,
	} {
		if n > 0 {
			m.SyncRecords.WithLabelValues(provider, ev).Add(float64(n))
		}
	}
}

// Search records one search or browse query.
func (m *Metrics) Search(backend, kind string, d time.Duration) {
	if m == nil {
		return
	}
	m.SearchDuration.WithLabelValues(backend, kind).Observe(d.Seconds())
}

// API records one API call.
func (m *Metrics) API(procedure string, err error, d time.Duration) {
	if m == nil {
		return
	}
	m.APIRequests.WithLabelValues(procedure, Code(err)).Inc()
	m.APIDuration.WithLabelValues(procedure).Observe(d.Seconds())
}
