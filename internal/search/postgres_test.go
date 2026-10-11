// SPDX-License-Identifier: GPL-3.0-or-later

package search_test

import (
	"testing"

	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/search/searchtest"
)

func TestPostgresConformance(t *testing.T) {
	t.Parallel()
	f := searchtest.Seed(t, false)
	searchtest.Run(t, f, search.NewPostgres(f.Store.Pool), "postgres")
}
