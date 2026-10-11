// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/panelarr/meta/internal/store/db"
)

// ErrNotActive is returned when an entry to merge isn't active.
var ErrNotActive = errors.New("the entry isn't active")

// MergeEntries makes two entries one, as a curator decided: every record
// of from moves to into, even one of a provider into already has (the
// providers kept them apart, the curator says they are the same work), and
// from is retired behind a redirect, so its ID and its records' IDs keep
// resolving. Field locks carry over unless into has its own.
func (w *Writer) MergeEntries(ctx context.Context, from, into uuid.UUID, reason string) (Result, error) {
	if from == into {
		return Result{}, errors.New("an entry can't be merged into itself")
	}
	return w.inTx(ctx, func(t *tx) error {
		for _, id := range []uuid.UUID{from, into} {
			e, err := t.q.GetEntry(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: no entry %s", ErrNotActive, id)
			}
			if err != nil {
				return err
			}
			if e.State != "active" {
				return fmt.Errorf("%w: entry %s is %s", ErrNotActive, id, e.State)
			}
		}
		if err := t.q.MoveProviderEntries(ctx, db.MoveProviderEntriesParams{ToID: uuid.NullUUID{UUID: into, Valid: true}, FromID: uuid.NullUUID{UUID: from, Valid: true}}); err != nil {
			return err
		}
		if err := t.q.MoveFieldLocks(ctx, db.MoveFieldLocksParams{ToID: into, FromID: from}); err != nil {
			return err
		}
		if err := t.q.RepointRedirects(ctx, db.RepointRedirectsParams{ToID: into, FromID: from}); err != nil {
			return err
		}
		if err := t.q.InsertRedirect(ctx, db.InsertRedirectParams{FromID: from, ToID: into, Reason: reason}); err != nil {
			return err
		}
		if err := t.q.SetEntryState(ctx, db.SetEntryStateParams{ID: from, State: "merged", MergedInto: uuid.NullUUID{UUID: into, Valid: true}}); err != nil {
			return err
		}
		t.reindex[from] = true
		t.dirty[into] = true
		t.result.EntriesChanged++
		return nil
	})
}
