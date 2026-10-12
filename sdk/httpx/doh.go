// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// doh resolves host names over DNS over HTTPS (RFC 8484, GET with the wire
// format) and dials the first address that answers.
type doh struct {
	endpoint string
	dialer   *net.Dialer
	http     *http.Client

	mu    sync.Mutex
	cache map[string]dohEntry
}

type dohEntry struct {
	addrs   []net.IP
	expires time.Time
}

func newDoH(endpoint string, dialer *net.Dialer) (*doh, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("httpx: the DNS over HTTPS URL must be https:// with a host")
	}
	return &doh{
		endpoint: endpoint,
		dialer:   dialer,
		// The resolver itself is reached with the system resolver.
		http:  &http.Client{Timeout: 10 * time.Second},
		cache: map[string]dohEntry{},
	}, nil
}

func (d *doh) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		return d.dialer.DialContext(ctx, network, addr)
	}
	ips, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (d *doh) lookup(ctx context.Context, host string) ([]net.IP, error) {
	d.mu.Lock()
	if e, ok := d.cache[host]; ok && time.Now().Before(e.expires) {
		d.mu.Unlock()
		return e.addrs, nil
	}
	d.mu.Unlock()
	var all []net.IP
	ttl := uint32(300)
	for _, t := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		ips, minTTL, err := d.query(ctx, host, t)
		if err != nil {
			return nil, err
		}
		all = append(all, ips...)
		if len(ips) > 0 && minTTL < ttl {
			ttl = minTTL
		}
	}
	if len(all) == 0 {
		return nil, &net.DNSError{Err: "no such host (DNS over HTTPS)", Name: host, IsNotFound: true}
	}
	d.mu.Lock()
	d.cache[host] = dohEntry{addrs: all, expires: time.Now().Add(time.Duration(max(ttl, 30)) * time.Second)}
	d.mu.Unlock()
	return all, nil
}

func (d *doh) query(ctx context.Context, host string, t dnsmessage.Type) ([]net.IP, uint32, error) {
	name, err := dnsmessage.NewName(dnsName(host))
	if err != nil {
		return nil, 0, err
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: t, Class: dnsmessage.ClassINET}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, 0, err
	}
	u := d.endpoint + "?dns=" + base64.RawURLEncoding.EncodeToString(packed)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("DNS over HTTPS: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("DNS over HTTPS: HTTP %d", resp.StatusCode)
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(body); err != nil {
		return nil, 0, fmt.Errorf("DNS over HTTPS: %w", err)
	}
	if answer.RCode != dnsmessage.RCodeSuccess && answer.RCode != dnsmessage.RCodeNameError {
		return nil, 0, errors.New("DNS over HTTPS: " + answer.RCode.String())
	}
	var ips []net.IP
	minTTL := uint32(1 << 31)
	for _, a := range answer.Answers {
		switch r := a.Body.(type) {
		case *dnsmessage.AResource:
			ips = append(ips, net.IP(bytes.Clone(r.A[:])))
		case *dnsmessage.AAAAResource:
			ips = append(ips, net.IP(bytes.Clone(r.AAAA[:])))
		default:
			continue
		}
		minTTL = min(minTTL, a.Header.TTL)
	}
	return ips, minTTL, nil
}

func dnsName(host string) string {
	if len(host) > 0 && host[len(host)-1] == '.' {
		return host
	}
	return host + "."
}
