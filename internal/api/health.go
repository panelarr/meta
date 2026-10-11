// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/buildinfo"
	"github.com/panelarr/meta/internal/search"
)

// SetIndex lets GetHealth check the search backend.
func (a *Admin) SetIndex(idx search.Index) { a.idx = idx }

const (
	healthOK       = metadatav1.HealthStatus_HEALTH_STATUS_OK
	healthDegraded = metadatav1.HealthStatus_HEALTH_STATUS_DEGRADED
	healthDown     = metadatav1.HealthStatus_HEALTH_STATUS_DOWN
)

func timed(name, kind string, f func() (metadatav1.HealthStatus, string, map[string]string)) *metav1.ComponentHealth {
	start := time.Now()
	st, msg, details := f()
	return &metav1.ComponentHealth{
		Name: name, Kind: kind, Status: st, Message: msg, Details: details,
		Latency: durationpb.New(time.Since(start)), CheckedAt: timestamppb.Now(),
	}
}

// GetHealth checks the database, the search backend and every plugin.
func (a *Admin) GetHealth(ctx context.Context, _ *connect.Request[metav1.GetHealthRequest]) (*connect.Response[metav1.GetHealthResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var comps []*metav1.ComponentHealth

	comps = append(comps, timed("database", "database", func() (metadatav1.HealthStatus, string, map[string]string) {
		if err := a.st.Pool.Ping(ctx); err != nil {
			return healthDown, err.Error(), nil
		}
		stat := a.st.Pool.Stat()
		return healthOK, "", map[string]string{
			"connections": strconv.Itoa(int(stat.TotalConns())),
			"idle":        strconv.Itoa(int(stat.IdleConns())),
		}
	}))

	if a.idx != nil {
		comps = append(comps, timed("search", "search", func() (metadatav1.HealthStatus, string, map[string]string) {
			caps := a.idx.Capabilities()
			details := map[string]string{
				"backend":        caps.Name,
				"typo_tolerance": strconv.FormatBool(caps.TypoTolerance),
				"substring":      strconv.FormatBool(caps.Substring),
				"facets":         strconv.FormatBool(caps.Facets),
			}
			if _, err := a.idx.Query(ctx, search.Query{Limit: 1}); err != nil {
				return healthDown, err.Error(), details
			}
			return healthOK, "", details
		}))
	}

	providers := a.host.List()
	pcs := make([]*metav1.ComponentHealth, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pcs[i] = timed("provider:"+p.ID(), "provider", func() (metadatav1.HealthStatus, string, map[string]string) {
				p.CheckHealth(ctx)
				st, msg := p.Health()
				details := map[string]string{"mode": p.Config().Mode()}
				if info := p.Info(); info != nil {
					details["plugin"] = info.GetId()
					details["version"] = info.GetVersion()
				}
				if id, ok := a.runner.Running(p.ID()); ok {
					details["running_sync"] = id.String()
				}
				if st == metadatav1.HealthStatus_HEALTH_STATUS_UNSPECIFIED {
					st, msg = healthDown, "not connected yet"
				}
				return st, msg, details
			})
		}()
	}
	wg.Wait()
	comps = append(comps, pcs...)

	overall := healthOK
	for _, c := range comps {
		switch {
		case c.GetKind() == "database" && c.GetStatus() != healthOK:
			overall = healthDown
		case c.GetStatus() != healthOK && overall == healthOK:
			overall = healthDegraded
		}
	}
	return connect.NewResponse(&metav1.GetHealthResponse{Status: overall, Components: comps, Version: buildinfo.Version}), nil
}
