// SPDX-License-Identifier: Apache-2.0

package plugin_test

import (
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
)

func TestSyncWriterCheckpointsAndTotals(t *testing.T) {
	var events []*metadatav1.SyncResponse
	w := plugin.NewSyncWriter(func(ev *metadatav1.SyncResponse) error {
		events = append(events, ev)
		return nil
	})
	w.CheckpointEvery = 2
	ref := func(id string) *metadatav1.ExternalId { return &metadatav1.ExternalId{Provider: "p", Id: id} }
	_ = w.Started(&metadatav1.SyncStarted{Kind: plugin.KindDump})
	for _, id := range []string{"1", "2", "3"} {
		if err := w.Upsert(&metadatav1.ProviderEntry{Ref: ref(id)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Merge(ref("4"), ref("1"))
	_ = w.Skip(ref("5"), "broken")
	_ = w.Finished("c")

	var checkpoints []int64
	for _, ev := range events {
		if cp := ev.GetCheckpoint(); cp != nil {
			checkpoints = append(checkpoints, cp.GetCount())
		}
	}
	if len(checkpoints) != 1 || checkpoints[0] != 2 {
		t.Fatalf("checkpoints: %v", checkpoints)
	}
	f := events[len(events)-1].GetFinished()
	if f.GetUpserts() != 3 || f.GetMerges() != 1 || f.GetSkipped() != 1 || f.GetCursor() != "c" {
		t.Fatalf("finished: %v", f)
	}
}

func TestErrorCodes(t *testing.T) {
	if plugin.ErrorCode(plugin.ErrNotFound("x")) != plugin.CodeNotFound {
		t.Fatal("not found")
	}
	if plugin.ErrorCode(nil) != 0 {
		t.Fatal("nil")
	}
}
