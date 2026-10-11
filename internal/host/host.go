// SPDX-License-Identifier: GPL-3.0-or-later

// Package host runs meta's metadata plugins: it starts managed binaries or
// connects to remote ones, applies their settings, keeps track of their
// health, and enforces the limits each plugin declares for its ingestion
// types.
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugin"
)

// DialFunc connects to a provider's plugin. Tests replace it to use
// in-process plugins.
type DialFunc func(ctx context.Context, p config.Provider, log *slog.Logger) (client.Client, error)

// Dial is the default DialFunc: managed binaries through go-plugin, remote
// plugins over gRPC (or Connect when configured).
func Dial(_ context.Context, p config.Provider, log *slog.Logger) (client.Client, error) {
	if p.Managed != nil {
		return client.Managed(p.Managed.Path, client.ManagedOptions{
			Args:   p.Managed.Args,
			Env:    p.Managed.Env,
			Logger: hclogAdapter(log.With("provider", p.ID)),
		})
	}
	return client.Remote(p.Remote.URL, client.RemoteOptions{APIKey: p.Remote.APIKey, Connect: p.Remote.Connect}), nil
}

func hclogAdapter(l *slog.Logger) hclog.Logger {
	return hclog.FromStandardLogger(slog.NewLogLogger(l.Handler(), slog.LevelInfo), &hclog.LoggerOptions{Level: hclog.Info})
}

// Host owns the configured providers.
type Host struct {
	st        *store.Store
	log       *slog.Logger
	dial      DialFunc
	order     []string
	providers map[string]*Provider
}

// New prepares a host for the enabled providers in cfg. Nothing connects
// until Start or first use.
func New(cfg config.Config, st *store.Store, log *slog.Logger, dial DialFunc) *Host {
	if dial == nil {
		dial = Dial
	}
	h := &Host{st: st, log: log, dial: dial, providers: map[string]*Provider{}}
	for _, pc := range cfg.Providers {
		if pc.Disabled {
			continue
		}
		h.order = append(h.order, pc.ID)
		h.providers[pc.ID] = &Provider{cfg: pc, host: h, log: log.With("provider", pc.ID)}
	}
	return h
}

// Start connects to every provider in the background, retrying failed
// connections until ctx ends, and checks health every interval.
func (h *Host) Start(ctx context.Context, interval time.Duration) {
	for _, id := range h.order {
		p := h.providers[id]
		go p.watch(ctx, interval)
	}
}

// Close disconnects every provider (and stops managed processes).
func (h *Host) Close() {
	for _, p := range h.providers {
		p.disconnect()
	}
}

// Get returns a provider by ID.
func (h *Host) Get(id string) (*Provider, bool) {
	p, ok := h.providers[id]
	return p, ok
}

// List returns the providers in configuration order.
func (h *Host) List() []*Provider {
	out := make([]*Provider, 0, len(h.order))
	for _, id := range h.order {
		out = append(out, h.providers[id])
	}
	return out
}

// Empty reports whether no providers are configured.
func (h *Host) Empty() bool { return len(h.order) == 0 }

// Provider is one configured plugin.
type Provider struct {
	cfg  config.Provider
	host *Host
	log  *slog.Logger

	mu       sync.Mutex
	client   client.Client
	info     *metadatav1.PluginInfo
	health   metadatav1.HealthStatus
	message  string
	limiters map[string]*rate.Limiter
	sems     map[string]chan struct{}
}

// ID is the provider key.
func (p *Provider) ID() string { return p.cfg.ID }

// Config is the provider's configuration.
func (p *Provider) Config() config.Provider { return p.cfg }

// Info is what the plugin last reported, or nil before it connected.
func (p *Provider) Info() *metadatav1.PluginInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// Health is the last known health and its message.
func (p *Provider) Health() (metadatav1.HealthStatus, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health, p.message
}

// Supports reports whether the plugin declares an ingestion kind.
func (p *Provider) Supports(kind string) bool {
	return p.ingestion(kind) != nil
}

func (p *Provider) ingestion(kind string) *metadatav1.Ingestion {
	for _, in := range p.Info().GetIngestions() {
		if in.GetKind() == kind {
			return in
		}
	}
	return nil
}

// Connect makes sure the plugin is connected.
func (p *Provider) Connect(ctx context.Context) error {
	_, err := p.conn(ctx)
	return err
}

// ErrNotConnected is returned while a plugin can't be reached.
var ErrNotConnected = errors.New("plugin is not connected")

// conn returns a working client, connecting if needed.
func (p *Provider) conn(ctx context.Context) (client.Client, error) {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c != nil {
		return c, nil
	}
	return p.connect(ctx)
}

func (p *Provider) connect(ctx context.Context) (client.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	c, err := p.host.dial(ctx, p.cfg, p.log)
	if err != nil {
		p.setHealthLocked(ctx, metadatav1.HealthStatus_HEALTH_STATUS_DOWN, err.Error())
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	info, err := c.GetInfo(ctx)
	if err == nil && info.GetProvider() != p.cfg.ID {
		err = fmt.Errorf("plugin reports provider %q, configured as %q", info.GetProvider(), p.cfg.ID)
	}
	if err == nil && p.cfg.Settings != nil {
		var st *structpb.Struct
		if st, err = structpb.NewStruct(p.cfg.Settings); err == nil {
			_, err = c.SetSettings(ctx, &metadatav1.SetSettingsRequest{Settings: st})
			if err != nil {
				err = fmt.Errorf("settings rejected: %w", err)
			}
		}
	}
	if err != nil {
		_ = c.Close()
		p.setHealthLocked(ctx, metadatav1.HealthStatus_HEALTH_STATUS_DOWN, err.Error())
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	p.client = c
	p.info = info
	p.buildLimits(info)
	status := metadatav1.HealthStatus_HEALTH_STATUS_OK
	msg := ""
	if h, herr := c.Health(ctx); herr == nil {
		status, msg = h.GetStatus(), h.GetMessage()
	}
	p.setHealthLocked(ctx, status, msg)
	p.log.Info("plugin connected", "plugin", info.GetId(), "version", info.GetVersion(), "mode", p.cfg.Mode())
	return c, nil
}

func (p *Provider) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
}

func (p *Provider) setHealthLocked(ctx context.Context, s metadatav1.HealthStatus, msg string) {
	p.health, p.message = s, msg
	var info []byte
	if p.info != nil {
		info, _ = protojson.Marshal(p.info)
	}
	if p.host.st == nil {
		return
	}
	if err := p.host.st.UpsertProvider(ctx, db.UpsertProviderParams{
		ID: p.cfg.ID, Mode: p.cfg.Mode(), Info: info, Health: enums.Name(s), HealthMessage: msg,
	}); err != nil {
		p.log.Warn("store provider state", "err", err)
	}
}

// CheckHealth asks the plugin for its health, reconnecting if the
// connection is gone.
func (p *Provider) CheckHealth(ctx context.Context) {
	c, err := p.conn(ctx)
	if err != nil {
		return
	}
	h, err := c.Health(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.log.Warn("plugin health check failed", "err", err)
		if p.client != nil {
			_ = p.client.Close()
			p.client = nil
		}
		p.setHealthLocked(ctx, metadatav1.HealthStatus_HEALTH_STATUS_DOWN, err.Error())
		return
	}
	if h.GetStatus() == metadatav1.HealthStatus_HEALTH_STATUS_DOWN && p.cfg.Managed != nil && h.GetMessage() == "plugin process exited" && p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
	p.setHealthLocked(ctx, h.GetStatus(), h.GetMessage())
}

func (p *Provider) watch(ctx context.Context, interval time.Duration) {
	backoff := time.Second
	for {
		if _, err := p.conn(ctx); err != nil {
			p.log.Warn("plugin not reachable, will retry", "err", err, "in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		p.CheckHealth(ctx)
	}
}

func (p *Provider) buildLimits(info *metadatav1.PluginInfo) {
	p.limiters = map[string]*rate.Limiter{}
	p.sems = map[string]chan struct{}{}
	for _, in := range info.GetIngestions() {
		for _, rl := range in.GetLimits().GetRateLimits() {
			if rl.GetRequests() <= 0 || rl.GetPer().AsDuration() <= 0 {
				continue
			}
			burst := max(int(rl.GetBurst()), 1)
			p.limiters[in.GetKind()+"/"+rl.GetOperation()] = rate.NewLimiter(rate.Every(rl.GetPer().AsDuration()/time.Duration(rl.GetRequests())), burst)
		}
		if n := in.GetLimits().GetMaxConcurrency(); n > 0 {
			p.sems[in.GetKind()] = make(chan struct{}, n)
		}
	}
}

// acquire waits for the limits of kind and operation and returns a
// release function for the concurrency slot.
func (p *Provider) acquire(ctx context.Context, kind, op string) (func(), error) {
	p.mu.Lock()
	lims := []*rate.Limiter{p.limiters[kind+"/"+op], p.limiters[kind+"/*"]}
	sem := p.sems[kind]
	p.mu.Unlock()
	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release := func() {
		if sem != nil {
			<-sem
		}
	}
	for _, l := range lims {
		if l == nil {
			continue
		}
		if err := l.Wait(ctx); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

// lookupKind is the on-demand ingestion kind the plugin supports.
func (p *Provider) lookupKind() string {
	for _, k := range []string{plugin.KindLookup, plugin.KindScrape} {
		if p.Supports(k) {
			return k
		}
	}
	return ""
}

// CanLookup reports whether the plugin supports on-demand lookups.
func (p *Provider) CanLookup(ctx context.Context) bool {
	if _, err := p.conn(ctx); err != nil {
		return false
	}
	return p.lookupKind() != ""
}

// Get fetches one record through the plugin's lookup, within its limits.
func (p *Provider) Get(ctx context.Context, ref *metadatav1.ExternalId) (*metadatav1.ProviderEntry, error) {
	c, err := p.conn(ctx)
	if err != nil {
		return nil, err
	}
	kind := p.lookupKind()
	if kind == "" {
		return nil, plugin.ErrUnimplemented("lookup")
	}
	release, err := p.acquire(ctx, kind, "get")
	if err != nil {
		return nil, err
	}
	defer release()
	return c.Get(ctx, ref)
}

// Search searches through the plugin's lookup, within its limits.
func (p *Provider) Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	c, err := p.conn(ctx)
	if err != nil {
		return nil, err
	}
	kind := p.lookupKind()
	if kind == "" {
		return nil, plugin.ErrUnimplemented("lookup")
	}
	release, err := p.acquire(ctx, kind, "search")
	if err != nil {
		return nil, err
	}
	defer release()
	return c.Search(ctx, req)
}

// Sync starts a sync within the plugin's limits. Call release when the
// stream is done.
func (p *Provider) Sync(ctx context.Context, req *metadatav1.SyncRequest) (client.SyncStream, func(), error) {
	c, err := p.conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !p.Supports(req.GetKind()) {
		return nil, nil, plugin.Errorf(plugin.CodeFailedPrecondition, "provider %s does not support %q syncs", p.cfg.ID, req.GetKind())
	}
	release, err := p.acquire(ctx, req.GetKind(), "sync")
	if err != nil {
		return nil, nil, err
	}
	s, err := c.Sync(ctx, req)
	if err != nil {
		release()
		return nil, nil, err
	}
	return s, release, nil
}
