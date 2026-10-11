// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/google/uuid"
)

// Switch is an Index whose backend can be changed while meta runs, when
// the search engine settings change.
type Switch struct {
	cur atomic.Pointer[Index]
}

// NewSwitch returns a Switch serving idx.
func NewSwitch(idx Index) *Switch {
	s := &Switch{}
	s.Set(idx)
	return s
}

// Set changes the backend.
func (s *Switch) Set(idx Index) { s.cur.Store(&idx) }

// Get returns the backend.
func (s *Switch) Get() Index { return *s.cur.Load() }

func (s *Switch) Query(ctx context.Context, q Query) (Result, error) { return s.Get().Query(ctx, q) }
func (s *Switch) Upsert(ctx context.Context, ids ...uuid.UUID) error {
	return s.Get().Upsert(ctx, ids...)
}
func (s *Switch) Delete(ctx context.Context, ids ...uuid.UUID) error {
	return s.Get().Delete(ctx, ids...)
}
func (s *Switch) Rebuild(ctx context.Context) error { return s.Get().Rebuild(ctx) }
func (s *Switch) Capabilities() Capabilities        { return s.Get().Capabilities() }

// Health reports the backend's health, when it has more to say than
// whether a query works.
func (s *Switch) Health(ctx context.Context) (Health, bool) {
	idx := s.Get()
	if u, ok := idx.(interface{ Unwrap() Index }); ok {
		idx = u.Unwrap()
	}
	if h, ok := idx.(interface {
		Health(context.Context) Health
	}); ok {
		return h.Health(ctx), true
	}
	return Health{}, false
}

// Health is an engine backend's state for the health view.
type Health struct {
	// Degraded: searches are answered by PostgreSQL instead.
	Degraded bool
	Message  string
	Details  map[string]string
}

// Health checks the engine and reports the index and the outbox.
func (x *External) Health(ctx context.Context) Health {
	st, err := x.Status(ctx)
	h := Health{Details: map[string]string{"index": st.Index, "documents": strconv.FormatInt(st.Documents, 10),
		"outbox_pending": strconv.FormatInt(st.Pending, 10), "outbox_lag": st.Lag.Round(1e9).String()}}
	switch {
	case err != nil:
		h.Degraded, h.Message = true, err.Error()
	case !st.Ready:
		h.Degraded, h.Message = true, "the index isn't built yet; PostgreSQL answers searches"
	default:
		if cerr := x.Check(ctx); cerr != nil {
			h.Degraded, h.Message = true, fmt.Sprintf("unreachable (%v); PostgreSQL answers searches", cerr)
		} else if st.Degraded {
			h.Degraded, h.Message = true, "recent queries failed ("+st.LastErr+"); PostgreSQL answered them"
		}
	}
	return h
}
