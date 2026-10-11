// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panelarr/meta/internal/store/storetest"
)

func TestMigrateCommand(t *testing.T) {
	ctx := context.Background()
	url := storetest.URL(t)
	var out bytes.Buffer

	if err := run(ctx, []string{"migrate", "--database-url", url, "status"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pending") {
		t.Fatalf("status before: %q", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"migrate", "--database-url", url}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "applied 1") {
		t.Fatalf("up: %q", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"migrate", "--database-url", url, "up"}, &out); err != nil || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("second up: %q %v", out.String(), err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"migrate", "--database-url", "x", "sideways"}} {
		err := run(context.Background(), args, &bytes.Buffer{})
		if len(args) == 4 {
			// A bad database URL fails before the subcommand is checked.
			if err == nil {
				t.Errorf("%v: want an error", args)
			}
			continue
		}
		if !errors.Is(err, errUsage) {
			t.Errorf("%v: got %v", args, err)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil || out.Len() == 0 {
		t.Fatalf("version: %q %v", out.String(), err)
	}
}
