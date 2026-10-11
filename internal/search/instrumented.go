// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"time"

	"github.com/panelarr/meta/internal/metrics"
)

// Instrumented records the latency of every query of idx in m.
func Instrumented(idx Index, m *metrics.Metrics) Index {
	if m == nil {
		return idx
	}
	return instrumented{Index: idx, m: m}
}

type instrumented struct {
	Index
	m *metrics.Metrics
}

func (i instrumented) Query(ctx context.Context, q Query) (Result, error) {
	start := time.Now()
	res, err := i.Index.Query(ctx, q)
	kind := "browse"
	if q.Text != "" {
		kind = "search"
	}
	i.m.Search(i.Capabilities().Name, kind, time.Since(start))
	return res, err
}
