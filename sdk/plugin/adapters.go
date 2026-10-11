// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"

	"connectrpc.com/connect"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/gen/panelarr/metadata/v1/metadatav1connect"
)

// connectServer exposes a Plugin as a Connect handler (standalone mode).
type connectServer struct {
	p Plugin
}

var _ metadatav1connect.MetadataPluginServiceHandler = (*connectServer)(nil)

func unaryConnect[Req, Res any](ctx context.Context, req *connect.Request[Req], f func(context.Context, *Req) (*Res, error)) (*connect.Response[Res], error) {
	res, err := f(ctx, req.Msg)
	if err != nil {
		return nil, ToConnectError(err)
	}
	return connect.NewResponse(res), nil
}

func (s *connectServer) GetInfo(ctx context.Context, r *connect.Request[metadatav1.GetInfoRequest]) (*connect.Response[metadatav1.GetInfoResponse], error) {
	return unaryConnect(ctx, r, s.p.GetInfo)
}

func (s *connectServer) Search(ctx context.Context, r *connect.Request[metadatav1.SearchRequest]) (*connect.Response[metadatav1.SearchResponse], error) {
	return unaryConnect(ctx, r, s.p.Search)
}

func (s *connectServer) Get(ctx context.Context, r *connect.Request[metadatav1.GetRequest]) (*connect.Response[metadatav1.GetResponse], error) {
	return unaryConnect(ctx, r, s.p.Get)
}

func (s *connectServer) Sync(ctx context.Context, r *connect.Request[metadatav1.SyncRequest], stream *connect.ServerStream[metadatav1.SyncResponse]) error {
	return ToConnectError(s.p.Sync(ctx, r.Msg, stream.Send))
}

func (s *connectServer) GetSettingsSchema(ctx context.Context, r *connect.Request[metadatav1.GetSettingsSchemaRequest]) (*connect.Response[metadatav1.GetSettingsSchemaResponse], error) {
	return unaryConnect(ctx, r, s.p.GetSettingsSchema)
}

func (s *connectServer) GetSettings(ctx context.Context, r *connect.Request[metadatav1.GetSettingsRequest]) (*connect.Response[metadatav1.GetSettingsResponse], error) {
	return unaryConnect(ctx, r, s.p.GetSettings)
}

func (s *connectServer) SetSettings(ctx context.Context, r *connect.Request[metadatav1.SetSettingsRequest]) (*connect.Response[metadatav1.SetSettingsResponse], error) {
	return unaryConnect(ctx, r, s.p.SetSettings)
}

func (s *connectServer) Health(ctx context.Context, r *connect.Request[metadatav1.HealthRequest]) (*connect.Response[metadatav1.HealthResponse], error) {
	return unaryConnect(ctx, r, s.p.Health)
}

// grpcServer exposes a Plugin as a gRPC service (managed mode).
type grpcServer struct {
	metadatav1.UnimplementedMetadataPluginServiceServer
	p Plugin
}

func unaryGRPC[Req, Res any](ctx context.Context, req *Req, f func(context.Context, *Req) (*Res, error)) (*Res, error) {
	res, err := f(ctx, req)
	if err != nil {
		return nil, ToGRPCError(err)
	}
	return res, nil
}

func (s *grpcServer) GetInfo(ctx context.Context, r *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	return unaryGRPC(ctx, r, s.p.GetInfo)
}

func (s *grpcServer) Search(ctx context.Context, r *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	return unaryGRPC(ctx, r, s.p.Search)
}

func (s *grpcServer) Get(ctx context.Context, r *metadatav1.GetRequest) (*metadatav1.GetResponse, error) {
	return unaryGRPC(ctx, r, s.p.Get)
}

func (s *grpcServer) Sync(r *metadatav1.SyncRequest, stream grpc.ServerStreamingServer[metadatav1.SyncResponse]) error {
	return ToGRPCError(s.p.Sync(stream.Context(), r, stream.Send))
}

func (s *grpcServer) GetSettingsSchema(ctx context.Context, r *metadatav1.GetSettingsSchemaRequest) (*metadatav1.GetSettingsSchemaResponse, error) {
	return unaryGRPC(ctx, r, s.p.GetSettingsSchema)
}

func (s *grpcServer) GetSettings(ctx context.Context, r *metadatav1.GetSettingsRequest) (*metadatav1.GetSettingsResponse, error) {
	return unaryGRPC(ctx, r, s.p.GetSettings)
}

func (s *grpcServer) SetSettings(ctx context.Context, r *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	return unaryGRPC(ctx, r, s.p.SetSettings)
}

func (s *grpcServer) Health(ctx context.Context, r *metadatav1.HealthRequest) (*metadatav1.HealthResponse, error) {
	return unaryGRPC(ctx, r, s.p.Health)
}

// GRPCPlugin is the go-plugin glue for metadata plugins. Hosts use it with
// a nil Impl to dispense a client; plugins serve with Impl set.
type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl Plugin
}

// GRPCServer registers the plugin on the go-plugin gRPC server.
func (g *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	metadatav1.RegisterMetadataPluginServiceServer(s, &grpcServer{p: g.Impl})
	return nil
}

// GRPCClient returns a gRPC client for the plugin service.
func (g *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return metadatav1.NewMetadataPluginServiceClient(c), nil
}

// PluginSet is the go-plugin plugin set for metadata plugins.
func PluginSet(impl Plugin) goplugin.PluginSet {
	return goplugin.PluginSet{PluginName: &GRPCPlugin{Impl: impl}}
}

func serveManaged(p Plugin) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         PluginSet(p),
		GRPCServer: func(opts []grpc.ServerOption) *grpc.Server {
			opts = append(opts, grpc.MaxRecvMsgSize(MaxMessageSize), grpc.MaxSendMsgSize(MaxMessageSize))
			return grpc.NewServer(opts...)
		},
		Logger: hclog.New(&hclog.LoggerOptions{Name: "plugin", JSONFormat: true, Level: hclog.Info}),
	})
}
