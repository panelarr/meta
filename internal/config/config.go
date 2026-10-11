// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads meta's configuration: a YAML file (optional) with
// ${VAR} expansion, then environment variables for the common settings.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"
)

// Config is meta's whole configuration.
type Config struct {
	// Listen is the address of the API server. Default ":8901".
	Listen string `yaml:"listen"`
	// AdminToken is the admin's bearer token. Empty: meta generates one on
	// first start, prints it once and stores only its hash.
	AdminToken string `yaml:"admin_token"`
	// DataDir holds files meta keeps outside the database: the generated
	// encryption key for stored secrets. Default: the user's config
	// directory ("…/panelarr-meta"); the image uses /data.
	DataDir string `yaml:"data_dir"`
	// SecretKey encrypts stored secrets such as proxy passwords: base64 of
	// 32 bytes. Empty: a key file in DataDir, generated on first use.
	SecretKey string `yaml:"secret_key"`
	// APIKeys is deprecated: create clients with scoped keys instead. Keys
	// listed here keep full access.
	APIKeys []string `yaml:"api_keys"`
	// DatabaseURL is the PostgreSQL URL.
	DatabaseURL string `yaml:"database_url"`
	// Search configures the search backend.
	Search Search `yaml:"search"`
	// Merge configures how provider records become entries.
	Merge Merge `yaml:"merge"`
	// Ingest tunes syncs.
	Ingest Ingest `yaml:"ingest"`
	// Jobs tunes background jobs.
	Jobs Jobs `yaml:"jobs"`
	// Providers are the metadata plugins meta uses. None by default.
	Providers []Provider `yaml:"providers"`
	// Log configures logging.
	Log Log `yaml:"log"`
	// Metrics configures the Prometheus metrics endpoint.
	Metrics Metrics `yaml:"metrics"`
}

// Metrics configures Prometheus metrics. They are off by default and, when
// on, served on their own address so they aren't open on the API port.
type Metrics struct {
	Enabled bool `yaml:"enabled"`
	// Listen is the metrics address, e.g. ":9464". Empty serves /metrics on
	// the API port instead, to callers with the metrics:read scope.
	Listen string `yaml:"listen"`
}

type Search struct {
	// Backend is "postgres" (the default), "typesense" or "meilisearch".
	// Settings saved in the admin win over these.
	Backend string `yaml:"backend"`
	// Browse says who serves browsing (no search text) with an engine:
	// "facets" (the engine when facets are asked for; the default),
	// "engine" or "postgres".
	Browse string `yaml:"browse"`
	// Timeout of one engine query before PostgreSQL answers. Default 2s.
	Timeout     time.Duration `yaml:"timeout"`
	Typesense   Engine        `yaml:"typesense"`
	Meilisearch Engine        `yaml:"meilisearch"`
	Outbox      Outbox        `yaml:"outbox"`
}

// Engine is how meta reaches a search engine.
type Engine struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
	// IndexPrefix starts the names of meta's indexes. Default "meta".
	IndexPrefix string `yaml:"index_prefix"`
}

// Outbox tunes how entry changes reach a search engine.
type Outbox struct {
	// Batch is how many changes one round applies. Default 500.
	Batch int `yaml:"batch"`
	// Interval is how often an idle indexer looks for changes. Default 1s.
	Interval time.Duration `yaml:"interval"`
	// RebuildThreshold: with more changes waiting, the index is rebuilt
	// instead. Default 50000.
	RebuildThreshold int64 `yaml:"rebuild_threshold"`
}

type Merge struct {
	// Priority lists provider keys in order of preference per field of the
	// entry schema ("titles", "credits", ...). The key "*" is the default
	// for fields without their own list. Providers not listed come after,
	// in the order they are configured.
	Priority map[string][]string `yaml:"priority"`
	// DisplayLanguage picks the display title when a provider marks one
	// primary title per language. Default "en".
	DisplayLanguage string `yaml:"display_language"`
}

type Ingest struct {
	// BatchSize caps how many records are written in one transaction when
	// a plugin sends checkpoints rarely. Default 500.
	BatchSize int `yaml:"batch_size"`
	// StaleAfter is the age after which an entry counts as stale and a
	// lookup refreshes it. Default 168h (a week).
	StaleAfter time.Duration `yaml:"stale_after"`
}

// Jobs tunes background jobs other than provider syncs (those are
// scheduled per provider).
type Jobs struct {
	// Retention is how many finished runs are kept per job, syncs
	// included. Default 100.
	Retention int `yaml:"retention"`
	// StaleRefresh refreshes the entries whose provider records are older
	// than ingest.stale_after, oldest first, from providers that can look
	// records up.
	StaleRefresh StaleRefresh `yaml:"stale_refresh"`
}

type StaleRefresh struct {
	// Schedule is a cron expression (UTC). Empty: only run by hand.
	Schedule string `yaml:"schedule"`
	// Limit caps the entries refreshed per run. Default 100.
	Limit int `yaml:"limit"`
}

type Log struct {
	// Level is debug, info, warn or error. Default info.
	Level string `yaml:"level"`
	// Format is "json" or "text". Default json.
	Format string `yaml:"format"`
}

// Provider is one metadata plugin.
type Provider struct {
	// ID is the provider key the plugin reports (e.g. "mangabaka").
	ID string `yaml:"id"`
	// Managed starts the plugin binary as a child process.
	Managed *Managed `yaml:"managed"`
	// Remote connects to a plugin running as a service.
	Remote *Remote `yaml:"remote"`
	// Settings are sent to the plugin with SetSettings.
	Settings map[string]any `yaml:"settings"`
	// Schedules per ingestion kind, as cron expressions (5 fields, UTC),
	// e.g. {dump: "0 3 * * *"}. "@every 6h" also works. Empty: manual only.
	Schedules map[string]string `yaml:"schedules"`
	// Disabled keeps the provider configured but unused.
	Disabled bool `yaml:"disabled"`
}

type Managed struct {
	Path string   `yaml:"path"`
	Args []string `yaml:"args"`
	Env  []string `yaml:"env"`
}

type Remote struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
	// Connect uses the Connect protocol instead of gRPC.
	Connect bool `yaml:"connect"`
}

// Mode returns "managed" or "remote".
func (p Provider) Mode() string {
	if p.Managed != nil {
		return "managed"
	}
	return "remote"
}

// Default returns the configuration with defaults and no providers.
func Default() Config {
	dataDir := "data"
	if dir, err := os.UserConfigDir(); err == nil {
		dataDir = filepath.Join(dir, "panelarr-meta")
	}
	return Config{
		DataDir: dataDir,
		Listen:  ":8901",
		Search: Search{
			Backend: "postgres", Browse: "facets", Timeout: 2 * time.Second,
			Typesense:   Engine{IndexPrefix: "meta"},
			Meilisearch: Engine{IndexPrefix: "meta"},
			Outbox:      Outbox{Batch: 500, Interval: time.Second, RebuildThreshold: 50000},
		},
		Merge:   Merge{DisplayLanguage: "en"},
		Ingest:  Ingest{BatchSize: 500, StaleAfter: 7 * 24 * time.Hour},
		Jobs:    Jobs{Retention: 100, StaleRefresh: StaleRefresh{Limit: 100}},
		Log:     Log{Level: "info", Format: "json"},
	}
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expand replaces ${VAR} and ${VAR:-default} with environment values.
func expand(b []byte) []byte {
	return envRef.ReplaceAllFunc(b, func(m []byte) []byte {
		g := envRef.FindSubmatch(m)
		if v, ok := os.LookupEnv(string(g[1])); ok && v != "" {
			return []byte(v)
		}
		return g[3]
	})
}

// Load reads the file at path (if not empty), applies environment
// variables and checks the result.
//
// Environment variables: META_CONFIG (file path, when path is empty),
// META_LISTEN, META_ADMIN_TOKEN, META_DATA_DIR, META_SECRET_KEY, META_DATABASE_URL, META_API_KEYS (comma-separated),
// META_LOG_LEVEL, META_LOG_FORMAT, META_SEARCH_BACKEND, META_METRICS_ENABLED,
// META_METRICS_LISTEN.
func Load(path string) (Config, error) {
	return LoadWith(path, nil)
}

// LoadWith is Load with a final override (e.g. from command-line flags)
// applied before the configuration is checked.
func LoadWith(path string, override func(*Config)) (Config, error) {
	cfg := Default()
	if path == "" {
		path = os.Getenv("META_CONFIG")
	}
	if path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // the operator chooses the config file
		if err != nil {
			return cfg, fmt.Errorf("config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(expand(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	if override != nil {
		override(&cfg)
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Listen, "META_LISTEN")
	set(&cfg.AdminToken, "META_ADMIN_TOKEN")
	set(&cfg.DataDir, "META_DATA_DIR")
	set(&cfg.SecretKey, "META_SECRET_KEY")
	set(&cfg.DatabaseURL, "META_DATABASE_URL")
	set(&cfg.Log.Level, "META_LOG_LEVEL")
	set(&cfg.Log.Format, "META_LOG_FORMAT")
	set(&cfg.Search.Backend, "META_SEARCH_BACKEND")
	set(&cfg.Search.Typesense.URL, "META_TYPESENSE_URL")
	set(&cfg.Search.Typesense.APIKey, "META_TYPESENSE_API_KEY")
	set(&cfg.Search.Meilisearch.URL, "META_MEILISEARCH_URL")
	set(&cfg.Search.Meilisearch.APIKey, "META_MEILISEARCH_API_KEY")
	if v := os.Getenv("META_METRICS_ENABLED"); v != "" {
		cfg.Metrics.Enabled = v == "true" || v == "1"
	}
	if v, ok := os.LookupEnv("META_METRICS_LISTEN"); ok {
		cfg.Metrics.Listen = v
	}
	if v := os.Getenv("META_API_KEYS"); v != "" {
		cfg.APIKeys = nil
		for k := range strings.SplitSeq(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				cfg.APIKeys = append(cfg.APIKeys, k)
			}
		}
	}
}

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// CronParser parses provider schedules.
var CronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Validate checks the configuration.
func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url is required (or META_DATABASE_URL)"))
	}
	switch c.Search.Backend {
	case "postgres":
	case "typesense", "meilisearch":
		e := c.Search.Typesense
		if c.Search.Backend == "meilisearch" {
			e = c.Search.Meilisearch
		}
		if e.URL == "" {
			errs = append(errs, fmt.Errorf("search.%s.url is required with search.backend %s", c.Search.Backend, c.Search.Backend))
		}
	default:
		errs = append(errs, fmt.Errorf("search.backend %q is not available (supported: postgres, typesense, meilisearch)", c.Search.Backend))
	}
	if !slices.Contains([]string{"facets", "engine", "postgres"}, c.Search.Browse) {
		errs = append(errs, fmt.Errorf("search.browse %q: use facets, engine or postgres", c.Search.Browse))
	}
	if c.Ingest.BatchSize <= 0 {
		errs = append(errs, errors.New("ingest.batch_size must be positive"))
	}
	if c.Jobs.Retention <= 0 {
		errs = append(errs, errors.New("jobs.retention must be positive"))
	}
	if c.Jobs.StaleRefresh.Limit <= 0 {
		errs = append(errs, errors.New("jobs.stale_refresh.limit must be positive"))
	}
	if spec := c.Jobs.StaleRefresh.Schedule; spec != "" {
		if _, err := CronParser.Parse(spec); err != nil {
			errs = append(errs, fmt.Errorf("jobs.stale_refresh.schedule: %w", err))
		}
	}
	seen := map[string]bool{}
	for i, p := range c.Providers {
		where := fmt.Sprintf("providers[%d]", i)
		if !providerID.MatchString(p.ID) {
			errs = append(errs, fmt.Errorf("%s: id %q must be lower case letters, digits, '-' or '_'", where, p.ID))
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("%s: provider %q is listed twice", where, p.ID))
		}
		seen[p.ID] = true
		switch {
		case p.Managed != nil && p.Remote != nil:
			errs = append(errs, fmt.Errorf("%s (%s): set either managed or remote, not both", where, p.ID))
		case p.Managed == nil && p.Remote == nil:
			errs = append(errs, fmt.Errorf("%s (%s): set managed.path or remote.url", where, p.ID))
		case p.Managed != nil && p.Managed.Path == "":
			errs = append(errs, fmt.Errorf("%s (%s): managed.path is empty", where, p.ID))
		case p.Remote != nil && p.Remote.URL == "":
			errs = append(errs, fmt.Errorf("%s (%s): remote.url is empty", where, p.ID))
		}
		for kind, spec := range p.Schedules {
			if _, err := CronParser.Parse(spec); err != nil {
				errs = append(errs, fmt.Errorf("%s (%s): schedule for %q: %w", where, p.ID, kind, err))
			}
		}
	}
	for field, order := range c.Merge.Priority {
		sorted := slices.Clone(order)
		slices.Sort(sorted)
		if len(slices.Compact(sorted)) != len(order) {
			errs = append(errs, fmt.Errorf("merge.priority.%s lists a provider twice", field))
		}
	}
	return errors.Join(errs...)
}

// ProviderOrder returns the provider keys in configuration order.
func (c Config) ProviderOrder() []string {
	out := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		out = append(out, p.ID)
	}
	return out
}
