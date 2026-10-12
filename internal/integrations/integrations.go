// SPDX-License-Identifier: GPL-3.0-or-later

// Package integrations manages the network settings of metadata plugins:
// challenge solvers (FlareSolverr, Byparr), network profiles (proxy, solver,
// rate limit, user agent, headers, timeouts), the default profile, a profile
// per provider, and profiles pushed by a host.
//
// A provider uses, in order: a pushed profile, its assigned profile, the
// default profile, or a direct connection.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/secret"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Errors map to API status codes.
var (
	ErrInvalid   = errors.New("invalid")
	ErrNotFound  = errors.New("not found")
	ErrInUse     = errors.New("in use")
	ErrNoSecrets = errors.New("secrets unavailable")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// ProviderInfo is what the service needs to know about a configured
// provider.
type ProviderInfo struct {
	ID string
	// Supported: the plugin declares the network_profile common service.
	Supported bool
}

// Service manages integrations.
type Service struct {
	st        *store.Store
	box       *secret.Box
	providers func() []ProviderInfo
	// OnChange is called after any change, so the host can hand the new
	// profiles to plugins. It must not block for long.
	OnChange func()
}

// New returns the service. providers lists the configured providers.
func New(st *store.Store, box *secret.Box, providers func() []ProviderInfo) *Service {
	return &Service{st: st, box: box, providers: providers}
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func checkID(kind, id string) error {
	if !idPattern.MatchString(id) {
		return invalid("%s id %q: use lowercase letters, digits and dashes (max 63)", kind, id)
	}
	return nil
}

var pj = protojson.MarshalOptions{}

// ---- solvers -------------------------------------------------------------

func (s *Service) solverProto(ctx context.Context, row db.Solver) (*metav1.Solver, error) {
	sv := &metav1.Solver{}
	if err := protojson.Unmarshal(row.Settings, sv); err != nil {
		return nil, err
	}
	sv.Id = row.ID
	used, err := s.st.ProfilesUsingSolver(ctx, pgtype.Text{String: row.ID, Valid: true})
	if err != nil {
		return nil, err
	}
	sv.UsedBy = used
	return sv, nil
}

// ListSolvers lists solvers, each with the profiles that use it.
func (s *Service) ListSolvers(ctx context.Context) ([]*metav1.Solver, error) {
	rows, err := s.st.ListSolvers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*metav1.Solver, 0, len(rows))
	for _, r := range rows {
		sv, err := s.solverProto(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, nil
}

func checkHTTPURL(what, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s must be an absolute http(s) URL", what)
	}
	return nil
}

// PutSolver creates or replaces a solver. When there are no profiles and no
// default yet, it also creates a "default" profile using the solver and makes
// it the default, so the solver takes effect at once; the ID of that profile
// is returned.
func (s *Service) PutSolver(ctx context.Context, sv *metav1.Solver) (*metav1.Solver, string, error) {
	if sv == nil {
		return nil, "", invalid("solver is required")
	}
	if err := checkID("solver", sv.GetId()); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(sv.GetName()) == "" {
		return nil, "", invalid("name is required")
	}
	if err := checkHTTPURL("url", sv.GetUrl()); err != nil {
		return nil, "", err
	}
	if sv.GetSessionTtl().AsDuration() < 0 || sv.GetMaxTimeout().AsDuration() < 0 {
		return nil, "", invalid("durations can't be negative")
	}
	clean := proto.Clone(sv).(*metav1.Solver)
	clean.Id, clean.UsedBy = "", nil
	clean.Name = strings.TrimSpace(clean.GetName())
	clean.Url = strings.TrimRight(strings.TrimSpace(clean.GetUrl()), "/")
	if clean.GetType() == metav1.SolverType_SOLVER_TYPE_UNSPECIFIED {
		clean.Type = metav1.SolverType_SOLVER_TYPE_FLARESOLVERR
	}
	settings, err := pj.Marshal(clean)
	if err != nil {
		return nil, "", err
	}

	var created string
	err = s.st.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		_, getErr := q.GetSolver(ctx, sv.GetId())
		isNew := errors.Is(getErr, pgx.ErrNoRows)
		if getErr != nil && !isNew {
			return getErr
		}
		if err := q.PutSolver(ctx, db.PutSolverParams{ID: sv.GetId(), Settings: settings}); err != nil {
			return err
		}
		if !isNew {
			return nil
		}
		n, err := q.CountProfiles(ctx)
		if err != nil {
			return err
		}
		def, err := q.GetDefaultProfileID(ctx)
		if err != nil {
			return err
		}
		if n > 0 || def.Valid {
			return nil
		}
		// The solver would otherwise do nothing: wire it up.
		p := &metav1.NetworkProfile{Name: "Default", UserAgentMode: metav1.UserAgentMode_USER_AGENT_MODE_FROM_SOLVER}
		ps, err := pj.Marshal(p)
		if err != nil {
			return err
		}
		if err := q.PutProfile(ctx, db.PutProfileParams{ID: "default", SolverID: pgtype.Text{String: sv.GetId(), Valid: true}, Settings: ps}); err != nil {
			return err
		}
		created = "default"
		return q.SetDefaultProfileID(ctx, pgtype.Text{String: "default", Valid: true})
	})
	if err != nil {
		return nil, "", err
	}
	s.changed()
	row, err := s.st.GetSolver(ctx, sv.GetId())
	if err != nil {
		return nil, "", err
	}
	out, err := s.solverProto(ctx, row)
	return out, created, err
}

// DeleteSolver deletes a solver that no profile uses.
func (s *Service) DeleteSolver(ctx context.Context, id string) error {
	used, err := s.st.ProfilesUsingSolver(ctx, pgtype.Text{String: id, Valid: true})
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return fmt.Errorf("%w: solver %q is used by profiles: %s", ErrInUse, id, strings.Join(used, ", "))
	}
	n, err := s.st.DeleteSolver(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: no solver %q", ErrNotFound, id)
	}
	s.changed()
	return nil
}

// ---- profiles ------------------------------------------------------------

func profileProto(row db.NetworkProfile) (*metav1.NetworkProfile, error) {
	p := &metav1.NetworkProfile{}
	if err := protojson.Unmarshal(row.Settings, p); err != nil {
		return nil, err
	}
	p.Id = row.ID
	p.SolverId = row.SolverID.String
	if p.GetProxy() != nil {
		p.Proxy.Password = ""
		p.Proxy.HasPassword = row.ProxyPasswordSealed.Valid
	}
	return p, nil
}

// ListProfiles lists profiles (passwords never included) and the default.
func (s *Service) ListProfiles(ctx context.Context) ([]*metav1.NetworkProfile, string, error) {
	rows, err := s.st.ListProfiles(ctx)
	if err != nil {
		return nil, "", err
	}
	out := make([]*metav1.NetworkProfile, 0, len(rows))
	for _, r := range rows {
		p, err := profileProto(r)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	def, err := s.st.GetDefaultProfileID(ctx)
	return out, def.String, err
}

var proxySchemes = map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}

// PutProfile creates or replaces a profile. An empty proxy password keeps
// the stored one (as long as the username stays); clearing the username or
// the proxy drops it.
func (s *Service) PutProfile(ctx context.Context, p *metav1.NetworkProfile) (*metav1.NetworkProfile, error) {
	if p == nil {
		return nil, invalid("profile is required")
	}
	if err := checkID("profile", p.GetId()); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.GetName()) == "" {
		return nil, invalid("name is required")
	}
	clean := proto.Clone(p).(*metav1.NetworkProfile)
	clean.Name = strings.TrimSpace(clean.GetName())
	var solver pgtype.Text
	if id := strings.TrimSpace(p.GetSolverId()); id != "" {
		if _, err := s.st.GetSolver(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return nil, invalid("no solver %q", id)
		} else if err != nil {
			return nil, err
		}
		solver = pgtype.Text{String: id, Valid: true}
	}
	if rl := clean.GetRateLimit(); rl != nil {
		if rl.GetRequests() == 0 {
			clean.RateLimit = nil
		} else if rl.GetPeriod().AsDuration() <= 0 {
			return nil, invalid("a rate limit needs a period")
		}
	}
	for _, d := range []*durationpb.Duration{clean.GetConnectTimeout(), clean.GetReadTimeout(), clean.GetCallTimeout()} {
		if d.AsDuration() < 0 {
			return nil, invalid("timeouts can't be negative")
		}
	}
	for name := range clean.GetHeaders() {
		if !validHeaderName(name) {
			return nil, invalid("header name %q is not valid", name)
		}
	}
	if clean.GetUserAgentMode() == metav1.UserAgentMode_USER_AGENT_MODE_FIXED && strings.TrimSpace(clean.GetUserAgent()) == "" {
		return nil, invalid("a fixed user agent needs a value")
	}

	prev, err := s.st.GetProfile(ctx, p.GetId())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var sealed pgtype.Text
	if px := clean.GetProxy(); px != nil && strings.TrimSpace(px.GetUrl()) != "" {
		u, err := url.Parse(strings.TrimSpace(px.GetUrl()))
		if err != nil || !proxySchemes[u.Scheme] || u.Hostname() == "" {
			return nil, invalid("proxy url must be http://, https:// or socks5:// with a host")
		}
		if u.User != nil {
			return nil, invalid("put proxy credentials in the username and password fields, not in the URL")
		}
		px.Url = u.String()
		px.Username = strings.TrimSpace(px.GetUsername())
		switch {
		case px.GetUsername() == "":
			// No username: no password.
		case px.GetPassword() != "":
			v, err := s.box.Seal(px.GetPassword())
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrNoSecrets, err)
			}
			sealed = pgtype.Text{String: v, Valid: true}
		default:
			sealed = prev.ProxyPasswordSealed // keep the stored one, if any
		}
		px.Password, px.HasPassword = "", false
	} else {
		clean.Proxy = nil
	}
	stored := proto.Clone(clean).(*metav1.NetworkProfile)
	stored.Id, stored.SolverId = "", ""
	settings, err := pj.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := s.st.PutProfile(ctx, db.PutProfileParams{ID: p.GetId(), SolverID: solver, Settings: settings, ProxyPasswordSealed: sealed}); err != nil {
		return nil, err
	}
	s.changed()
	row, err := s.st.GetProfile(ctx, p.GetId())
	if err != nil {
		return nil, err
	}
	return profileProto(row)
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > 126 || r <= 32 || strings.ContainsRune(`()<>@,;:\"/[]?={}`, r) {
			return false
		}
	}
	return http.CanonicalHeaderKey(name) != ""
}

// DeleteProfile deletes a profile, its assignments, and the default when it
// was the default.
func (s *Service) DeleteProfile(ctx context.Context, id string) error {
	n, err := s.st.DeleteProfile(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: no profile %q", ErrNotFound, id)
	}
	s.changed()
	return nil
}

// SetDefaultProfile sets the default profile; empty means none.
func (s *Service) SetDefaultProfile(ctx context.Context, id string) error {
	var v pgtype.Text
	if id != "" {
		if _, err := s.st.GetProfile(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no profile %q", ErrNotFound, id)
		} else if err != nil {
			return err
		}
		v = pgtype.Text{String: id, Valid: true}
	}
	if err := s.st.SetDefaultProfileID(ctx, v); err != nil {
		return err
	}
	s.changed()
	return nil
}

// ---- assignments ---------------------------------------------------------

type state struct {
	assigned map[string]string
	pushed   map[string]db.PushedProfile
	def      string
}

func (s *Service) load(ctx context.Context) (state, error) {
	st := state{assigned: map[string]string{}, pushed: map[string]db.PushedProfile{}}
	rows, err := s.st.ListAssignments(ctx)
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		st.assigned[r.Provider] = r.ProfileID
	}
	pushed, err := s.st.ListPushedProfiles(ctx)
	if err != nil {
		return st, err
	}
	for _, p := range pushed {
		st.pushed[p.Provider] = p
	}
	def, err := s.st.GetDefaultProfileID(ctx)
	st.def = def.String
	return st, err
}

func (st state) assignment(p ProviderInfo) *metav1.Assignment {
	a := &metav1.Assignment{ProviderId: p.ID, ProfileId: st.assigned[p.ID], Supported: p.Supported}
	switch {
	case st.pushed[p.ID].Provider != "":
		pp := st.pushed[p.ID]
		// Stored without the proxy password, which stays sealed.
		var np metadatav1.NetworkProfile
		_ = protojson.Unmarshal(pp.Profile, &np)
		a.PushedProfileName = np.GetName()
		a.PushedProfile = &np
		a.PushedProxyPasswordSet = pp.ProxyPasswordSealed.Valid
		a.PushedAt = timestamppb.New(pp.UpdatedAt)
		a.Effective = metav1.ProfileOrigin_PROFILE_ORIGIN_PUSHED
	case a.ProfileId != "":
		a.Effective = metav1.ProfileOrigin_PROFILE_ORIGIN_ASSIGNED
		a.EffectiveProfileId = a.ProfileId
	case st.def != "":
		a.Effective = metav1.ProfileOrigin_PROFILE_ORIGIN_DEFAULT
		a.EffectiveProfileId = st.def
	default:
		a.Effective = metav1.ProfileOrigin_PROFILE_ORIGIN_NONE
	}
	return a
}

// ListAssignments describes the profile of every configured provider.
func (s *Service) ListAssignments(ctx context.Context) ([]*metav1.Assignment, string, error) {
	st, err := s.load(ctx)
	if err != nil {
		return nil, "", err
	}
	var out []*metav1.Assignment
	for _, p := range s.providers() {
		out = append(out, st.assignment(p))
	}
	return out, st.def, nil
}

func (s *Service) provider(id string) (ProviderInfo, bool) {
	i := slices.IndexFunc(s.providers(), func(p ProviderInfo) bool { return p.ID == id })
	if i < 0 {
		return ProviderInfo{}, false
	}
	return s.providers()[i], true
}

// AssignProfile sets (or with an empty id, removes) a provider's profile.
func (s *Service) AssignProfile(ctx context.Context, provider, profile string) (*metav1.Assignment, error) {
	p, ok := s.provider(provider)
	if !ok {
		return nil, fmt.Errorf("%w: no provider %q is configured", ErrNotFound, provider)
	}
	if profile == "" {
		if err := s.st.DeleteAssignment(ctx, provider); err != nil {
			return nil, err
		}
	} else {
		if _, err := s.st.GetProfile(ctx, profile); errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: no profile %q", ErrNotFound, profile)
		} else if err != nil {
			return nil, err
		}
		if err := s.st.SetAssignment(ctx, db.SetAssignmentParams{Provider: provider, ProfileID: profile}); err != nil {
			return nil, err
		}
	}
	s.changed()
	st, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return st.assignment(p), nil
}

// SetPushedProfile stores (or with nil, removes) a host's profile for a
// provider.
func (s *Service) SetPushedProfile(ctx context.Context, provider string, np *metadatav1.NetworkProfile) error {
	if _, ok := s.provider(provider); !ok {
		return fmt.Errorf("%w: no provider %q is configured", ErrNotFound, provider)
	}
	if np == nil {
		if err := s.st.DeletePushedProfile(ctx, provider); err != nil {
			return err
		}
		s.changed()
		return nil
	}
	clean := proto.Clone(np).(*metadatav1.NetworkProfile)
	var sealed pgtype.Text
	if px := clean.GetProxy(); px != nil {
		if px.GetPassword() != "" {
			v, err := s.box.Seal(px.GetPassword())
			if err != nil {
				return fmt.Errorf("%w: %w", ErrNoSecrets, err)
			}
			sealed = pgtype.Text{String: v, Valid: true}
		}
		px.Password = ""
	}
	js, err := pj.Marshal(clean)
	if err != nil {
		return err
	}
	if err := s.st.SetPushedProfile(ctx, db.SetPushedProfileParams{Provider: provider, Profile: js, ProxyPasswordSealed: sealed}); err != nil {
		return err
	}
	s.changed()
	return nil
}

// ---- resolution ----------------------------------------------------------

// Effective returns the profile a provider uses, in the shape plugins
// receive it, with secrets decrypted, and where it comes from. A nil profile
// means a direct connection with the plugin's defaults.
func (s *Service) Effective(ctx context.Context, provider string) (*metadatav1.NetworkProfile, metav1.ProfileOrigin, error) {
	st, err := s.load(ctx)
	if err != nil {
		return nil, 0, err
	}
	a := st.assignment(ProviderInfo{ID: provider})
	switch a.GetEffective() {
	case metav1.ProfileOrigin_PROFILE_ORIGIN_PUSHED:
		pp := st.pushed[provider]
		var np metadatav1.NetworkProfile
		if err := protojson.Unmarshal(pp.Profile, &np); err != nil {
			return nil, 0, err
		}
		if pp.ProxyPasswordSealed.Valid && np.GetProxy() != nil {
			pw, err := s.box.Open(pp.ProxyPasswordSealed.String)
			if err != nil {
				return nil, 0, err
			}
			np.Proxy.Password = pw
		}
		return &np, a.GetEffective(), nil
	case metav1.ProfileOrigin_PROFILE_ORIGIN_ASSIGNED, metav1.ProfileOrigin_PROFILE_ORIGIN_DEFAULT:
		np, err := s.pluginProfile(ctx, a.GetEffectiveProfileId())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, metav1.ProfileOrigin_PROFILE_ORIGIN_NONE, nil // dangling: direct
		}
		return np, a.GetEffective(), err
	default:
		return nil, metav1.ProfileOrigin_PROFILE_ORIGIN_NONE, nil
	}
}

// pluginProfile turns a stored profile into what a plugin receives.
func (s *Service) pluginProfile(ctx context.Context, id string) (*metadatav1.NetworkProfile, error) {
	row, err := s.st.GetProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := profileProto(row)
	if err != nil {
		return nil, err
	}
	np := &metadatav1.NetworkProfile{
		Name:        p.GetName(),
		Concurrency: p.GetConcurrency(),
		Headers:     p.GetHeaders(),
	}
	if px := p.GetProxy(); px != nil {
		np.Proxy = &metadatav1.Proxy{Url: px.GetUrl(), Username: px.GetUsername()}
		if row.ProxyPasswordSealed.Valid {
			pw, err := s.box.Open(row.ProxyPasswordSealed.String)
			if err != nil {
				return nil, err
			}
			np.Proxy.Password = pw
		}
	}
	if row.SolverID.Valid {
		srow, err := s.st.GetSolver(ctx, row.SolverID.String)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			var sv metav1.Solver
			if err := protojson.Unmarshal(srow.Settings, &sv); err != nil {
				return nil, err
			}
			np.Solver = &metadatav1.Solver{
				Type:               metadatav1.SolverType(sv.GetType()),
				Url:                sv.GetUrl(),
				SessionTtl:         sv.GetSessionTtl(),
				MaxTimeout:         sv.GetMaxTimeout(),
				AsResponseFallback: sv.GetAsResponseFallback(),
			}
		}
	}
	if rl := p.GetRateLimit(); rl != nil && rl.GetRequests() > 0 {
		np.RateLimit = &metadatav1.NetworkRateLimit{Requests: rl.GetRequests(), Period: rl.GetPeriod(), PerHost: rl.GetPerHost()}
	}
	switch p.GetUserAgentMode() {
	case metav1.UserAgentMode_USER_AGENT_MODE_FIXED:
		np.UserAgent = &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_Fixed{Fixed: p.GetUserAgent()}}
	case metav1.UserAgentMode_USER_AGENT_MODE_RANDOM_DESKTOP:
		np.UserAgent = &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_RandomDesktop{RandomDesktop: true}}
	case metav1.UserAgentMode_USER_AGENT_MODE_FROM_SOLVER:
		np.UserAgent = &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_FromSolver{FromSolver: true}}
	}
	if p.GetConnectTimeout() != nil || p.GetReadTimeout() != nil || p.GetCallTimeout() != nil {
		np.Timeouts = &metadatav1.Timeouts{Connect: p.GetConnectTimeout(), Read: p.GetReadTimeout(), Call: p.GetCallTimeout()}
	}
	return np, nil
}

// UnusedSolvers lists solvers no profile uses, for warnings.
func (s *Service) UnusedSolvers(ctx context.Context) ([]string, error) {
	solvers, err := s.ListSolvers(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sv := range solvers {
		if len(sv.GetUsedBy()) == 0 {
			out = append(out, sv.GetId())
		}
	}
	return out, nil
}
