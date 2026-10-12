// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

// fakeDoH answers every A query with 127.0.0.1 and AAAA with nothing.
func fakeDoH(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil || r.Header.Get("Accept") != "application/dns-message" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var q dnsmessage.Message
		if err := q.Unpack(raw); err != nil {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		ans := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true}, Questions: q.Questions}
		if q.Questions[0].Type == dnsmessage.TypeA {
			ans.Answers = append(ans.Answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
			})
		}
		out, _ := ans.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	}))
}

func TestDoHResolvesAndDials(t *testing.T) {
	resolver := fakeDoH(t)
	defer resolver.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) }))
	defer target.Close()

	d, err := newDoH(resolver.URL+"/dns-query", &net.Dialer{})
	if err != nil {
		t.Fatal(err)
	}
	d.http = resolver.Client()
	ips, err := d.lookup(context.Background(), "made-up.example")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("lookup: %v %v", ips, err)
	}
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	conn, err := d.dialContext(context.Background(), "tcp", net.JoinHostPort("made-up.example", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	if _, err := newDoH("http://plain.example/dns-query", &net.Dialer{}); err == nil {
		t.Fatal("a plain-HTTP resolver was accepted")
	}
}

func TestProfileDoH(t *testing.T) {
	c := New(Options{})
	if err := c.SetProfile(&metadatav1.NetworkProfile{DohUrl: "https://dns.example/dns-query"}); err != nil {
		t.Fatalf("https resolver: %v", err)
	}
	if err := c.SetProfile(&metadatav1.NetworkProfile{DohUrl: "http://dns.example/dns-query"}); err == nil {
		t.Fatal("a plain-HTTP resolver was accepted")
	}
	// Through a proxy, the proxy resolves names: the resolver isn't used.
	if err := c.SetProfile(&metadatav1.NetworkProfile{
		Proxy:  &metadatav1.Proxy{Url: "http://proxy.example:3128"},
		DohUrl: "not a url",
	}); err != nil {
		t.Fatalf("with a proxy: %v", err)
	}
}
