// SPDX-License-Identifier: GPL-3.0-or-later

package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/panelarr/meta/sdk/plugin"
)

func TestHelpers(t *testing.T) {
	var none *Metrics
	none.PluginCall("p", "get", time.Now(), nil) // nil-safe
	none.SyncFinished("p", "dump", "succeeded", time.Second, SyncCounts{})

	m := New()
	m.PluginCall("mangabaka", "get", time.Now(), plugin.ErrNotFound("x"))
	m.PluginCall("mangabaka", "get", time.Now(), nil)
	if v := testutil.ToFloat64(m.PluginCalls.WithLabelValues("mangabaka", "get", "not_found")); v != 1 {
		t.Fatalf("not_found calls: %v", v)
	}
	if v := testutil.ToFloat64(m.PluginCalls.WithLabelValues("mangabaka", "get", "ok")); v != 1 {
		t.Fatalf("ok calls: %v", v)
	}
	m.SyncStarted("mangabaka")
	if v := testutil.ToFloat64(m.SyncRunning.WithLabelValues("mangabaka")); v != 1 {
		t.Fatal("running")
	}
	m.SyncFinished("mangabaka", "dump", "succeeded", time.Minute, SyncCounts{Upserts: 10, Changed: 3, Merges: 2})
	if testutil.ToFloat64(m.SyncRunning.WithLabelValues("mangabaka")) != 0 ||
		testutil.ToFloat64(m.SyncRuns.WithLabelValues("mangabaka", "dump", "succeeded")) != 1 ||
		testutil.ToFloat64(m.SyncRecords.WithLabelValues("mangabaka", "upsert")) != 10 ||
		testutil.ToFloat64(m.SyncRecords.WithLabelValues("mangabaka", "merge")) != 2 {
		t.Fatal("sync metrics")
	}
	if Code(errors.New("x")) != "internal" || Code(nil) != "ok" {
		t.Fatal("codes")
	}
}
