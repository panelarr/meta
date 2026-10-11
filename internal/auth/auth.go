// SPDX-License-Identifier: GPL-3.0-or-later

// Package auth authenticates callers of meta's API and decides what they
// may call.
//
// There are three kinds of credentials, all sent as
// "Authorization: Bearer <token>":
//
//   - the admin token: full access. It comes from META_ADMIN_TOKEN, or meta
//     generates one on first start, prints it once and stores only its hash;
//   - client API keys ("mtk_…"): created in the admin, each with scopes;
//   - keys from the deprecated api_keys setting: full access, kept so
//     existing setups keep working.
//
// Authorization is deny-by-default: every RPC needs the admin scope unless
// RequiredScope lists it.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Scopes.
const (
	// ScopeCatalogRead allows browsing, searching and reading the catalogue.
	ScopeCatalogRead = "catalog:read"
	// ScopeMetricsRead allows scraping /metrics on the API port.
	ScopeMetricsRead = "metrics:read"
	// ScopeAdmin allows everything.
	ScopeAdmin = "admin"
)

// KnownScopes lists every scope, in display order.
var KnownScopes = []string{ScopeCatalogRead, ScopeMetricsRead, ScopeAdmin}

// Token prefixes tell the kinds apart at a glance (and in secret scanners).
const (
	AdminTokenPrefix = "mta_"
	ClientKeyPrefix  = "mtk_"
	// KeyPrefixLen is how much of a key is kept in clear to tell keys apart.
	KeyPrefixLen = 10
)

// Kind is the kind of a principal.
type Kind int

const (
	KindAdmin Kind = iota + 1
	KindClient
	KindConfigKey
)

// Principal is an authenticated caller.
type Principal struct {
	Kind     Kind
	ClientID string
	Name     string
	Scopes   []string
}

// Allows reports whether the principal holds scope. admin implies every
// scope.
func (p Principal) Allows(scope string) bool {
	return slices.Contains(p.Scopes, ScopeAdmin) || slices.Contains(p.Scopes, scope)
}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal of the request, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// RequiredScope returns the scope an RPC needs, given its full procedure
// name ("/package.Service/Method"). An empty scope means any valid
// credential.
func RequiredScope(procedure string) string {
	switch {
	case strings.HasPrefix(procedure, "/panelarr.meta.v1.MetaService/"):
		return ScopeCatalogRead
	case procedure == "/panelarr.meta.v1.MetaClientsService/WhoAmI":
		return ""
	default:
		return ScopeAdmin
	}
}

// NewToken returns prefix followed by 32 random bytes in base64url.
func NewToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns the SHA-256 of a token.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewClientID returns a random client ID.
func NewClientID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ErrUnauthenticated means no valid credential was sent.
var ErrUnauthenticated = errors.New("a valid bearer token is required")

// Options configure an Authenticator.
type Options struct {
	// AdminToken from META_ADMIN_TOKEN. Empty: use the stored hash, or
	// generate a token on first start.
	AdminToken string
	// ConfigKeys from the deprecated api_keys setting.
	ConfigKeys []string
	// Out receives the generated admin token, once. It is not a log, so
	// the token doesn't end up in log files.
	Out io.Writer
	Log *slog.Logger
}

// Authenticator checks credentials.
type Authenticator struct {
	st         *store.Store
	log        *slog.Logger
	adminHash  []byte
	configKeys [][]byte

	mu       sync.Mutex
	lastSeen map[string]time.Time
}

// lastUsedResolution limits last-use writes to one per client per minute.
const lastUsedResolution = time.Minute

// New prepares the authenticator, generating and printing an admin token
// on first start when none is configured.
func New(ctx context.Context, st *store.Store, opts Options) (*Authenticator, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	a := &Authenticator{st: st, log: opts.Log, lastSeen: map[string]time.Time{}}
	for _, k := range opts.ConfigKeys {
		if k = strings.TrimSpace(k); k != "" {
			a.configKeys = append(a.configKeys, Hash(k))
		}
	}
	if len(a.configKeys) > 0 {
		opts.Log.Warn("the api_keys setting is deprecated: create clients in the admin instead; these keys keep full access until removed")
	}
	if t := strings.TrimSpace(opts.AdminToken); t != "" {
		a.adminHash = Hash(t)
		return a, nil
	}
	row, err := st.GetAdminToken(ctx)
	switch {
	case err == nil:
		a.adminHash = row.TokenHash
		return a, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	token, err := ResetAdminToken(ctx, st)
	if err != nil {
		return nil, err
	}
	a.adminHash = Hash(token)
	PrintAdminToken(opts.Out, token)
	return a, nil
}

// ResetAdminToken generates a new admin token and stores its hash. The old
// token stops working when meta restarts.
func ResetAdminToken(ctx context.Context, st *store.Store) (string, error) {
	token, err := NewToken(AdminTokenPrefix)
	if err != nil {
		return "", err
	}
	if err := st.SetAdminToken(ctx, Hash(token)); err != nil {
		return "", err
	}
	return token, nil
}

// PrintAdminToken shows a generated token in a banner.
func PrintAdminToken(w io.Writer, token string) {
	if w == nil {
		return
	}
	line := strings.Repeat("=", 72)
	_, _ = fmt.Fprintf(w, "%s\n Generated admin token (shown only now, stored only as a hash):\n   %s\n Set META_ADMIN_TOKEN to use your own token instead.\n%s\n", line, token, line)
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(header string) string {
	header = strings.TrimSpace(header)
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

// Authenticate resolves a bearer token to a principal.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	h := Hash(token)
	if subtle.ConstantTimeCompare(h, a.adminHash) == 1 {
		return Principal{Kind: KindAdmin, Name: "admin", Scopes: []string{ScopeAdmin}}, nil
	}
	for _, k := range a.configKeys {
		if subtle.ConstantTimeCompare(h, k) == 1 {
			return Principal{Kind: KindConfigKey, Name: "api_keys setting", Scopes: []string{ScopeAdmin}}, nil
		}
	}
	if !strings.HasPrefix(token, ClientKeyPrefix) {
		return Principal{}, ErrUnauthenticated
	}
	c, err := a.st.GetClientByKeyHash(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	a.touch(ctx, c)
	return Principal{Kind: KindClient, ClientID: c.ID, Name: c.Name, Scopes: c.Scopes}, nil
}

func (a *Authenticator) touch(ctx context.Context, c db.Client) {
	now := time.Now()
	a.mu.Lock()
	last, ok := a.lastSeen[c.ID]
	if ok && now.Sub(last) < lastUsedResolution {
		a.mu.Unlock()
		return
	}
	a.lastSeen[c.ID] = now
	a.mu.Unlock()
	if err := a.st.TouchClient(context.WithoutCancel(ctx), db.TouchClientParams{ID: c.ID, UsedAt: &now}); err != nil {
		a.log.Warn("record client use", "client", c.ID, "err", err)
	}
}
