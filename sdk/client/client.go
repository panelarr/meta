// SPDX-License-Identifier: Apache-2.0

// Package client connects a host (meta, or any program) to metadata
// plugins: managed binaries it starts itself, remote plugins it reaches by
// URL, and in-process plugins for tests. All three give the same [Client].
package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"sync"

	"connectrpc.com/connect"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/gen/panelarr/metadata/v1/metadatav1connect"
	"github.com/panelarr/meta/sdk/plugin"
)

// Client talks to one metadata plugin. Errors from the plugin come back as
// *plugin.Error where the plugin gave a status.
type Client interface {
	GetInfo(ctx context.Context) (*metadatav1.PluginInfo, error)
	Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error)
	Get(ctx context.Context, ref *metadatav1.ExternalId) (*metadatav1.ProviderEntry, error)
	// Sync starts a sync. Read events with Recv until it returns io.EOF.
	Sync(ctx context.Context, req *metadatav1.SyncRequest) (SyncStream, error)
	GetSettingsSchema(ctx context.Context) (*metadatav1.GetSettingsSchemaResponse, error)
	GetSettings(ctx context.Context) (*metadatav1.GetSettingsResponse, error)
	SetSettings(ctx context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error)
	Health(ctx context.Context) (*metadatav1.HealthResponse, error)
	// Close ends the connection and, for managed plugins, the process.
	Close() error
}

// SyncStream reads sync events.
type SyncStream interface {
	// Recv returns the next event, or io.EOF after the last one.
	Recv() (*metadatav1.SyncResponse, error)
	// Close stops reading and releases the stream.
	Close() error
}

// RemoteOptions configure [Remote].
type RemoteOptions struct {
	// APIKey is sent as "Authorization: Bearer <key>".
	APIKey string
	// HTTPClient defaults to a client that supports cleartext HTTP/2.
	HTTPClient *http.Client
	// GRPC uses the gRPC protocol instead of Connect. Needs HTTP/2.
	GRPC bool
}

// Remote returns a client for a plugin running as a service at baseURL,
// e.g. "http://meta-mangabaka:8903".
func Remote(baseURL string, opts RemoteOptions) Client {
	hc := opts.HTTPClient
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Protocols = new(http.Protocols)
		tr.Protocols.SetHTTP1(true)
		tr.Protocols.SetUnencryptedHTTP2(true)
		hc = &http.Client{Transport: tr}
	}
	copts := []connect.ClientOption{
		connect.WithReadMaxBytes(plugin.MaxMessageSize),
		connect.WithSendMaxBytes(plugin.MaxMessageSize),
	}
	if opts.APIKey != "" {
		copts = append(copts, connect.WithInterceptors(bearer(opts.APIKey)))
	}
	if opts.GRPC {
		copts = append(copts, connect.WithGRPC())
	}
	return &remoteClient{c: metadatav1connect.NewMetadataPluginServiceClient(hc, baseURL, copts...)}
}

type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(b))
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(b))
		return conn
	}
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type remoteClient struct {
	c metadatav1connect.MetadataPluginServiceClient
}

func callConnect[Req, Res any](ctx context.Context, req *Req, f func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)) (*Res, error) {
	res, err := f(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, plugin.FromConnectError(err)
	}
	return res.Msg, nil
}

func (r *remoteClient) GetInfo(ctx context.Context) (*metadatav1.PluginInfo, error) {
	res, err := callConnect(ctx, &metadatav1.GetInfoRequest{}, r.c.GetInfo)
	return res.GetInfo(), err
}

func (r *remoteClient) Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	return callConnect(ctx, req, r.c.Search)
}

func (r *remoteClient) Get(ctx context.Context, ref *metadatav1.ExternalId) (*metadatav1.ProviderEntry, error) {
	res, err := callConnect(ctx, &metadatav1.GetRequest{Ref: ref}, r.c.Get)
	return res.GetEntry(), err
}

func (r *remoteClient) Sync(ctx context.Context, req *metadatav1.SyncRequest) (SyncStream, error) {
	s, err := r.c.Sync(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, plugin.FromConnectError(err)
	}
	return &connectStream{s: s}, nil
}

func (r *remoteClient) GetSettingsSchema(ctx context.Context) (*metadatav1.GetSettingsSchemaResponse, error) {
	return callConnect(ctx, &metadatav1.GetSettingsSchemaRequest{}, r.c.GetSettingsSchema)
}

func (r *remoteClient) GetSettings(ctx context.Context) (*metadatav1.GetSettingsResponse, error) {
	return callConnect(ctx, &metadatav1.GetSettingsRequest{}, r.c.GetSettings)
}

func (r *remoteClient) SetSettings(ctx context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	return callConnect(ctx, req, r.c.SetSettings)
}

func (r *remoteClient) Health(ctx context.Context) (*metadatav1.HealthResponse, error) {
	return callConnect(ctx, &metadatav1.HealthRequest{}, r.c.Health)
}

func (r *remoteClient) Close() error { return nil }

type connectStream struct {
	s *connect.ServerStreamForClient[metadatav1.SyncResponse]
}

func (c *connectStream) Recv() (*metadatav1.SyncResponse, error) {
	if c.s.Receive() {
		return c.s.Msg(), nil
	}
	if err := c.s.Err(); err != nil {
		return nil, plugin.FromConnectError(err)
	}
	return nil, io.EOF
}

func (c *connectStream) Close() error { return c.s.Close() }

// ManagedOptions configure [Managed].
type ManagedOptions struct {
	// Args are passed to the plugin binary.
	Args []string
	// Env is added to the plugin's environment.
	Env []string
	// Logger receives the plugin's log output. Defaults to no output.
	Logger hclog.Logger
}

// Managed starts the plugin binary at path as a child process (go-plugin
// handshake, gRPC on a local socket) and returns a client for it. Close
// stops the process.
func Managed(path string, opts ManagedOptions) (Client, error) {
	// go-plugin owns the process lifetime (Close kills it), so no context here.
	cmd := exec.Command(path, opts.Args...) //nolint:gosec,noctx // the admin chose which plugin binary to run
	cmd.Env = append(cmd.Environ(), opts.Env...)
	logger := opts.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	pc := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  plugin.Handshake,
		Plugins:          plugin.PluginSet(nil),
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger:           logger,
		GRPCDialOptions: []grpc.DialOption{grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(plugin.MaxMessageSize),
			grpc.MaxCallSendMsgSize(plugin.MaxMessageSize),
		)},
	})
	rpc, err := pc.Client()
	if err != nil {
		pc.Kill()
		return nil, err
	}
	raw, err := rpc.Dispense(plugin.PluginName)
	if err != nil {
		pc.Kill()
		return nil, err
	}
	svc, ok := raw.(metadatav1.MetadataPluginServiceClient)
	if !ok {
		pc.Kill()
		return nil, errors.New("client: plugin did not return a metadata plugin client")
	}
	return &grpcClient{c: svc, kill: pc.Kill, exited: pc.Exited}, nil
}

type grpcClient struct {
	c      metadatav1.MetadataPluginServiceClient
	kill   func()
	exited func() bool
	once   sync.Once
}

func callGRPC[Req, Res any](ctx context.Context, req *Req, f func(context.Context, *Req, ...grpc.CallOption) (*Res, error)) (*Res, error) {
	res, err := f(ctx, req)
	if err != nil {
		return nil, plugin.FromGRPCError(err)
	}
	return res, nil
}

func (g *grpcClient) GetInfo(ctx context.Context) (*metadatav1.PluginInfo, error) {
	res, err := callGRPC(ctx, &metadatav1.GetInfoRequest{}, g.c.GetInfo)
	return res.GetInfo(), err
}

func (g *grpcClient) Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	return callGRPC(ctx, req, g.c.Search)
}

func (g *grpcClient) Get(ctx context.Context, ref *metadatav1.ExternalId) (*metadatav1.ProviderEntry, error) {
	res, err := callGRPC(ctx, &metadatav1.GetRequest{Ref: ref}, g.c.Get)
	return res.GetEntry(), err
}

func (g *grpcClient) Sync(ctx context.Context, req *metadatav1.SyncRequest) (SyncStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	s, err := g.c.Sync(ctx, req)
	if err != nil {
		cancel()
		return nil, plugin.FromGRPCError(err)
	}
	return &grpcStream{s: s, cancel: cancel}, nil
}

func (g *grpcClient) GetSettingsSchema(ctx context.Context) (*metadatav1.GetSettingsSchemaResponse, error) {
	return callGRPC(ctx, &metadatav1.GetSettingsSchemaRequest{}, g.c.GetSettingsSchema)
}

func (g *grpcClient) GetSettings(ctx context.Context) (*metadatav1.GetSettingsResponse, error) {
	return callGRPC(ctx, &metadatav1.GetSettingsRequest{}, g.c.GetSettings)
}

func (g *grpcClient) SetSettings(ctx context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	return callGRPC(ctx, req, g.c.SetSettings)
}

func (g *grpcClient) Health(ctx context.Context) (*metadatav1.HealthResponse, error) {
	if g.exited != nil && g.exited() {
		return &metadatav1.HealthResponse{Status: metadatav1.HealthStatus_HEALTH_STATUS_DOWN, Message: "plugin process exited"}, nil
	}
	return callGRPC(ctx, &metadatav1.HealthRequest{}, g.c.Health)
}

func (g *grpcClient) Close() error {
	g.once.Do(func() {
		if g.kill != nil {
			g.kill()
		}
	})
	return nil
}

type grpcStream struct {
	s      grpc.ServerStreamingClient[metadatav1.SyncResponse]
	cancel context.CancelFunc
}

func (g *grpcStream) Recv() (*metadatav1.SyncResponse, error) {
	msg, err := g.s.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, plugin.FromGRPCError(err)
	}
	return msg, nil
}

func (g *grpcStream) Close() error {
	g.cancel()
	return nil
}
