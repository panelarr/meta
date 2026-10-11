// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// healthURL is the default probe URL: the local /healthz on the port of
// META_LISTEN, or 8901.
func healthURL() string {
	port := "8901"
	if l := os.Getenv("META_LISTEN"); l != "" {
		if _, p, err := net.SplitHostPort(l); err == nil && p != "" {
			port = p
		}
	}
	return "http://127.0.0.1:" + port + "/healthz"
}

// runHealthcheck probes a running meta, for container health checks in
// images without a shell. It exits 0 when /healthz answers 200.
func runHealthcheck(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", healthURL(), "health endpoint to probe")
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck:", err)
		return 2
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "healthcheck: status", res.Status)
		return 1
	}
	return 0
}
