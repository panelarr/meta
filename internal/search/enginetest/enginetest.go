// SPDX-License-Identifier: GPL-3.0-or-later

// Package enginetest starts search engines for tests, once per test
// binary. Engine tests are opt-in: they run with META_TEST_ENGINES=1
// (containers via testcontainers) or with the URL of a running engine in
// META_TEST_TYPESENSE_URL / META_TEST_MEILISEARCH_URL (CI service
// containers), and skip otherwise.
package enginetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Images of the engines the tests run.
const (
	TypesenseImage   = "typesense/typesense:30.2"
	MeilisearchImage = "getmeili/meilisearch:v1.54"
)

// Key is the API key the test engines start with.
const Key = "meta-test-key"

type engine struct {
	once sync.Once
	url  string
	err  error
}

var typesense, meilisearch engine

func (e *engine) get(t testing.TB, env string, start func() (string, error)) (string, string) {
	t.Helper()
	if u := os.Getenv(env); u != "" {
		return u, envOr(env+"_KEY", Key)
	}
	if os.Getenv("META_TEST_ENGINES") != "1" {
		t.Skipf("search engine tests are opt-in: set META_TEST_ENGINES=1 or %s", env)
	}
	e.once.Do(func() { e.url, e.err = start() })
	if e.err != nil {
		t.Fatalf("enginetest: %v", e.err)
	}
	return e.url, Key
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Typesense returns the URL and API key of a Typesense server.
func Typesense(t testing.TB) (string, string) {
	return typesense.get(t, "META_TEST_TYPESENSE_URL", func() (string, error) {
		return start(TypesenseImage, "8108/tcp", []string{"--data-dir", "/tmp", "--api-key", Key, "--enable-cors=false"}, nil, "/health")
	})
}

// Meilisearch returns the URL and master key of a Meilisearch server.
func Meilisearch(t testing.TB) (string, string) {
	return meilisearch.get(t, "META_TEST_MEILISEARCH_URL", func() (string, error) {
		return start(MeilisearchImage, "7700/tcp", nil, map[string]string{
			"MEILI_MASTER_KEY": Key, "MEILI_NO_ANALYTICS": "true", "MEILI_ENV": "development",
		}, "/health")
	})
}

func start(image, port string, cmd []string, env map[string]string, health string) (string, error) {
	ctx := context.Background()
	c, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithCmd(cmd...),
		testcontainers.WithEnv(env),
		testcontainers.WithWaitStrategy(wait.ForHTTP(health).WithPort(port).WithStartupTimeout(2*time.Minute)),
		testcontainers.WithLogger(nopLogger{}),
	)
	if err != nil {
		return "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return "", err
	}
	mapped, err := c.MappedPort(ctx, port)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%s", host, mapped.Port()), nil
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// Prefix is a fresh index name prefix, so tests sharing an engine don't
// see each other's indexes.
func Prefix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)
}
