// SPDX-License-Identifier: GPL-3.0-or-later

// Package api serves meta's API: MetaService and MetaAdminService over
// gRPC, gRPC-Web, Connect and REST/JSON (the google.api.http paths, through
// vanguard), plus health probes and the OpenAPI document.
package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/vanguard"

	"github.com/panelarr/meta/gen/openapi"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/sdk/plugin"
)

// Server bundles the handlers and their dependencies.
type Server struct {
	meta    metav1connect.MetaServiceHandler
	admin   metav1connect.MetaAdminServiceHandler
	keys    [][]byte
	log     *slog.Logger
	ready   func(context.Context) error
	handler http.Handler
}

// Options configure the server.
type Options struct {
	// APIKeys accepted as "Authorization: Bearer <key>". Empty: no auth.
	APIKeys []string
	// Ready reports whether meta can serve (e.g. the database answers).
	Ready func(context.Context) error
	Log   *slog.Logger
}

// New builds the HTTP handler for the given service implementations.
func New(meta metav1connect.MetaServiceHandler, admin metav1connect.MetaAdminServiceHandler, opts Options) (*Server, error) {
	s := &Server{meta: meta, admin: admin, log: opts.Log, ready: opts.Ready}
	for _, k := range opts.APIKeys {
		s.keys = append(s.keys, []byte("Bearer "+k))
	}
	hopts := []connect.HandlerOption{
		connect.WithInterceptors(errorInterceptor{log: opts.Log}),
		connect.WithReadMaxBytes(4 << 20),
	}
	metaPath, metaHandler := metav1connect.NewMetaServiceHandler(meta, hopts...)
	adminPath, adminHandler := metav1connect.NewMetaAdminServiceHandler(admin, hopts...)
	transcoder, err := vanguard.NewTranscoder([]*vanguard.Service{
		vanguard.NewService(metaPath, metaHandler),
		vanguard.NewService(adminPath, adminHandler),
	})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	reflector := grpcreflect.NewStaticReflector(metav1connect.MetaServiceName, metav1connect.MetaAdminServiceName)
	rpath, rhandler := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(rpath, s.auth(rhandler))
	rpathA, rhandlerA := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(rpathA, s.auth(rhandlerA))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi.Meta)
	})
	mux.Handle("/", s.auth(transcoder))
	s.handler = mux
	return s, nil
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

func (s *Server) auth(next http.Handler) http.Handler {
	if len(s.keys) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		ok := false
		for _, k := range s.keys {
			if subtle.ConstantTimeCompare(got, k) == 1 {
				ok = true
			}
		}
		if !ok {
			unauthenticated(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
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
