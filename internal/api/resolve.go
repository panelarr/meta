// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/panelarr/meta/internal/store"
)

type timeT = time.Time

// resolveEntryID parses an entry ID and follows the redirect of an entry
// that was merged into another.
func resolveEntryID(ctx context.Context, st *store.Store, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid entry id %q", raw))
	}
	to, err := st.GetRedirect(ctx, id)
	switch {
	case err == nil:
		return to, nil
	case errors.Is(err, pgx.ErrNoRows):
		return id, nil
	default:
		return uuid.Nil, err
	}
}
