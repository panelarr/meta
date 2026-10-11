// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"io"
	"sync"

	"google.golang.org/protobuf/proto"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
)

// InProcess returns a client that calls p directly, without a process or a
// network in between. Messages are cloned on the way, as a real transport
// would copy them. Meant for tests.
func InProcess(p plugin.Plugin) Client {
	return &inProcess{p: p}
}

type inProcess struct {
	p plugin.Plugin
}

func clone[T proto.Message](m T) T {
	return proto.Clone(m).(T)
}

func (c *inProcess) GetInfo(ctx context.Context) (*metadatav1.PluginInfo, error) {
	res, err := c.p.GetInfo(ctx, &metadatav1.GetInfoRequest{})
	if err != nil {
		return nil, err
	}
	return clone(res.GetInfo()), nil
}

func (c *inProcess) Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	res, err := c.p.Search(ctx, clone(req))
	if err != nil {
		return nil, err
	}
	return clone(res), nil
}

func (c *inProcess) Get(ctx context.Context, ref *metadatav1.ExternalId) (*metadatav1.ProviderEntry, error) {
	res, err := c.p.Get(ctx, &metadatav1.GetRequest{Ref: clone(ref)})
	if err != nil {
		return nil, err
	}
	return clone(res.GetEntry()), nil
}

func (c *inProcess) Sync(ctx context.Context, req *metadatav1.SyncRequest) (SyncStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &inProcessStream{events: make(chan *metadatav1.SyncResponse, 64), cancel: cancel}
	req = clone(req)
	go func() {
		defer close(s.events)
		err := c.p.Sync(ctx, req, func(ev *metadatav1.SyncResponse) error {
			select {
			case s.events <- clone(ev):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}()
	return s, nil
}

func (c *inProcess) GetSettingsSchema(ctx context.Context) (*metadatav1.GetSettingsSchemaResponse, error) {
	return c.p.GetSettingsSchema(ctx, &metadatav1.GetSettingsSchemaRequest{})
}

func (c *inProcess) GetSettings(ctx context.Context) (*metadatav1.GetSettingsResponse, error) {
	return c.p.GetSettings(ctx, &metadatav1.GetSettingsRequest{})
}

func (c *inProcess) SetSettings(ctx context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	return c.p.SetSettings(ctx, clone(req))
}

func (c *inProcess) Health(ctx context.Context) (*metadatav1.HealthResponse, error) {
	return c.p.Health(ctx, &metadatav1.HealthRequest{})
}

func (c *inProcess) SetNetworkProfile(ctx context.Context, p *metadatav1.NetworkProfile) error {
	var cp *metadatav1.NetworkProfile
	if p != nil {
		cp = clone(p)
	}
	_, err := c.p.SetNetworkProfile(ctx, &metadatav1.SetNetworkProfileRequest{Profile: cp})
	return err
}

func (c *inProcess) Close() error { return nil }

type inProcessStream struct {
	events chan *metadatav1.SyncResponse
	cancel context.CancelFunc
	mu     sync.Mutex
	err    error
}

func (s *inProcessStream) Recv() (*metadatav1.SyncResponse, error) {
	ev, ok := <-s.events
	if ok {
		return ev, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

func (s *inProcessStream) Close() error {
	s.cancel()
	// Drain so the plugin goroutine can finish.
	for range s.events { //nolint:revive // draining
	}
	return nil
}
