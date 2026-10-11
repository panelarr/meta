// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Clients implements MetaClientsService.
type Clients struct {
	metav1connect.UnimplementedMetaClientsServiceHandler
	st *store.Store
}

// NewClients returns the clients service.
func NewClients(st *store.Store) *Clients { return &Clients{st: st} }

func clientProto(c db.Client) *metav1.Client {
	out := &metav1.Client{
		Id:        c.ID,
		Name:      c.Name,
		Scopes:    c.Scopes,
		KeyPrefix: c.KeyPrefix,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
	if c.RotatedAt != nil {
		out.RotatedAt = timestamppb.New(*c.RotatedAt)
	}
	if c.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*c.LastUsedAt)
	}
	return out
}

func invalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("name is required")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", invalid("name is longer than 100 characters")
	}
	return name, nil
}

func cleanScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, invalid("at least one scope is required (known scopes: %s)", strings.Join(auth.KnownScopes, ", "))
	}
	var out []string
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if !slices.Contains(auth.KnownScopes, s) {
			return nil, invalid("unknown scope %q (known scopes: %s)", s, strings.Join(auth.KnownScopes, ", "))
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(auth.KnownScopes, a) - slices.Index(auth.KnownScopes, b) })
	return out, nil
}

func newKey() (key string, hash []byte, prefix string, err error) {
	key, err = auth.NewToken(auth.ClientKeyPrefix)
	if err != nil {
		return "", nil, "", err
	}
	return key, auth.Hash(key), key[:auth.KeyPrefixLen], nil
}

func (c *Clients) WhoAmI(ctx context.Context, _ *connect.Request[metav1.WhoAmIRequest]) (*connect.Response[metav1.WhoAmIResponse], error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	kind := metav1.PrincipalKind_PRINCIPAL_KIND_ADMIN
	switch p.Kind {
	case auth.KindClient:
		kind = metav1.PrincipalKind_PRINCIPAL_KIND_CLIENT
	case auth.KindConfigKey:
		kind = metav1.PrincipalKind_PRINCIPAL_KIND_CONFIG_KEY
	}
	return connect.NewResponse(&metav1.WhoAmIResponse{Kind: kind, ClientId: p.ClientID, Name: p.Name, Scopes: p.Scopes}), nil
}

func (c *Clients) ListClients(ctx context.Context, _ *connect.Request[metav1.ListClientsRequest]) (*connect.Response[metav1.ListClientsResponse], error) {
	rows, err := c.st.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	res := &metav1.ListClientsResponse{}
	for _, r := range rows {
		res.Clients = append(res.Clients, clientProto(r))
	}
	return connect.NewResponse(res), nil
}

func (c *Clients) CreateClient(ctx context.Context, req *connect.Request[metav1.CreateClientRequest]) (*connect.Response[metav1.CreateClientResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	scopes, err := cleanScopes(req.Msg.GetScopes())
	if err != nil {
		return nil, err
	}
	id, err := auth.NewClientID()
	if err != nil {
		return nil, err
	}
	key, hash, prefix, err := newKey()
	if err != nil {
		return nil, err
	}
	row, err := c.st.InsertClient(ctx, db.InsertClientParams{ID: id, Name: name, Scopes: scopes, KeyHash: hash, KeyPrefix: prefix})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.CreateClientResponse{Client: clientProto(row), ApiKey: key}), nil
}

func (c *Clients) get(ctx context.Context, id string) (db.Client, error) {
	row, err := c.st.GetClient(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, connect.NewError(connect.CodeNotFound, fmt.Errorf("no client %q", id))
	}
	return row, err
}

func (c *Clients) UpdateClient(ctx context.Context, req *connect.Request[metav1.UpdateClientRequest]) (*connect.Response[metav1.UpdateClientResponse], error) {
	cur, err := c.get(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, err
	}
	name, scopes := cur.Name, cur.Scopes
	if strings.TrimSpace(req.Msg.GetName()) != "" {
		if name, err = cleanName(req.Msg.GetName()); err != nil {
			return nil, err
		}
	}
	if len(req.Msg.GetScopes()) > 0 {
		if scopes, err = cleanScopes(req.Msg.GetScopes()); err != nil {
			return nil, err
		}
	}
	row, err := c.st.UpdateClient(ctx, db.UpdateClientParams{ID: cur.ID, Name: name, Scopes: scopes})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.UpdateClientResponse{Client: clientProto(row)}), nil
}

func (c *Clients) RotateClientKey(ctx context.Context, req *connect.Request[metav1.RotateClientKeyRequest]) (*connect.Response[metav1.RotateClientKeyResponse], error) {
	cur, err := c.get(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, err
	}
	key, hash, prefix, err := newKey()
	if err != nil {
		return nil, err
	}
	row, err := c.st.RotateClientKey(ctx, db.RotateClientKeyParams{ID: cur.ID, KeyHash: hash, KeyPrefix: prefix})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.RotateClientKeyResponse{Client: clientProto(row), ApiKey: key}), nil
}

func (c *Clients) DeleteClient(ctx context.Context, req *connect.Request[metav1.DeleteClientRequest]) (*connect.Response[metav1.DeleteClientResponse], error) {
	n, err := c.st.DeleteClient(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no client %q", req.Msg.GetClientId()))
	}
	return connect.NewResponse(&metav1.DeleteClientResponse{}), nil
}
