// SPDX-License-Identifier: Apache-2.0

package httpx_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/httpx"
)

func get(t *testing.T, c *httpx.Client, url string, hdr ...string) (int, string, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.HTTP().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), nil
}

func echo() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ua=%s x=%s", r.UserAgent(), r.Header.Get("X-Extra"))
	}))
}

func TestHTTPProxyWithCredentials(t *testing.T) {
	target := echo()
	defer target.Close()
	var seen atomic.Int32
	var auth atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		auth.Store(r.Header.Get("Proxy-Authorization"))
		// A forward proxy: the request line carries the absolute URL.
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out.Header = r.Header.Clone()
		res, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	defer proxy.Close()

	c := httpx.New(httpx.Options{UserAgent: "plugin/1"})
	if err := c.SetProfile(&metadatav1.NetworkProfile{Name: "p", Proxy: &metadatav1.Proxy{Url: proxy.URL, Username: "me", Password: "pw"}}); err != nil {
		t.Fatal(err)
	}
	code, body, err := get(t, c, target.URL)
	if err != nil || code != 200 || seen.Load() != 1 {
		t.Fatalf("via proxy: %d %q %v (proxy saw %d)", code, body, err, seen.Load())
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me:pw"))
	if auth.Load() != want {
		t.Fatalf("proxy auth: %v", auth.Load())
	}
	// Back to direct.
	if err := c.SetProfile(nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(t, c, target.URL); err != nil || seen.Load() != 1 {
		t.Fatalf("direct went through the proxy: %v", err)
	}
	if err := c.SetProfile(&metadatav1.NetworkProfile{Proxy: &metadatav1.Proxy{Url: "ftp://x"}}); err == nil {
		t.Fatal("bad proxy accepted")
	}
}

// socks5 is a minimal SOCKS5 server with username/password auth.
func socks5(t *testing.T, user, pass string) (addr string, used *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	used = &atomic.Int32{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 512)
				// Greeting: must offer username/password (0x02).
				if _, err := io.ReadFull(conn, buf[:2]); err != nil {
					return
				}
				methods := make([]byte, buf[1])
				_, _ = io.ReadFull(conn, methods)
				if !strings.ContainsRune(string(methods), 2) {
					_, _ = conn.Write([]byte{5, 0xff})
					return
				}
				_, _ = conn.Write([]byte{5, 2})
				// RFC 1929 auth.
				_, _ = io.ReadFull(conn, buf[:2])
				u := make([]byte, buf[1])
				_, _ = io.ReadFull(conn, u)
				_, _ = io.ReadFull(conn, buf[:1])
				pw := make([]byte, buf[0])
				_, _ = io.ReadFull(conn, pw)
				if string(u) != user || string(pw) != pass {
					_, _ = conn.Write([]byte{1, 1})
					return
				}
				_, _ = conn.Write([]byte{1, 0})
				// CONNECT request.
				_, _ = io.ReadFull(conn, buf[:4])
				var host string
				switch buf[3] {
				case 1:
					_, _ = io.ReadFull(conn, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					_, _ = io.ReadFull(conn, buf[:1])
					h := make([]byte, buf[0])
					_, _ = io.ReadFull(conn, h)
					host = string(h)
				default:
					return
				}
				_, _ = io.ReadFull(conn, buf[:2])
				port := binary.BigEndian.Uint16(buf[:2])
				up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
				if err != nil {
					_, _ = conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				used.Add(1)
				_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go func() { _, _ = io.Copy(up, conn) }()
				_, _ = io.Copy(conn, up)
			}()
		}
	}()
	return ln.Addr().String(), used
}

func TestSOCKS5ProxyWithCredentials(t *testing.T) {
	target := echo()
	defer target.Close()
	addr, used := socks5(t, "alice", "s3cret")
	c := httpx.New(httpx.Options{})
	if err := c.SetProfile(&metadatav1.NetworkProfile{Proxy: &metadatav1.Proxy{Url: "socks5://" + addr, Username: "alice", Password: "s3cret"}}); err != nil {
		t.Fatal(err)
	}
	if code, _, err := get(t, c, target.URL); err != nil || code != 200 || used.Load() != 1 {
		t.Fatalf("socks5: %d %v (used %d)", code, err, used.Load())
	}
	if err := c.SetProfile(&metadatav1.NetworkProfile{Proxy: &metadatav1.Proxy{Url: "socks5://" + addr, Username: "alice", Password: "wrong"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(t, c, target.URL); err == nil {
		t.Fatal("wrong socks5 password worked")
	}
}

func TestUserAgentAndHeaders(t *testing.T) {
	target := echo()
	defer target.Close()
	c := httpx.New(httpx.Options{UserAgent: "plugin/1"})
	if _, body, _ := get(t, c, target.URL); body != "ua=plugin/1 x=" {
		t.Fatalf("default: %q", body)
	}
	_ = c.SetProfile(&metadatav1.NetworkProfile{Headers: map[string]string{"X-Extra": "yes"}, UserAgent: &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_Fixed{Fixed: "Fixed/2"}}})
	if _, body, _ := get(t, c, target.URL); body != "ua=Fixed/2 x=yes" {
		t.Fatalf("fixed: %q", body)
	}
	// The plugin's own user agent is replaced, a deliberate other one stays.
	if _, body, _ := get(t, c, target.URL, "User-Agent", "plugin/1"); body != "ua=Fixed/2 x=yes" {
		t.Fatalf("own UA: %q", body)
	}
	if _, body, _ := get(t, c, target.URL, "User-Agent", "special/9", "X-Extra", "mine"); body != "ua=special/9 x=mine" {
		t.Fatalf("deliberate UA: %q", body)
	}
	_ = c.SetProfile(&metadatav1.NetworkProfile{Name: "p", UserAgent: &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_RandomDesktop{RandomDesktop: true}}})
	_, a, _ := get(t, c, target.URL)
	_, b, _ := get(t, c, target.URL)
	if !strings.Contains(a, "Mozilla/5.0") || a != b {
		t.Fatalf("random desktop should be stable: %q %q", a, b)
	}
}

func TestConcurrencyAndRateLimit(t *testing.T) {
	var cur, peak atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
	}))
	defer slow.Close()
	c := httpx.New(httpx.Options{})
	_ = c.SetProfile(&metadatav1.NetworkProfile{Concurrency: 2})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = get(t, c, slow.URL) }()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("concurrency: peak %d", peak.Load())
	}

	_ = c.SetProfile(&metadatav1.NetworkProfile{RateLimit: &metadatav1.NetworkRateLimit{Requests: 5, Period: durationpb.New(500 * time.Millisecond)}})
	start := time.Now()
	for range 4 {
		_, _, _ = get(t, c, slow.URL)
	}
	// 1 immediately, then one every 100ms.
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("rate limit not applied: %s", d)
	}
}

func TestCallTimeout(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	c := httpx.New(httpx.Options{})
	_ = c.SetProfile(&metadatav1.NetworkProfile{Timeouts: &metadatav1.Timeouts{Call: durationpb.New(100 * time.Millisecond)}})
	start := time.Now()
	if _, _, err := get(t, c, hang.URL); err == nil || time.Since(start) > time.Second {
		t.Fatalf("call timeout: %v after %s", err, time.Since(start))
	}
}

// guarded is a site behind a challenge that lets through requests with the
// clearance cookie and the solver's user agent.
func guarded(t *testing.T, cfHeader bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		ck, err := r.Cookie("cf_clearance")
		if err == nil && ck.Value == "ok" && r.UserAgent() == "SolverUA/1" {
			fmt.Fprint(w, "content")
			return
		}
		if cfHeader {
			w.Header().Set("Cf-Mitigated", "challenge")
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html><head><title>Just a moment...</title></head></html>")
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

func fakeSolver(t *testing.T, good bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Cmd, URL   string
			MaxTimeout int64
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		time.Sleep(50 * time.Millisecond) // let concurrent requests pile up
		value := "ok"
		if !good {
			value = "nope"
		}
		u := strings.TrimPrefix(strings.TrimPrefix(req.URL, "http://"), "https://")
		host := strings.Split(strings.Split(u, "/")[0], ":")[0]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "message": "",
			"solution": map[string]any{
				"url": req.URL, "status": 200, "userAgent": "SolverUA/1", "response": "<html>solved page</html>",
				"cookies": []map[string]any{{"name": "cf_clearance", "value": value, "domain": "." + host, "path": "/"}},
			},
		})
	}))
	t.Cleanup(s.Close)
	return s, &calls
}

func TestSolverPassesChallenge(t *testing.T) {
	site, _ := guarded(t, true)
	sv, calls := fakeSolver(t, true)
	c := httpx.New(httpx.Options{UserAgent: "plugin/1"})
	_ = c.SetProfile(&metadatav1.NetworkProfile{Name: "p", Solver: &metadatav1.Solver{Url: sv.URL}})

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body, err := get(t, c, site.URL+"/page")
			if err == nil && (code != 200 || body != "content") {
				err = fmt.Errorf("got %d %q", code, body)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("solves should be coalesced per host: %d", calls.Load())
	}
	// Later requests reuse the cookies and user agent without solving.
	if code, _, err := get(t, c, site.URL+"/other"); err != nil || code != 200 || calls.Load() != 1 {
		t.Fatalf("reuse: %d %v (%d solves)", code, err, calls.Load())
	}
}

func TestChallengeWithoutSolverFailsLoudly(t *testing.T) {
	site, _ := guarded(t, false) // recognized by the page, not a header
	c := httpx.New(httpx.Options{})
	_, _, err := get(t, c, site.URL)
	if !httpx.IsChallenge(err) || !strings.Contains(err.Error(), "add a solver") {
		t.Fatalf("want a challenge error, got %v", err)
	}
}

func TestSolverFallbackResponse(t *testing.T) {
	site, _ := guarded(t, true)
	sv, _ := fakeSolver(t, false) // clearance that doesn't work
	c := httpx.New(httpx.Options{})
	_ = c.SetProfile(&metadatav1.NetworkProfile{Solver: &metadatav1.Solver{Url: sv.URL, AsResponseFallback: true}})
	code, body, err := get(t, c, site.URL)
	if err != nil || code != 200 || body != "<html>solved page</html>" {
		t.Fatalf("fallback: %d %q %v", code, body, err)
	}
	_ = c.SetProfile(&metadatav1.NetworkProfile{Solver: &metadatav1.Solver{Url: sv.URL}})
	if code, _, err := get(t, c, site.URL+"/x"); err != nil || code != http.StatusForbidden {
		t.Fatalf("no fallback: %d %v", code, err)
	}
}

func TestPlain403PassesThrough(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "no access")
	}))
	defer s.Close()
	c := httpx.New(httpx.Options{})
	code, body, err := get(t, c, s.URL)
	if err != nil || code != 403 || body != "no access" {
		t.Fatalf("plain 403: %d %q %v", code, body, err)
	}
	var ce *httpx.ChallengeError
	if errors.As(err, &ce) {
		t.Fatal("plain 403 treated as a challenge")
	}
}
