// SPDX-License-Identifier: Apache-2.0

package conformance_test

import (
	"context"
	"strings"
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/conformance"
	"github.com/panelarr/meta/sdk/plugin"
	"github.com/panelarr/meta/sdk/plugintest"
)

// broken breaks several rules on purpose.
type broken struct {
	*plugintest.Fake
}

func (b broken) GetInfo(ctx context.Context, r *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	res, _ := b.Fake.GetInfo(ctx, r)
	res.Info.Id = "Not An ID"
	res.Info.Protocols = nil
	return res, nil
}

func (b broken) Sync(_ context.Context, _ *metadatav1.SyncRequest, send plugin.SendFunc) error {
	w := plugin.NewSyncWriter(send)
	e := b.Entry("1", "One")
	other := b.Entry("2", "")
	other.Ref.Provider = "someone-else"
	_ = w.Upsert(e) // before Started
	_ = w.Started(&metadatav1.SyncStarted{Kind: plugin.KindDump})
	_ = w.Upsert(e)                     // duplicate
	_ = w.Upsert(other)                 // wrong provider, empty title
	_ = w.Merge(b.Ref("3"), b.Ref("3")) // merge into itself
	return nil                          // no Finished
}

func TestReportsViolations(t *testing.T) {
	rep, err := conformance.Check(context.Background(), client.InProcess(broken{&plugintest.Fake{}}), conformance.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"id \"Not An ID\"",
		"protocols must include",
		"must be the first",
		"sent twice",
		"provider must be \"fake\"",
		"title 0 is empty",
		"into itself",
		"no SyncFinished",
	}
	all := strings.Join(rep.Problems, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("missing problem %q in:\n%s", w, all)
		}
	}
}
