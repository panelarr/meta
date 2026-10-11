// SPDX-License-Identifier: GPL-3.0-or-later

// Package api serves meta's API: MetaService and MetaAdminService over
// gRPC, gRPC-Web, Connect and REST/JSON (the google.api.http paths, through
// vanguard), plus health probes and the OpenAPI document.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/vanguard"

	adminui "github.com/panelarr/meta/admin"
	"github.com/panelarr/meta/gen/openapi"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/sdk/plugin"
)

// Server bundles the handlers and their dependencies.
type Server struct {
	auth    *auth.Authenticator
	log     *slog.Logger
	ready   func(context.Context) error
	handler http.Handler
}

// Services are the service implementations to serve.
type Services struct {
	Meta    metav1connect.MetaServiceHandler
	Admin   metav1connect.MetaAdminServiceHandler
	Clients metav1connect.MetaClientsServiceHandler
	// Integrations is optional; nil leaves the service unimplemented.
	Integrations metav1connect.MetaIntegrationsServiceHandler
}

// Options configure the server.
type Options struct {
	// Metrics records API calls; nil records nothing.
	Metrics *metrics.Metrics
	// MetricsHandler, when set, serves /metrics on the API port to callers
	// with the metrics:read scope.
	MetricsHandler http.Handler
	// Auth checks credentials. Nil serves everything without credentials,
	// which is only meant for tests.
	Auth *auth.Authenticator
	// Ready reports whether meta can serve (e.g. the database answers).
	Ready func(context.Context) error
	Log   *slog.Logger
}

// New builds the HTTP handler for the given service implementations.
func New(svc Services, opts Options) (*Server, error) {
	s := &Server{auth: opts.Auth, log: opts.Log, ready: opts.Ready}
	hopts := []connect.HandlerOption{
		// Scope checks run first, so a caller without the scope learns
		// nothing from the handler.
		connect.WithInterceptors(metricsInterceptor{m: opts.Metrics}, scopeInterceptor{}, errorInterceptor{log: opts.Log}),
		connect.WithReadMaxBytes(4 << 20),
	}
	var services []*vanguard.Service
	add := func(path string, h http.Handler) { services = append(services, vanguard.NewService(path, h)) }
	add(metav1connect.NewMetaServiceHandler(svc.Meta, hopts...))
	add(metav1connect.NewMetaAdminServiceHandler(svc.Admin, hopts...))
	add(metav1connect.NewMetaClientsServiceHandler(svc.Clients, hopts...))
	if svc.Integrations == nil {
		svc.Integrations = metav1connect.UnimplementedMetaIntegrationsServiceHandler{}
	}
	add(metav1connect.NewMetaIntegrationsServiceHandler(svc.Integrations, hopts...))
	// Unknown API paths answer like other API errors, not with the UI.
	transcoder, err := vanguard.NewTranscoder(services, vanguard.WithUnknownHandler(http.HandlerFunc(apiNotFound)))
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	reflector := grpcreflect.NewStaticReflector(metav1connect.MetaServiceName, metav1connect.MetaAdminServiceName,
		metav1connect.MetaClientsServiceName, metav1connect.MetaIntegrationsServiceName)
	rpath, rhandler := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(rpath, s.authn(requireScope(auth.ScopeAdmin, rhandler)))
	rpathA, rhandlerA := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(rpathA, s.authn(requireScope(auth.ScopeAdmin, rhandlerA)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi.Meta)
	})
	if opts.MetricsHandler != nil {
		mux.Handle("GET /metrics", s.authn(requireScope(auth.ScopeMetricsRead, opts.MetricsHandler)))
	}
	mux.Handle("/", s.route(s.authn(transcoder), adminui.Handler()))
	s.handler = mux
	return s, nil
}

// isAPI reports whether a request is for the API (REST under /v1, gRPC,
// gRPC-Web and Connect under the service paths) rather than for the UI.
func isAPI(r *http.Request) bool {
	p := r.URL.Path
	if p == "/v1" || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/panelarr.") || strings.HasPrefix(p, "/grpc.") {
		return true
	}
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/connect")
}

// route sends API requests to the API (with authentication) and page loads
// to the admin UI. The UI needs no credentials to load: it asks for the
// admin token and sends it with its API calls.
func (s *Server) route(api, ui http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPI(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			ui.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"code":5,"message":"no such API path","details":[]}`)
}

// Handler is the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// HTTPServer returns an http.Server for addr speaking HTTP/1.1 and HTTP/2
// (cleartext, as gRPC clients expect behind a proxy or in a cluster).
func (s *Server) HTTPServer(addr string) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	return srv
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	if s.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "not ready\n")
			return
		}
	}
	_, _ = io.WriteString(w, "ok\n")
}

// authn resolves the caller's credentials and puts the principal in the
// request context. Requests without valid credentials are refused.
func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			p := auth.Principal{Kind: auth.KindAdmin, Name: "admin", Scopes: []string{auth.ScopeAdmin}}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
			return
		}
		p, err := s.auth.Authenticate(r.Context(), auth.BearerToken(r.Header.Get("Authorization")))
		if errors.Is(err, auth.ErrUnauthenticated) {
			unauthenticated(w, r)
			return
		}
		if err != nil {
			if s.log != nil {
				s.log.Error("authenticate", "err", err)
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

// requireScope guards plain HTTP handlers (not RPCs) with a scope.
func requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := auth.FromContext(r.Context()); !p.Allows(scope) {
			http.Error(w, "this call needs the '"+scope+"' scope", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// metricsInterceptor counts API calls by procedure and code. It runs
// outermost, so it sees refused calls too.
type metricsInterceptor struct{ m *metrics.Metrics }

func (i metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	if i.m == nil {
		return next
	}
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		res, err := next(ctx, req)
		i.m.API(req.Spec().Procedure, err, time.Since(start))
		return res, err
	}
}

func (metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	if i.m == nil {
		return next
	}
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		i.m.API(conn.Spec().Procedure, err, time.Since(start))
		return err
	}
}

// scopeInterceptor checks that the caller holds the scope each RPC needs
// (auth.RequiredScope; deny by default).
type scopeInterceptor struct{}

func (scopeInterceptor) check(ctx context.Context, procedure string) error {
	need := auth.RequiredScope(procedure)
	p, ok := auth.FromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	if need != "" && !p.Allows(need) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this call needs the '%s' scope", need))
	}
	return nil
}

func (i scopeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.check(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (scopeInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i scopeInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.check(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// unauthenticated answers in the caller's protocol.
func unauthenticated(w http.ResponseWriter, r *http.Request) {
	const msg = "missing or wrong API key"
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/grpc") {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Grpc-Status", "16")
		w.Header().Set("Grpc-Message", msg)
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"code":"unauthenticated","message":"`+msg+`"}`)
}

// errorInterceptor turns plugin and internal errors into Connect errors
// and logs internal ones.
type errorInterceptor struct {
	log *slog.Logger
}

func (e errorInterceptor) convert(procedure string, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	var pe *plugin.Error
	if errors.As(err, &pe) {
		return plugin.ToConnectError(pe)
	}
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	if e.log != nil {
		e.log.Error("request failed", "procedure", procedure, "err", err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

func (e errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		return res, e.convert(req.Spec().Procedure, err)
	}
}

func (e errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (e errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return e.convert(conn.Spec().Procedure, next(ctx, conn))
	}
}
