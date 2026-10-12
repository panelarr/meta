// SPDX-License-Identifier: Apache-2.0

// Package httpx gives metadata plugins an HTTP client that follows the
// network profile their host hands them (SetNetworkProfile): the proxy
// (http, https or socks5, with credentials), a challenge solver
// (FlareSolverr/Byparr), a rate limit, a concurrency cap, the user agent,
// extra headers, timeouts and a DNS-over-HTTPS resolver.
//
// Create one Client, use Client.HTTP() for every request, and call
// SetProfile whenever the host sends a profile. The *http.Client stays the
// same; requests made after SetProfile use the new settings.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
)

// Options configure a Client.
type Options struct {
	// UserAgent is the plugin's own user agent, used unless the profile
	// says otherwise.
	UserAgent string
	// Defaults when the profile sets no timeout. Zero means none.
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	CallTimeout    time.Duration
	Log            *slog.Logger
}

// Client is an HTTP client that follows a network profile.
type Client struct {
	opts   Options
	jar    http.CookieJar
	http   *http.Client
	state  atomic.Pointer[state]
	solver *solver
}

// state is everything built from one profile.
type state struct {
	profile   *metadatav1.NetworkProfile
	transport *http.Transport
	limit     *limiter
	sem       chan struct{}
	call      time.Duration
}

// New returns a client with a direct connection until SetProfile is called.
func New(opts Options) *Client {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	jar, _ := cookiejar.New(nil)
	c := &Client{opts: opts, jar: jar}
	c.solver = &solver{c: c}
	c.http = &http.Client{Transport: roundTripper{c}, Jar: jar}
	if err := c.SetProfile(nil); err != nil {
		panic(err) // a nil profile always builds
	}
	return c
}

// HTTP returns the client to make requests with. It is the same value for
// the life of the Client.
func (c *Client) HTTP() *http.Client { return c.http }

// Profile returns the profile in effect (nil when direct). Don't change it.
func (c *Client) Profile() *metadatav1.NetworkProfile { return c.state.Load().profile }

var proxySchemes = map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}

// SetProfile switches to a profile; nil means a direct connection with the
// plugin's defaults. Connections made under the old profile are closed.
func (c *Client) SetProfile(p *metadatav1.NetworkProfile) error {
	st := &state{call: c.opts.CallTimeout}
	if p != nil {
		st.profile = proto.Clone(p).(*metadatav1.NetworkProfile)
	}
	dialer := &net.Dialer{Timeout: or(p.GetTimeouts().GetConnect().AsDuration(), c.opts.ConnectTimeout), KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: or(p.GetTimeouts().GetRead().AsDuration(), c.opts.ReadTimeout),
	}
	if px := p.GetProxy(); px != nil && strings.TrimSpace(px.GetUrl()) != "" {
		u, err := url.Parse(strings.TrimSpace(px.GetUrl()))
		if err != nil || !proxySchemes[u.Scheme] || u.Hostname() == "" {
			return fmt.Errorf("httpx: proxy url must be http://, https:// or socks5:// with a host")
		}
		if px.GetUsername() != "" {
			u.User = url.UserPassword(px.GetUsername(), px.GetPassword())
		}
		// The transport speaks HTTP CONNECT and SOCKS5 (with username and
		// password), and lets the proxy resolve host names.
		tr.Proxy = http.ProxyURL(u)
	} else if doh := strings.TrimSpace(p.GetDohUrl()); doh != "" {
		// Without a proxy, names are resolved over HTTPS. (Through a proxy,
		// the proxy resolves them.)
		r, err := newDoH(doh, dialer)
		if err != nil {
			return err
		}
		tr.DialContext = r.dialContext
	}
	if d := p.GetTimeouts().GetCall().AsDuration(); d > 0 {
		st.call = d
	}
	if rl := p.GetRateLimit(); rl != nil && rl.GetRequests() > 0 && rl.GetPeriod().AsDuration() > 0 {
		st.limit = newLimiter(rl)
	}
	if n := p.GetConcurrency(); n > 0 {
		st.sem = make(chan struct{}, n)
	}
	st.transport = tr
	if old := c.state.Swap(st); old != nil {
		old.transport.CloseIdleConnections()
	}
	return nil
}

func or(a, b time.Duration) time.Duration {
	if a > 0 {
		return a
	}
	return b
}

// limiter is a rate limit for the whole provider or per target host.
type limiter struct {
	perHost bool
	every   rate.Limit
	burst   int
	mu      sync.Mutex
	all     *rate.Limiter
	hosts   map[string]*rate.Limiter
}

func newLimiter(rl *metadatav1.NetworkRateLimit) *limiter {
	l := &limiter{
		perHost: rl.GetPerHost(),
		every:   rate.Every(rl.GetPeriod().AsDuration() / time.Duration(rl.GetRequests())),
		burst:   1 + int(rl.GetBurst()),
		hosts:   map[string]*rate.Limiter{},
	}
	l.all = rate.NewLimiter(l.every, l.burst)
	return l
}

func (l *limiter) wait(ctx context.Context, host string) error {
	if !l.perHost {
		return l.all.Wait(ctx)
	}
	l.mu.Lock()
	h, ok := l.hosts[host]
	if !ok {
		h = rate.NewLimiter(l.every, l.burst)
		l.hosts[host] = h
	}
	l.mu.Unlock()
	return h.Wait(ctx)
}

// desktopAgents are picked from for the "random desktop" user agent mode,
// stable per profile name.
var desktopAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:143.0) Gecko/20100101 Firefox/143.0",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
}

// userAgent decides the user agent for a request to host.
func (c *Client) userAgent(p *metadatav1.NetworkProfile, host string) string {
	ua := p.GetUserAgent()
	if _, fixed := ua.GetMode().(*metadatav1.UserAgent_Fixed); fixed && ua.GetFixed() != "" {
		return ua.GetFixed()
	}
	// After a solve, the solver's user agent goes with its cookies.
	if v, ok := c.solver.agents.Load(host); ok {
		return v.(string)
	}
	if ua.GetRandomDesktop() {
		h := fnv.New32a()
		_, _ = h.Write([]byte(p.GetName()))
		return desktopAgents[h.Sum32()%uint32(len(desktopAgents))]
	}
	return c.opts.UserAgent
}

type roundTripper struct{ c *Client }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.c.roundTrip(req)
}

// bodyCloser runs done when the response body is closed.
type bodyCloser struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *bodyCloser) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}

func (c *Client) roundTrip(req *http.Request) (*http.Response, error) {
	st := c.state.Load()
	p := st.profile
	ctx := req.Context()
	var cleanup []func()
	finish := func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}
	if st.call > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, st.call)
		cleanup = append(cleanup, cancel)
	}
	req = req.Clone(ctx)
	host := req.URL.Hostname()

	// The plugin's own user agent (or none) is replaced by the profile's;
	// a different one set on purpose by the plugin stays.
	if cur := req.Header.Get("User-Agent"); cur == "" || cur == c.opts.UserAgent {
		if ua := c.userAgent(p, host); ua != "" {
			req.Header.Set("User-Agent", ua)
		}
	}
	for k, v := range p.GetHeaders() {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	if st.sem != nil {
		select {
		case st.sem <- struct{}{}:
			cleanup = append(cleanup, func() { <-st.sem })
		case <-ctx.Done():
			finish()
			return nil, ctx.Err()
		}
	}
	if st.limit != nil {
		if err := st.limit.wait(ctx, host); err != nil {
			finish()
			return nil, err
		}
	}
	resp, err := st.transport.RoundTrip(req)
	if err != nil {
		finish()
		return nil, err
	}
	if challenged, err := isChallenge(resp); err != nil {
		finish()
		return nil, err
	} else if challenged {
		resp, err = c.solver.handle(st, req, resp)
		if err != nil {
			finish()
			return nil, err
		}
	} else if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		c.opts.Log.Info("HTTP error doesn't look like a challenge; not solving",
			"status", resp.StatusCode, "host", host, "server", resp.Header.Get("Server"))
	}
	resp.Body = &bodyCloser{ReadCloser: resp.Body, done: finish}
	return resp, nil
}

// ChallengeError means a site answered with an anti-bot challenge that
// couldn't be passed.
type ChallengeError struct {
	Host   string
	Reason string
}

func (e *ChallengeError) Error() string {
	return fmt.Sprintf("%s answered with an anti-bot challenge; %s", e.Host, e.Reason)
}

// IsChallenge reports whether err is (or wraps) a ChallengeError.
func IsChallenge(err error) bool {
	var ce *ChallengeError
	return errors.As(err, &ce)
}

// SetNetworkProfile implements the plugin RPC of the same name, so a plugin
// can pass it straight through:
//
//	func (p *Plugin) SetNetworkProfile(ctx context.Context, r *metadatav1.SetNetworkProfileRequest) (*metadatav1.SetNetworkProfileResponse, error) {
//		return p.net.SetNetworkProfile(ctx, r)
//	}
func (c *Client) SetNetworkProfile(_ context.Context, req *metadatav1.SetNetworkProfileRequest) (*metadatav1.SetNetworkProfileResponse, error) {
	if err := c.SetProfile(req.GetProfile()); err != nil {
		return nil, plugin.ErrInvalidArgument("%v", err)
	}
	c.opts.Log.Info("network profile applied", "profile", req.GetProfile().GetName(), "proxy", req.GetProfile().GetProxy().GetUrl() != "", "solver", req.GetProfile().GetSolver().GetUrl() != "")
	return &metadatav1.SetNetworkProfileResponse{}, nil
}
