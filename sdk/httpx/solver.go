// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

// peekLimit is how much of a 403/503 body is read to recognize a challenge.
const peekLimit = 64 << 10

var challengeMarkers = []string{
	"<title>just a moment...</title>",
	"challenge-platform",
	"cf-chl",
	"cf_chl_opt",
	"cf-browser-verification",
	"<title>attention required! | cloudflare</title>",
}

// isChallenge recognizes an anti-bot challenge: a 403 or 503 with
// Cloudflare's markers in the headers or the page. It leaves the body
// readable from the start.
func isChallenge(resp *http.Response) (bool, error) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false, nil
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Cf-Mitigated")), "challenge") {
		return true, nil
	}
	server := strings.ToLower(strings.TrimSpace(resp.Header.Get("Server")))
	if server == "cloudflare" || server == "cloudflare-nginx" {
		return true, nil
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, peekLimit))
	if err != nil {
		_ = resp.Body.Close()
		return false, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	lower := strings.ToLower(string(head))
	for _, m := range challengeMarkers {
		if strings.Contains(lower, m) {
			return true, nil
		}
	}
	return false, nil
}

type solver struct {
	c      *Client
	flight singleflight.Group
	// agents holds the solver's user agent per host after a solve.
	agents sync.Map
}

type solveRequest struct {
	Cmd        string      `json:"cmd"`
	URL        string      `json:"url"`
	MaxTimeout int64       `json:"maxTimeout"`
	Proxy      *solveProxy `json:"proxy,omitempty"`
}

type solveProxy struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type solveResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution *struct {
		URL       string `json:"url"`
		Status    int    `json:"status"`
		UserAgent string `json:"userAgent"`
		Response  string `json:"response"`
		Cookies   []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
		} `json:"cookies"`
	} `json:"solution"`
}

// handle passes a challenge with the profile's solver and retries the
// request once.
func (s *solver) handle(st *state, req *http.Request, resp *http.Response) (*http.Response, error) {
	_ = resp.Body.Close()
	host := req.URL.Hostname()
	sv := st.profile.GetSolver()
	if sv == nil || sv.GetUrl() == "" {
		s.c.opts.Log.Info("anti-bot challenge and no solver in the profile", "host", host, "profile", st.profile.GetName())
		return nil, &ChallengeError{Host: host, Reason: "add a solver to a network profile and assign it to this provider (or make it the default profile)"}
	}
	key := sv.GetUrl() + "|" + host
	v, err, _ := s.flight.Do(key, func() (any, error) { return s.solve(st, sv, req) })
	if err != nil {
		return nil, &ChallengeError{Host: host, Reason: "not solved: " + err.Error()}
	}
	sol := v.(*solveResponse)

	// Retry once with the solver's cookies and user agent.
	retry := req.Clone(req.Context())
	retry.Header.Del("Cookie")
	for _, ck := range s.c.jar.Cookies(req.URL) {
		retry.AddCookie(ck)
	}
	if ua := sol.Solution.UserAgent; ua != "" {
		retry.Header.Set("User-Agent", ua)
	}
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		retry.Body = body
	}
	again, err := st.transport.RoundTrip(retry)
	if err != nil {
		return nil, err
	}
	still, err := isChallenge(again)
	if err != nil {
		return nil, err
	}
	if still && sv.GetAsResponseFallback() && sol.Solution.Response != "" {
		_ = again.Body.Close()
		status := sol.Solution.Status
		if status < 200 || status > 299 {
			status = http.StatusOK
		}
		return &http.Response{
			Status:        fmt.Sprintf("%d OK (from solver)", status),
			StatusCode:    status,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			Body:          io.NopCloser(strings.NewReader(sol.Solution.Response)),
			ContentLength: int64(len(sol.Solution.Response)),
			Request:       req,
		}, nil
	}
	return again, nil
}

func (s *solver) solve(st *state, sv *metadatav1.Solver, req *http.Request) (*solveResponse, error) {
	maxTimeout := sv.GetMaxTimeout().AsDuration()
	if maxTimeout <= 0 {
		maxTimeout = time.Minute
	}
	body := solveRequest{Cmd: "request.get", URL: req.URL.String(), MaxTimeout: maxTimeout.Milliseconds()}
	// Clearance is bound to the client's IP, so the solver must use the
	// same proxy.
	if px := st.profile.GetProxy(); px != nil && px.GetUrl() != "" {
		body.Proxy = &solveProxy{URL: px.GetUrl(), Username: px.GetUsername(), Password: px.GetPassword()}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), maxTimeout+15*time.Second)
	defer cancel()
	sreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(sv.GetUrl(), "/")+"/v1", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	sreq.Header.Set("Content-Type", "application/json")
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		return nil, err
	}
	defer sresp.Body.Close()
	var out solveResponse
	if err := json.NewDecoder(io.LimitReader(sresp.Body, 32<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("solver answered with something else than JSON (HTTP %d)", sresp.StatusCode)
	}
	if out.Status != "ok" || out.Solution == nil {
		return nil, fmt.Errorf("solver: %s %s", out.Status, out.Message)
	}
	host := req.URL.Hostname()
	if out.Solution.UserAgent != "" {
		s.agents.Store(host, out.Solution.UserAgent)
	}
	var cookies []*http.Cookie
	for _, ck := range out.Solution.Cookies {
		c := &http.Cookie{ //nolint:gosec // the attributes are the site's, as the solver saw them
			Name: ck.Name, Value: ck.Value, Path: ck.Path, Domain: strings.TrimPrefix(ck.Domain, "."),
			HttpOnly: ck.HTTPOnly, Secure: ck.Secure,
		}
		if c.Path == "" {
			c.Path = "/"
		}
		if c.Domain == "" {
			c.Domain = host
		}
		if ck.Expires > 0 {
			c.Expires = time.Unix(int64(ck.Expires), 0)
		}
		cookies = append(cookies, c)
	}
	s.c.jar.SetCookies(&url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: "/"}, cookies)
	return &out, nil
}
