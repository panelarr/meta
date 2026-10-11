// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/duplicates"
)

// addDuplicate stores the fake provider's own duplicate of "Lantern
// Keeper" (made up), runs the finder and returns the pair's ID.
func addDuplicate(t *testing.T, f *fixture) int64 {
	t.Helper()
	ctx := context.Background()
	e := entry(f.fake, "8", func(e *metadatav1.ProviderEntry) {
		e.Titles = []*metadatav1.Title{{Text: "The Lantern Keeper", Language: "en", IsPrimary: true}}
		e.Type = metadatav1.EntryType_ENTRY_TYPE_MANHWA
		e.OriginalLanguage = "ko"
		e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2018}}
	})
	if _, err := f.writer.Apply(ctx, catalog.Batch{Provider: "fake", Upserts: []*metadatav1.ProviderEntry{e}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dups.Finder().Run(ctx, time.Time{}, uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := f.st.Pool.QueryRow(ctx, `SELECT id FROM duplicate_candidates ORDER BY score DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDuplicatesReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	id := addDuplicate(t, f)
	c := f.as(testAdminToken).duplicates

	list, err := c.ListDuplicates(ctx, connect.NewRequest(&metav1.ListDuplicatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if list.Msg.GetTotalSize() != 1 || len(list.Msg.GetPairs()) != 1 {
		t.Fatalf("pairs: %v", list.Msg)
	}
	p := list.Msg.GetPairs()[0]
	titles := p.GetEntryA().GetTitle() + "|" + p.GetEntryB().GetTitle()
	if p.GetId() != id || p.GetStatus() != metav1.DuplicateStatus_DUPLICATE_STATUS_OPEN || !strings.Contains(titles, "Lantern Keeper") ||
		!strings.Contains(titles, "The Lantern Keeper") || !p.GetSignals().GetSameProvider() || p.GetScore() < 0.6 || p.GetVerdict() != nil {
		t.Fatalf("pair: %v", p)
	}
	if res, _ := c.ListDuplicates(ctx, connect.NewRequest(&metav1.ListDuplicatesRequest{JudgedOnly: true})); len(res.Msg.GetPairs()) != 0 {
		t.Fatal("judged only")
	}

	// Dismiss, then take it back.
	dis, err := c.DismissDuplicate(ctx, connect.NewRequest(&metav1.DismissDuplicateRequest{Id: id, Note: "not sure"}))
	if err != nil || dis.Msg.GetPair().GetStatus() != metav1.DuplicateStatus_DUPLICATE_STATUS_DISMISSED || dis.Msg.GetPair().GetDecidedBy() != "admin" {
		t.Fatalf("dismiss: %v %v", dis, err)
	}
	if _, err := c.DismissDuplicate(ctx, connect.NewRequest(&metav1.DismissDuplicateRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("dismiss twice: %v", err)
	}
	if res, _ := c.ListDuplicates(ctx, connect.NewRequest(&metav1.ListDuplicatesRequest{Status: metav1.DuplicateStatus_DUPLICATE_STATUS_DISMISSED})); len(res.Msg.GetPairs()) != 1 {
		t.Fatal("dismissed list")
	}
	if _, err := c.ReopenDuplicate(ctx, connect.NewRequest(&metav1.ReopenDuplicateRequest{Id: id})); err != nil {
		t.Fatal(err)
	}

	// Confirm, keeping entry b.
	if _, err := c.ConfirmDuplicate(ctx, connect.NewRequest(&metav1.ConfirmDuplicateRequest{Id: id, Keep: "c"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad keep: %v", err)
	}
	conf, err := c.ConfirmDuplicate(ctx, connect.NewRequest(&metav1.ConfirmDuplicateRequest{Id: id, Keep: "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if conf.Msg.GetEntryId() != p.GetEntryB().GetId() || conf.Msg.GetPair().GetStatus() != metav1.DuplicateStatus_DUPLICATE_STATUS_CONFIRMED {
		t.Fatalf("confirm: %v", conf.Msg)
	}
	// The other entry now redirects.
	got, err := f.as(testAdminToken).meta.GetEntry(ctx, connect.NewRequest(&metav1.GetEntryRequest{Id: p.GetEntryA().GetId()}))
	if err != nil || got.Msg.GetEntry().GetId() != p.GetEntryB().GetId() || got.Msg.GetRedirectedFrom() != p.GetEntryA().GetId() {
		t.Fatalf("redirect: %v %v", got, err)
	}
	if _, err := c.ConfirmDuplicate(ctx, connect.NewRequest(&metav1.ConfirmDuplicateRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirm twice: %v", err)
	}
	if _, err := c.GetDuplicate(ctx, connect.NewRequest(&metav1.GetDuplicateRequest{Id: 999999})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown pair: %v", err)
	}

	// Admin scope only.
	created, _ := f.as(testAdminToken).clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "reader", Scopes: []string{"catalog:read"}}))
	if _, err := f.as(created.Msg.GetApiKey()).duplicates.ListDuplicates(ctx, connect.NewRequest(&metav1.ListDuplicatesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reader: %v", err)
	}
}

func TestDecisionJudgeSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	c := f.as(testAdminToken).duplicates

	got, err := c.GetDecisionJudge(ctx, connect.NewRequest(&metav1.GetDecisionJudgeRequest{}))
	if err != nil || got.Msg.GetSettings().GetEnabled() || got.Msg.GetSettings().GetOrigin() != "config" {
		t.Fatalf("default: %v %v", got, err)
	}
	// A model at a mock server.
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer judge-secret" || !strings.Contains(string(raw), "Lantern Keeper") {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{
			"same": map[string]any{"type": "noul", "noul": 0.97}, "relation": map[string]any{"choice": "same_work"},
		}, "usage": map[string]int{"input_tokens": 222}})
	}))
	t.Cleanup(model.Close)
	if _, err := c.PutDecisionJudge(ctx, connect.NewRequest(&metav1.PutDecisionJudgeRequest{Settings: &metav1.DecisionJudgeSettings{
		Enabled: true, Kind: "typesafe", Url: model.URL,
	}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("on without a key: %v", err)
	}
	test, err := c.TestDecisionJudge(ctx, connect.NewRequest(&metav1.TestDecisionJudgeRequest{Settings: &metav1.DecisionJudgeSettings{
		Kind: "typesafe", Url: model.URL, ApiKey: "judge-secret",
	}}))
	if err != nil || !test.Msg.GetOk() || test.Msg.GetVerdict().GetSame() != 0.97 || test.Msg.GetVerdict().GetTokens() != 222 {
		t.Fatalf("test: %v %v", test, err)
	}
	put, err := c.PutDecisionJudge(ctx, connect.NewRequest(&metav1.PutDecisionJudgeRequest{Settings: &metav1.DecisionJudgeSettings{
		Enabled: true, Kind: "typesafe", Url: model.URL, ApiKey: "judge-secret",
	}}))
	if err != nil || !put.Msg.GetSettings().GetHasApiKey() || put.Msg.GetSettings().GetApiKey() != "" || put.Msg.GetSettings().GetOrigin() != "admin" {
		t.Fatalf("put: %v %v", put, err)
	}
	var sealed string
	if err := f.st.Pool.QueryRow(ctx, `SELECT api_key_sealed FROM judge_settings`).Scan(&sealed); err != nil || strings.Contains(sealed, "judge-secret") {
		t.Fatalf("stored key: %q %v", sealed, err)
	}

	// The job rates the open pair with the stored key.
	addDuplicate(t, f)
	rep, err := duplicates.JudgeOpen(ctx, f.st.Pool, mustJudge(t, f), duplicates.JudgeOptions{}, nil)
	if err != nil || rep.Judged != 1 {
		t.Fatalf("judge: %+v %v", rep, err)
	}
	list, _ := c.ListDuplicates(ctx, connect.NewRequest(&metav1.ListDuplicatesRequest{JudgedOnly: true}))
	if len(list.Msg.GetPairs()) != 1 || list.Msg.GetPairs()[0].GetVerdict().GetSame() != 0.97 || list.Msg.GetPairs()[0].GetVerdict().GetChoice() != "same_work" {
		t.Fatalf("judged pair: %v", list.Msg)
	}

	if res, err := c.ResetDecisionJudge(ctx, connect.NewRequest(&metav1.ResetDecisionJudgeRequest{})); err != nil || res.Msg.GetSettings().GetOrigin() != "config" {
		t.Fatalf("reset: %v %v", res, err)
	}
}

func mustJudge(t *testing.T, f *fixture) duplicates.Judge {
	t.Helper()
	js, err := f.dups.JudgeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j, err := duplicates.NewJudge(duplicates.JudgeConfig{Kind: js.Kind, URL: js.URL, Model: js.Model, APIKey: js.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	return j
}
