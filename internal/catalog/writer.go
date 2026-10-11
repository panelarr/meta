// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Options configure a Writer.
type Options struct {
	Priority        Priority
	DisplayLanguage string
}

// Writer stores provider records and keeps the merged entries up to date.
// It is safe for concurrent use; each call runs in its own transaction.
//
// Writes are set-based: a batch of records costs a handful of round trips
// (batched statements and COPY), not several per record, so a full dump of
// hundreds of thousands of records ingests in minutes.
type Writer struct {
	st   *store.Store
	opts Options

	// Shared-entity IDs known to exist, by kind and key. Only filled after
	// a transaction commits, so a rolled-back row is never reused.
	mu    sync.RWMutex
	known map[string]uuid.UUID
}

// NewWriter returns a Writer.
func NewWriter(st *store.Store, opts Options) *Writer {
	return &Writer{st: st, opts: opts, known: map[string]uuid.UUID{}}
}

// Result counts what a call changed.
type Result struct {
	// Records whose stored provider record changed (new, updated, merged,
	// removed).
	Changed int
	// Records that arrived unchanged.
	Unchanged int
	// Entries whose content or state changed.
	EntriesChanged int
}

// Merge is a provider-side merge of one record into another.
type Merge struct {
	Merged, Into string
}

// Batch is a group of changes from one provider, written in one
// transaction.
type Batch struct {
	Provider string
	// Run is the sync run that delivered the batch; empty for lookups.
	Run      uuid.NullUUID
	Upserts  []*metadatav1.ProviderEntry
	Merges   []Merge
	Removals []string
}

// tx is the state of one transaction.
type tx struct {
	w      *Writer
	q      *db.Queries
	pgtx   pgx.Tx
	result Result
	// Entries to rebuild at the end of the transaction.
	dirty map[uuid.UUID]bool
	// Entries created in this transaction: they have no parts to delete.
	fresh map[uuid.UUID]bool
	// (entry, provider) pairs claimed by records linked in this
	// transaction, for the one-record-per-provider rule.
	claimed map[string]bool
	// IDs created or found in this transaction, published on commit.
	known map[string]uuid.UUID
	// Records that became merged in this transaction.
	mergedIDs []string
	// force rebuilds entries even when their content hash is unchanged.
	force bool
	// Linking candidates for the current batch, by refKey.
	cands map[string][]db.FindEntriesForRefsRow
}

func (w *Writer) inTx(ctx context.Context, f func(t *tx) error) (Result, error) {
	var t *tx
	err := w.st.InTx(ctx, func(q *db.Queries, pgtx pgx.Tx) error {
		t = &tx{
			w: w, q: q, pgtx: pgtx,
			dirty: map[uuid.UUID]bool{}, fresh: map[uuid.UUID]bool{},
			claimed: map[string]bool{}, known: map[string]uuid.UUID{},
		}
		if err := f(t); err != nil {
			return err
		}
		return t.flushDirty(ctx)
	})
	if err != nil {
		return Result{}, err
	}
	w.mu.Lock()
	for k, v := range t.known {
		w.known[k] = v
	}
	w.mu.Unlock()
	return t.result, nil
}

// Apply writes a batch: upserts first, then removals and merges, then the
// affected entries are rebuilt.
func (w *Writer) Apply(ctx context.Context, b Batch) (Result, error) {
	return w.inTx(ctx, func(t *tx) error {
		upserted, err := t.upserts(ctx, b.Provider, b.Run, b.Upserts)
		if err != nil {
			return err
		}
		for _, id := range b.Removals {
			if err := t.removed(ctx, b.Provider, id, b.Run); err != nil {
				return err
			}
		}
		for _, m := range b.Merges {
			// The common case: the record itself arrived in this batch with
			// state merged, so the merge is already stored.
			if into, ok := upserted[m.Merged]; ok && into == m.Into {
				continue
			}
			changed, err := t.merged(ctx, b.Provider, m, b.Run)
			if err != nil {
				return err
			}
			if changed {
				t.mergedIDs = append(t.mergedIDs, m.Merged)
			}
		}
		if len(t.mergedIDs) > 0 {
			return t.reconcile(ctx, b.Provider, t.mergedIDs)
		}
		return nil
	})
}

// FinishSnapshot marks the provider's active records that the run did not
// deliver as removed, and resolves merges whose targets arrived late.
func (w *Writer) FinishSnapshot(ctx context.Context, provider string, run uuid.UUID) (int, error) {
	var marked int
	_, err := w.inTx(ctx, func(t *tx) error {
		ids, err := t.q.MarkUnseenRemoved(ctx, db.MarkUnseenRemovedParams{Provider: provider, RunID: run})
		if err != nil {
			return err
		}
		marked = len(ids)
		for _, id := range ids {
			if id.Valid {
				t.dirty[id.UUID] = true
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	_, err = w.Reconcile(ctx, provider)
	return marked, err
}

// Reconcile resolves the provider's merges: merged records without an
// entry point at their target's entry, and entries left behind by a merge
// are retired into their target.
func (w *Writer) Reconcile(ctx context.Context, provider string) (Result, error) {
	return w.inTx(ctx, func(t *tx) error {
		// Chains (A into B into C) settle over a few rounds.
		for range 8 {
			n, err := t.q.LinkMergedRecords(ctx, provider)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
		return t.reconcile(ctx, provider, nil)
	})
}

// Rebuild recomputes one entry, e.g. after a curator changed a lock.
func (w *Writer) Rebuild(ctx context.Context, entry uuid.UUID) (Result, error) {
	return w.inTx(ctx, func(t *tx) error {
		t.dirty[entry] = true
		return nil
	})
}

// RebuildAll rebuilds every active entry from its stored provider records,
// even if nothing changed: after an upgrade that changes how entries are
// built. progress, if set, gets the number of entries done so far.
func (w *Writer) RebuildAll(ctx context.Context, batch int, progress func(done int)) (int, error) {
	if batch <= 0 {
		batch = 500
	}
	var after uuid.UUID
	done := 0
	for {
		ids, err := w.st.ListActiveEntryIDsAfter(ctx, db.ListActiveEntryIDsAfterParams{After: after, Lim: int32(batch)})
		if err != nil {
			return done, err
		}
		if len(ids) == 0 {
			break
		}
		if _, err := w.inTx(ctx, func(t *tx) error {
			t.force = true
			for _, id := range ids {
				t.dirty[id] = true
			}
			return nil
		}); err != nil {
			return done, err
		}
		done += len(ids)
		after = ids[len(ids)-1]
		if progress != nil {
			progress(done)
		}
	}
	if err := w.st.RecountTags(ctx); err != nil {
		return done, err
	}
	_, err := w.st.DeleteUnusedTags(ctx)
	return done, err
}

// RecountTags refreshes the entry count of every tag.
func (w *Writer) RecountTags(ctx context.Context) error {
	return w.st.RecountTags(ctx)
}

func hashRecord(e *metadatav1.ProviderEntry) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(e)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

var pj = protojson.MarshalOptions{}

func stateName(s metadatav1.EntryState) string {
	switch s {
	case metadatav1.EntryState_ENTRY_STATE_MERGED:
		return "merged"
	case metadatav1.EntryState_ENTRY_STATE_REMOVED:
		return "removed"
	default:
		return "active"
	}
}

// upserts stores a batch of records. It returns the records that arrived
// with state merged, mapped to their target.
func (t *tx) upserts(ctx context.Context, provider string, run uuid.NullUUID, recs []*metadatav1.ProviderEntry) (map[string]string, error) {
	upserted := map[string]string{}
	if len(recs) == 0 {
		return upserted, nil
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		if r.GetRef().GetProvider() != provider || r.GetRef().GetId() == "" {
			return nil, fmt.Errorf("catalog: record %s:%s does not belong to provider %s", r.GetRef().GetProvider(), r.GetRef().GetId(), provider)
		}
		ids = append(ids, r.GetRef().GetId())
	}
	states, err := t.q.GetProviderEntryStates(ctx, db.GetProviderEntryStatesParams{Provider: provider, ExternalIds: ids})
	if err != nil {
		return nil, err
	}
	have := make(map[string]db.GetProviderEntryStatesRow, len(states))
	for _, s := range states {
		have[s.ExternalID] = s
	}

	if err := t.prefetchCandidates(ctx, recs, have); err != nil {
		return nil, err
	}
	var (
		unchanged  []string
		newEntries []uuid.UUID
		rows       []db.UpsertProviderEntryBatchParams
	)
	for _, r := range recs {
		id := r.GetRef().GetId()
		state := stateName(r.GetState())
		if state == "merged" {
			upserted[id] = r.GetMergedInto().GetId()
		}
		hash, err := hashRecord(r)
		if err != nil {
			return nil, err
		}
		prev, known := have[id]
		if known && bytes.Equal(prev.ContentHash, hash) && prev.State == state && (prev.EntryID.Valid || state != "active") {
			unchanged = append(unchanged, id)
			continue
		}
		entry := prev.EntryID
		// Active records get an entry. Records a provider already merged or
		// removed don't: merged ones point at their target's entry once it
		// is known (see Reconcile), which saves an entry and a redirect
		// for each.
		if !entry.Valid && state == "active" {
			e, created, err := t.link(ctx, r)
			if err != nil {
				return nil, err
			}
			if created {
				newEntries = append(newEntries, e)
			}
			entry = uuid.NullUUID{UUID: e, Valid: true}
		}
		row, err := recordRow(r, hash, entry, run)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
		if entry.Valid {
			t.dirty[entry.UUID] = true
		}
		if state == "merged" && entry.Valid {
			t.mergedIDs = append(t.mergedIDs, id)
		}
		t.result.Changed++
	}
	if err := batchExec(len(newEntries), func(f func(int, error)) {
		t.q.InsertEntryBatch(ctx, newEntries).Exec(f)
	}); err != nil {
		return nil, err
	}
	if err := batchExec(len(rows), func(f func(int, error)) {
		t.q.UpsertProviderEntryBatch(ctx, rows).Exec(f)
	}); err != nil {
		return nil, err
	}
	if len(unchanged) > 0 && run.Valid {
		if err := t.q.TouchProviderEntries(ctx, db.TouchProviderEntriesParams{RunID: run, Provider: provider, ExternalIds: unchanged}); err != nil {
			return nil, err
		}
	}
	t.result.Unchanged += len(unchanged)
	return upserted, nil
}

// batchExec runs a sqlc batch and returns its first error.
func batchExec(n int, run func(func(int, error))) error {
	if n == 0 {
		return nil
	}
	var first error
	run(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	return first
}

func recordRow(r *metadatav1.ProviderEntry, hash []byte, entry, run uuid.NullUUID) (db.UpsertProviderEntryBatchParams, error) {
	slim := proto.Clone(r).(*metadatav1.ProviderEntry)
	raw := slim.GetRaw()
	slim.Raw = nil
	row := db.UpsertProviderEntryBatchParams{
		Provider:    r.GetRef().GetProvider(),
		ExternalID:  r.GetRef().GetId(),
		EntryID:     entry,
		State:       stateName(r.GetState()),
		ContentHash: hash,
		LastSeenRun: run,
	}
	var err error
	if row.Record, err = pj.Marshal(slim); err != nil {
		return row, err
	}
	if raw != nil {
		if row.Raw, err = pj.Marshal(raw); err != nil {
			return row, err
		}
	}
	if r.GetAttribution() != nil {
		if row.Attribution, err = pj.Marshal(r.GetAttribution()); err != nil {
			return row, err
		}
	}
	if r.GetState() == metadatav1.EntryState_ENTRY_STATE_MERGED && r.GetMergedInto().GetId() != "" {
		row.MergedInto = pgtype.Text{String: r.GetMergedInto().GetId(), Valid: true}
	}
	if u := r.GetUpdatedAt(); u != nil {
		ts := u.AsTime()
		row.ProviderUpdatedAt = &ts
	}
	return row, nil
}

// refKey identifies an external ID.
func refKey(provider, id string) string { return provider + "\x00" + id }

func recordRefs(r *metadatav1.ProviderEntry) []*metadatav1.ExternalId {
	out := []*metadatav1.ExternalId{r.GetRef()}
	for _, x := range r.GetExternalIds() {
		if x.GetProvider() != "" && x.GetId() != "" {
			out = append(out, x)
		}
	}
	return out
}

// prefetchCandidates loads, in one query, the entries that the IDs of the
// batch's new active records point at.
func (t *tx) prefetchCandidates(ctx context.Context, recs []*metadatav1.ProviderEntry, have map[string]db.GetProviderEntryStatesRow) error {
	t.cands = map[string][]db.FindEntriesForRefsRow{}
	provSet, idSet := map[string]bool{}, map[string]bool{}
	for _, r := range recs {
		if prev, ok := have[r.GetRef().GetId()]; ok && prev.EntryID.Valid {
			continue
		}
		if r.GetState() != metadatav1.EntryState_ENTRY_STATE_ACTIVE && r.GetState() != metadatav1.EntryState_ENTRY_STATE_UNSPECIFIED {
			continue
		}
		for _, x := range recordRefs(r) {
			provSet[x.GetProvider()] = true
			idSet[x.GetId()] = true
		}
	}
	if len(idSet) == 0 {
		return nil
	}
	providers := make([]string, 0, len(provSet))
	for p := range provSet {
		providers = append(providers, p)
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	rows, err := t.q.FindEntriesForRefs(ctx, db.FindEntriesForRefsParams{Providers: providers, ExternalIds: ids})
	if err != nil {
		return err
	}
	for _, row := range rows {
		k := refKey(row.Provider, row.ExternalID)
		t.cands[k] = append(t.cands[k], row)
	}
	return nil
}

// link finds the entry a new record belongs to, or makes a new one. A
// record joins an entry that shares one of its IDs, unless that entry
// already has an active record of the same provider: the provider itself
// says those are different works.
func (t *tx) link(ctx context.Context, r *metadatav1.ProviderEntry) (uuid.UUID, bool, error) {
	provider := r.GetRef().GetProvider()
	seen := map[uuid.UUID]bool{}
	var cands []db.FindEntriesForRefsRow
	for _, x := range recordRefs(r) {
		for _, c := range t.cands[refKey(x.GetProvider(), x.GetId())] {
			if !seen[c.EntryID] {
				seen[c.EntryID] = true
				cands = append(cands, c)
			}
		}
	}
	// Active entries first, then by ID (time order) for stable results.
	sort.Slice(cands, func(i, j int) bool {
		ai, aj := cands[i].State == "active", cands[j].State == "active"
		if ai != aj {
			return ai
		}
		return bytes.Compare(cands[i].EntryID[:], cands[j].EntryID[:]) < 0
	})
	for _, c := range cands {
		if c.State == "merged" || t.claimed[c.EntryID.String()+"\x00"+provider] {
			continue
		}
		taken, err := t.q.EntryHasProviderRecord(ctx, db.EntryHasProviderRecordParams{
			EntryID: uuid.NullUUID{UUID: c.EntryID, Valid: true}, Provider: provider, ExternalID: r.GetRef().GetId(),
		})
		if err != nil {
			return uuid.Nil, false, err
		}
		if !taken {
			t.claimed[c.EntryID.String()+"\x00"+provider] = true
			return c.EntryID, false, nil
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, false, err
	}
	t.fresh[id] = true
	t.claimed[id.String()+"\x00"+provider] = true
	return id, true, nil
}

func (t *tx) removed(ctx context.Context, provider, id string, run uuid.NullUUID) error {
	prev, err := t.q.GetProviderEntry(ctx, db.GetProviderEntryParams{Provider: provider, ExternalID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // never had it
	}
	if err != nil {
		return err
	}
	if prev.State == "removed" {
		t.result.Unchanged++
		return nil
	}
	entry, err := t.q.SetProviderEntryRemoved(ctx, db.SetProviderEntryRemovedParams{Provider: provider, ExternalID: id, RunID: run})
	if err != nil {
		return err
	}
	if entry.Valid {
		t.dirty[entry.UUID] = true
	}
	t.result.Changed++
	return nil
}

func (t *tx) merged(ctx context.Context, provider string, m Merge, run uuid.NullUUID) (bool, error) {
	prev, err := t.q.GetProviderEntry(ctx, db.GetProviderEntryParams{Provider: provider, ExternalID: m.Merged})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // a record meta never had: nothing to redirect
	}
	if err != nil {
		return false, err
	}
	if prev.State == "merged" && prev.MergedInto.String == m.Into {
		t.result.Unchanged++
		return false, nil
	}
	entry, err := t.q.SetProviderEntryMerged(ctx, db.SetProviderEntryMergedParams{
		Provider: provider, ExternalID: m.Merged, MergedInto: pgtype.Text{String: m.Into, Valid: true}, RunID: run,
	})
	if err != nil {
		return false, err
	}
	if entry.Valid {
		t.dirty[entry.UUID] = true
	}
	t.result.Changed++
	return true, nil
}

// reconcile moves merged records that have an entry of their own into the
// entry of their target, and retires entries left without active records.
func (t *tx) reconcile(ctx context.Context, provider string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	for range 8 {
		rows, err := t.q.ListMergeTargets(ctx, db.ListMergeTargetsParams{Provider: provider, ExternalIds: ids})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := t.moveMerged(ctx, provider, r.ExternalID, r.FromEntry.UUID, r.ToEntry.UUID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *tx) moveMerged(ctx context.Context, provider, externalID string, from, to uuid.UUID) error {
	if err := t.q.SetProviderEntryEntry(ctx, db.SetProviderEntryEntryParams{
		EntryID: uuid.NullUUID{UUID: to, Valid: true}, Provider: provider, ExternalID: externalID,
	}); err != nil {
		return err
	}
	t.dirty[to] = true
	t.dirty[from] = true
	return t.retireIfEmpty(ctx, from, to)
}

// retireIfEmpty merges entry from into entry to when from has no active
// records left of its own: the records that remain move over, unless the
// target already has an active record of the same provider.
func (t *tx) retireIfEmpty(ctx context.Context, from, to uuid.UUID) error {
	recs, err := t.q.ListProviderEntriesForEntry(ctx, uuid.NullUUID{UUID: from, Valid: true})
	if err != nil {
		return err
	}
	var stay int
	for _, r := range recs {
		if r.State == "active" {
			taken, err := t.q.EntryHasProviderRecord(ctx, db.EntryHasProviderRecordParams{
				EntryID: uuid.NullUUID{UUID: to, Valid: true}, Provider: r.Provider, ExternalID: r.ExternalID,
			})
			if err != nil {
				return err
			}
			if taken {
				stay++
				continue
			}
		}
		if err := t.q.SetProviderEntryEntry(ctx, db.SetProviderEntryEntryParams{
			EntryID: uuid.NullUUID{UUID: to, Valid: true}, Provider: r.Provider, ExternalID: r.ExternalID,
		}); err != nil {
			return err
		}
	}
	if stay > 0 {
		return nil
	}
	if err := t.q.MoveFieldLocks(ctx, db.MoveFieldLocksParams{ToID: to, FromID: from}); err != nil {
		return err
	}
	if err := t.q.RepointRedirects(ctx, db.RepointRedirectsParams{ToID: to, FromID: from}); err != nil {
		return err
	}
	if err := t.q.InsertRedirect(ctx, db.InsertRedirectParams{FromID: from, ToID: to, Reason: "merged by provider"}); err != nil {
		return err
	}
	if err := t.clearParts(ctx, []uuid.UUID{from}); err != nil {
		return err
	}
	if err := t.q.SetEntryState(ctx, db.SetEntryStateParams{ID: from, State: "merged", MergedInto: uuid.NullUUID{UUID: to, Valid: true}}); err != nil {
		return err
	}
	delete(t.dirty, from)
	t.dirty[to] = true
	t.result.EntriesChanged++
	return nil
}
