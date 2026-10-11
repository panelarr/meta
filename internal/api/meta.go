// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"log/slog"
	"time"

	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/store"
)

// MetaOptions configure the catalogue service.
type MetaOptions struct {
	// DisplayLanguage is the default language for display titles.
	DisplayLanguage string
	// StaleAfter is the age after which a lookup refreshes an entry.
	StaleAfter time.Duration
	Log        *slog.Logger
}

// Meta implements MetaService: browse, search and details.
type Meta struct {
	metav1connect.UnimplementedMetaServiceHandler
	st     *store.Store
	host   *host.Host
	writer *catalog.Writer
	opts   MetaOptions
}

// NewMeta returns the catalogue service.
func NewMeta(st *store.Store, h *host.Host, w *catalog.Writer, opts MetaOptions) *Meta {
	return &Meta{st: st, host: h, writer: w, opts: opts}
}
