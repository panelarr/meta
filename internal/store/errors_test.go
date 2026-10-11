// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestExplainOutOfSpace(t *testing.T) {
	cases := []struct {
		name string
		err  error
		full bool
	}{
		{"disk_full", &pgconn.PgError{Code: "53100", Message: `could not extend file "base/16384/16500": No space left on device`}, true},
		{"io error", fmt.Errorf("upsert: %w", &pgconn.PgError{Code: "58030", Message: `could not write to file "pg_wal/xlogtemp.42": No space left on device`}), true},
		{"other", &pgconn.PgError{Code: "23505", Message: "duplicate key value"}, false},
		{"plain", errors.New("plugin: unavailable"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Explain(c.err)
			if errors.Is(got, ErrOutOfSpace) != c.full {
				t.Fatalf("Explain(%v) = %v, out of space want %v", c.err, got, c.full)
			}
			if c.full && !strings.HasPrefix(got.Error(), "database out of space: free disk space") {
				t.Errorf("message = %q", got)
			}
			if !errors.Is(got, c.err) {
				t.Error("the original error is lost")
			}
			if c.full && Explain(got).Error() != got.Error() {
				t.Error("explained twice")
			}
		})
	}
	if Explain(nil) != nil {
		t.Error("Explain(nil) != nil")
	}
}
