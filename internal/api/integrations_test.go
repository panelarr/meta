// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

func TestSolverAutoWireAndInUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	in := f.as(testAdminToken).integrations

	put, err := in.PutSolver(ctx, connect.NewRequest(&metav1.PutSolverRequest{Solver: &metav1.Solver{Id: "fs", Name: "FlareSolverr", Url: "http://flaresolverr:8191/"}}))
	if err != nil {
		t.Fatal(err)
	}
	// With no profiles at all, the solver gets a default profile at once.
	if put.Msg.GetCreatedDefaultProfileId() != "default" || strings.Join(put.Msg.GetSolver().GetUsedBy(), ",") != "default" ||
		put.Msg.GetSolver().GetType() != metav1.SolverType_SOLVER_TYPE_FLARESOLVERR || put.Msg.GetSolver().GetUrl() != "http://flaresolverr:8191" {
		t.Fatalf("first solver: %v", put.Msg)
	}
	profiles, err := in.ListProfiles(ctx, connect.NewRequest(&metav1.ListProfilesRequest{}))
	if err != nil || profiles.Msg.GetDefaultProfileId() != "default" || profiles.Msg.GetProfiles()[0].GetUserAgentMode() != metav1.UserAgentMode_USER_AGENT_MODE_FROM_SOLVER {
		t.Fatalf("auto profile: %v %v", profiles, err)
	}
	// A second solver isn't wired: it shows up as unused.
	put, err = in.PutSolver(ctx, connect.NewRequest(&metav1.PutSolverRequest{Solver: &metav1.Solver{Id: "byparr", Name: "Byparr", Type: metav1.SolverType_SOLVER_TYPE_BYPARR, Url: "http://byparr:8191"}}))
	if err != nil || put.Msg.GetCreatedDefaultProfileId() != "" || len(put.Msg.GetSolver().GetUsedBy()) != 0 {
		t.Fatalf("second solver: %v %v", put, err)
	}
	unused, err := f.integ.UnusedSolvers(ctx)
	if err != nil || strings.Join(unused, ",") != "byparr" {
		t.Fatalf("unused: %v %v", unused, err)
	}
	// In use: can't delete. Unused: can.
	if _, err := in.DeleteSolver(ctx, connect.NewRequest(&metav1.DeleteSolverRequest{SolverId: "fs"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete used: %v", err)
	}
	if _, err := in.DeleteSolver(ctx, connect.NewRequest(&metav1.DeleteSolverRequest{SolverId: "byparr"})); err != nil {
		t.Fatalf("delete unused: %v", err)
	}
	if _, err := in.DeleteSolver(ctx, connect.NewRequest(&metav1.DeleteSolverRequest{SolverId: "byparr"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestProfilesSecretsAndResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	in := f.as(testAdminToken).integrations
	if _, err := in.PutSolver(ctx, connect.NewRequest(&metav1.PutSolverRequest{Solver: &metav1.Solver{Id: "fs", Name: "FS", Url: "http://fs:8191", MaxTimeout: durationpb.New(time.Minute)}})); err != nil {
		t.Fatal(err)
	}
	vpn := &metav1.NetworkProfile{
		Id: "vpn-nl", Name: "VPN NL", SolverId: "fs",
		Proxy:         &metav1.Proxy{Url: "socks5://gluetun:1080", Username: "me", Password: "hunter2"},
		RateLimit:     &metav1.RateLimit{Requests: 30, Period: durationpb.New(time.Minute)},
		UserAgentMode: metav1.UserAgentMode_USER_AGENT_MODE_FIXED, UserAgent: "MetaTest/1",
		Headers:        map[string]string{"X-Hello": "world"},
		ConnectTimeout: durationpb.New(5 * time.Second),
		DohUrl:         " https://dns.example/dns-query ",
	}
	res, err := in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: vpn}))
	if err != nil {
		t.Fatal(err)
	}
	if px := res.Msg.GetProfile().GetProxy(); px.GetPassword() != "" || !px.GetHasPassword() {
		t.Fatalf("password not redacted: %v", px)
	}
	// Stored encrypted.
	var sealed string
	if err := f.st.Pool.QueryRow(ctx, `SELECT proxy_password_sealed FROM network_profiles WHERE id = 'vpn-nl'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "enc:v1:") || strings.Contains(sealed, "hunter2") {
		t.Fatalf("stored password: %q", sealed)
	}
	var settings string
	_ = f.st.Pool.QueryRow(ctx, `SELECT settings::text FROM network_profiles WHERE id = 'vpn-nl'`).Scan(&settings)
	if strings.Contains(settings, "hunter2") {
		t.Fatal("password in settings")
	}
	// An update without a password keeps the stored one.
	vpn.Proxy.Password = ""
	vpn.Name = "VPN Netherlands"
	if res, err = in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: vpn})); err != nil || !res.Msg.GetProfile().GetProxy().GetHasPassword() {
		t.Fatalf("keep password: %v %v", res, err)
	}

	// Resolution: none, then default, then assigned, then pushed.
	effective := func() (*metadatav1.NetworkProfile, metav1.ProfileOrigin) {
		t.Helper()
		np, origin, err := f.integ.Effective(ctx, "fake")
		if err != nil {
			t.Fatal(err)
		}
		return np, origin
	}
	// PutSolver created "default" (no profiles existed), which is the default.
	if _, origin := effective(); origin != metav1.ProfileOrigin_PROFILE_ORIGIN_DEFAULT {
		t.Fatalf("origin: %v", origin)
	}
	if _, err := in.AssignProfile(ctx, connect.NewRequest(&metav1.AssignProfileRequest{ProviderId: "fake", ProfileId: "vpn-nl"})); err != nil {
		t.Fatal(err)
	}
	np, origin := effective()
	if origin != metav1.ProfileOrigin_PROFILE_ORIGIN_ASSIGNED || np.GetProxy().GetPassword() != "hunter2" || np.GetProxy().GetUsername() != "me" ||
		np.GetSolver().GetUrl() != "http://fs:8191" || np.GetSolver().GetMaxTimeout().AsDuration() != time.Minute ||
		np.GetRateLimit().GetRequests() != 30 || np.GetUserAgent().GetFixed() != "MetaTest/1" || np.GetHeaders()["X-Hello"] != "world" ||
		np.GetTimeouts().GetConnect().AsDuration() != 5*time.Second || np.GetName() != "VPN Netherlands" ||
		np.GetDohUrl() != "https://dns.example/dns-query" {
		t.Fatalf("assigned profile: %v", np)
	}
	if _, err := in.SetPushedProfile(ctx, connect.NewRequest(&metav1.SetPushedProfileRequest{ProviderId: "fake", Profile: &metadatav1.NetworkProfile{
		Name: "from-server", Proxy: &metadatav1.Proxy{Url: "http://proxy:3128", Username: "u", Password: "p4ss"},
	}})); err != nil {
		t.Fatal(err)
	}
	np, origin = effective()
	if origin != metav1.ProfileOrigin_PROFILE_ORIGIN_PUSHED || np.GetName() != "from-server" || np.GetProxy().GetPassword() != "p4ss" {
		t.Fatalf("pushed: %v %v", origin, np)
	}
	list, err := in.ListAssignments(ctx, connect.NewRequest(&metav1.ListAssignmentsRequest{}))
	if err != nil || len(list.Msg.GetAssignments()) != 1 || list.Msg.GetAssignments()[0].GetPushedProfileName() != "from-server" ||
		list.Msg.GetAssignments()[0].GetProfileId() != "vpn-nl" || list.Msg.GetAssignments()[0].GetSupported() {
		t.Fatalf("assignments: %v %v", list, err)
	}
	if _, err := in.SetPushedProfile(ctx, connect.NewRequest(&metav1.SetPushedProfileRequest{ProviderId: "fake"})); err != nil {
		t.Fatal(err)
	}
	// Deleting the assigned profile drops the assignment: back to the default.
	if _, err := in.DeleteProfile(ctx, connect.NewRequest(&metav1.DeleteProfileRequest{ProfileId: "vpn-nl"})); err != nil {
		t.Fatal(err)
	}
	if _, origin := effective(); origin != metav1.ProfileOrigin_PROFILE_ORIGIN_DEFAULT {
		t.Fatalf("after delete: %v", origin)
	}
	// Deleting the default profile leaves a direct connection.
	if _, err := in.DeleteProfile(ctx, connect.NewRequest(&metav1.DeleteProfileRequest{ProfileId: "default"})); err != nil {
		t.Fatal(err)
	}
	if np, origin := effective(); origin != metav1.ProfileOrigin_PROFILE_ORIGIN_NONE || np != nil {
		t.Fatalf("direct: %v %v", origin, np)
	}
}

func TestIntegrationsValidationAndScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	in := f.as(testAdminToken).integrations
	bad := []*metav1.NetworkProfile{
		{Id: "Bad ID", Name: "x"},
		{Id: "p", Name: ""},
		{Id: "p", Name: "x", SolverId: "missing"},
		{Id: "p", Name: "x", Proxy: &metav1.Proxy{Url: "http://user:pw@proxy:3128"}},
		{Id: "p", Name: "x", Proxy: &metav1.Proxy{Url: "ftp://proxy"}},
		{Id: "p", Name: "x", RateLimit: &metav1.RateLimit{Requests: 5}},
		{Id: "p", Name: "x", UserAgentMode: metav1.UserAgentMode_USER_AGENT_MODE_FIXED},
		{Id: "p", Name: "x", Headers: map[string]string{"Bad Header": "v"}},
		{Id: "p", Name: "x", DohUrl: "http://dns.example/dns-query"},
	}
	for _, p := range bad {
		if _, err := in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: p})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: %v", p, err)
		}
	}
	if _, err := in.PutSolver(ctx, connect.NewRequest(&metav1.PutSolverRequest{Solver: &metav1.Solver{Id: "s", Name: "s", Url: "not a url"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("solver url: %v", err)
	}
	if _, err := in.AssignProfile(ctx, connect.NewRequest(&metav1.AssignProfileRequest{ProviderId: "nope", ProfileId: ""})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := in.SetDefaultProfile(ctx, connect.NewRequest(&metav1.SetDefaultProfileRequest{ProfileId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown default: %v", err)
	}
	// Clearing the proxy username drops the password.
	if _, err := in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: &metav1.NetworkProfile{Id: "p", Name: "p", Proxy: &metav1.Proxy{Url: "http://proxy:3128", Username: "u", Password: "x"}}})); err != nil {
		t.Fatal(err)
	}
	res, err := in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: &metav1.NetworkProfile{Id: "p", Name: "p", Proxy: &metav1.Proxy{Url: "http://proxy:3128"}}}))
	if err != nil || res.Msg.GetProfile().GetProxy().GetHasPassword() {
		t.Fatalf("username cleared: %v %v", res, err)
	}

	// A catalogue key can't touch integrations.
	created, err := f.as(testAdminToken).clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "r", Scopes: []string{"catalog:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.as(created.Msg.GetApiKey()).integrations.ListProfiles(ctx, connect.NewRequest(&metav1.ListProfilesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read key: %v", err)
	}
}
