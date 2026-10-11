// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"strconv"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

// SyncWriter sends sync events and keeps the counts SyncFinished reports.
// It can also emit a checkpoint every N upserts.
//
// A typical dump sync:
//
//	w := plugin.NewSyncWriter(send)
//	w.CheckpointEvery = 1000
//	w.Started(&metadatav1.SyncStarted{Kind: plugin.KindDump, FullSnapshot: true})
//	for each record { w.Upsert(entry) }
//	return w.Finished(cursor)
//
// SyncWriter is not safe for concurrent use.
type SyncWriter struct {
	send SendFunc

	// CheckpointEvery emits a checkpoint after this many upserts. Zero
	// disables automatic checkpoints.
	CheckpointEvery int64
	// Cursor returns the cursor for automatic checkpoints. The default is
	// the number of records sent so far.
	Cursor func(count int64) string

	upserts, removals, merges, skipped int64
	sinceCheckpoint                    int64
}

// NewSyncWriter wraps send.
func NewSyncWriter(send SendFunc) *SyncWriter {
	return &SyncWriter{send: send}
}

// Started sends the first event of a sync.
func (w *SyncWriter) Started(s *metadatav1.SyncStarted) error {
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Started{Started: s}})
}

// Upsert sends one record and, when due, a checkpoint.
func (w *SyncWriter) Upsert(e *metadatav1.ProviderEntry) error {
	if err := w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Upsert{Upsert: e}}); err != nil {
		return err
	}
	w.upserts++
	w.sinceCheckpoint++
	if w.CheckpointEvery > 0 && w.sinceCheckpoint >= w.CheckpointEvery {
		cursor := strconv.FormatInt(w.Count(), 10)
		if w.Cursor != nil {
			cursor = w.Cursor(w.Count())
		}
		return w.Checkpoint(cursor)
	}
	return nil
}

// Removed says the provider no longer has a record.
func (w *SyncWriter) Removed(ref *metadatav1.ExternalId, reason string) error {
	w.removals++
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Removed{
		Removed: &metadatav1.SyncRemoved{Ref: ref, Reason: reason},
	}})
}

// Merge says the provider merged one record into another.
func (w *SyncWriter) Merge(merged, into *metadatav1.ExternalId) error {
	w.merges++
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Merge{
		Merge: &metadatav1.SyncMerge{Merged: merged, Into: into},
	}})
}

// Checkpoint sends a checkpoint with the given cursor.
func (w *SyncWriter) Checkpoint(cursor string) error {
	w.sinceCheckpoint = 0
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Checkpoint{
		Checkpoint: &metadatav1.SyncCheckpoint{Cursor: cursor, Count: w.Count()},
	}})
}

// Skip counts a record that could not be read or mapped and reports why.
func (w *SyncWriter) Skip(ref *metadatav1.ExternalId, reason string) error {
	w.skipped++
	return w.Notice(metadatav1.NoticeLevel_NOTICE_LEVEL_WARNING, reason, ref)
}

// Notice sends a message for the admin.
func (w *SyncWriter) Notice(level metadatav1.NoticeLevel, msg string, ref *metadatav1.ExternalId) error {
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Notice{
		Notice: &metadatav1.SyncNotice{Level: level, Message: msg, Ref: ref},
	}})
}

// Progress reports work before or between records, such as a download.
func (w *SyncWriter) Progress(phase string, done, total int64, unit string) error {
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Progress{
		Progress: &metadatav1.SyncProgress{Phase: phase, Done: done, Total: total, Unit: unit},
	}})
}

// Finished sends the last event with the totals and the cursor for the next
// run.
func (w *SyncWriter) Finished(cursor string) error {
	return w.send(&metadatav1.SyncResponse{Event: &metadatav1.SyncResponse_Finished{
		Finished: &metadatav1.SyncFinished{
			Cursor:   cursor,
			Upserts:  w.upserts,
			Removals: w.removals,
			Merges:   w.merges,
			Skipped:  w.skipped,
		},
	}})
}

// Count is the number of record events (upserts, removals, merges) sent so
// far.
func (w *SyncWriter) Count() int64 { return w.upserts + w.removals + w.merges }

// Upserts is the number of upserts sent so far.
func (w *SyncWriter) Upserts() int64 { return w.upserts }
