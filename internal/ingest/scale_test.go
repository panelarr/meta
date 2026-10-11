// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugin"
)

// generator is a plugin that makes up n records shaped like a large
// provider dump: several titles, people, tags, a cover with variants,
// links, relations and external IDs, a raw original, and a share of
// records merged into others.
type generator struct {
	plugin.Unimplemented
	n         int
	mergedPct int
}

func (g generator) GetInfo(context.Context, *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	return &metadatav1.GetInfoResponse{Info: &metadatav1.PluginInfo{
		Id: "gen", Name: "Generator", Version: "0", Provider: "gen", Protocols: []string{plugin.Protocol},
		Ingestions:  []*metadatav1.Ingestion{{Kind: plugin.KindDump, FullSnapshot: true}},
		Attribution: &metadatav1.Attribution{ProviderName: "Generator"},
	}}, nil
}

func (g generator) record(i int) *metadatav1.ProviderEntry {
	id := strconv.Itoa(i)
	ref := func(p, x string) *metadatav1.ExternalId { return &metadatav1.ExternalId{Provider: p, Id: x} }
	e := &metadatav1.ProviderEntry{
		Ref: ref("gen", id), Url: "https://example.org/series/" + id,
		Type: metadatav1.EntryType(1 + i%6), RawType: "type" + strconv.Itoa(i%6),
		Status: metadatav1.EntryStatus_ENTRY_STATUS_RELEASING, RawStatus: "releasing",
		ContentRating: metadatav1.ContentRating_CONTENT_RATING_SAFE, RawContentRating: "safe",
		OriginalLanguage: []string{"ja", "ko", "zh"}[i%3],
		State:            metadatav1.EntryState_ENTRY_STATE_ACTIVE,
		Descriptions:     []*metadatav1.Description{{Language: "en", Text: fmt.Sprintf("Synthetic description number %d, long enough to look like a real summary of a series.", i)}},
		Dates:            &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: int32(1980 + i%45), Month: int32(1 + i%12)}},
		Stats:            &metadatav1.Stats{Rating: ptr(float64(i%100) + 0.5), PopularityRank: ptr(int64(i))},
		Attribution:      &metadatav1.Attribution{ProviderName: "Generator", License: "CC0-1.0"},
	}
	for j := range 5 {
		e.Titles = append(e.Titles, &metadatav1.Title{Text: fmt.Sprintf("Series %d title %d", i, j), Language: []string{"en", "ja", "ja-Latn", "ko", "fr"}[j], IsPrimary: j == 0})
	}
	for j := range 2 {
		e.Credits = append(e.Credits, &metadatav1.Credit{Person: &metadatav1.Person{Name: fmt.Sprintf("Person %d", (i*7+j)%120000)}, Role: metadatav1.PersonRole(1 + j)})
	}
	e.Companies = []*metadatav1.CompanyCredit{{Company: &metadatav1.Company{Name: fmt.Sprintf("Publisher %d", i%3000), ExternalIds: []*metadatav1.ExternalId{ref("gen", fmt.Sprintf("publisher/%d", i%3000))}}, Role: metadatav1.CompanyRole_COMPANY_ROLE_PUBLISHER}}
	for j := range 10 {
		t := (i*3 + j*17) % 1500
		e.Tags = append(e.Tags, &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: fmt.Sprintf("Tag %d", t), IsGenre: t < 40, ExternalIds: []*metadatav1.ExternalId{ref("gen", fmt.Sprintf("tag/%d", t))}}, Weight: metadatav1.TagWeight_TAG_WEIGHT_INCIDENTAL})
	}
	img := &metadatav1.Image{Kind: metadatav1.ImageKind_IMAGE_KIND_COVER, Url: "https://images.example.org/" + id + ".jpg", Width: 600, Height: 900, Blurhash: "LEHV6nWB2yk8pyo0adR*.7kCMdnj"}
	for _, w := range []int32{150, 250, 350} {
		for d := int32(1); d <= 3; d++ {
			img.Variants = append(img.Variants, &metadatav1.ImageVariant{Width: w, Density: d, Url: fmt.Sprintf("https://images.example.org/%s/%d@%d.jpg", id, w, d)})
		}
	}
	e.Images = []*metadatav1.Image{img}
	for j := range 3 {
		e.Links = append(e.Links, &metadatav1.Link{Type: metadatav1.LinkType_LINK_TYPE_STORE, Url: fmt.Sprintf("https://store%d.example.org/%s", j, id)})
	}
	for j := 1; j <= 2; j++ {
		e.Relations = append(e.Relations, &metadatav1.Relation{Type: metadatav1.RelationType_RELATION_TYPE_SEQUEL, Target: ref("gen", strconv.Itoa((i+j*13)%g.n))})
	}
	e.ExternalIds = []*metadatav1.ExternalId{ref("anilist", "a"+id), ref("mal", "m"+id), ref("mangaupdates", "u"+id), ref("kitsu", "k"+id)}
	e.Raw, _ = structpb.NewStruct(map[string]any{"id": float64(i), "title": e.Titles[0].Text, "flags": []any{"x", "y"}})
	if i%100 < g.mergedPct {
		e.State = metadatav1.EntryState_ENTRY_STATE_MERGED
		e.MergedInto = ref("gen", strconv.Itoa((i+1)%g.n))
		if (i+1)%100 < g.mergedPct { // merge targets are active
			e.MergedInto = ref("gen", strconv.Itoa((i+100-(i%100)+99)%g.n))
		}
	}
	return e
}

func (g generator) Sync(ctx context.Context, req *metadatav1.SyncRequest, send plugin.SendFunc) error {
	w := plugin.NewSyncWriter(send)
	w.CheckpointEvery = 1000
	if err := w.Started(&metadatav1.SyncStarted{Kind: req.GetKind(), SnapshotId: "gen", ExpectedCount: int64(g.n), FullSnapshot: true}); err != nil {
		return err
	}
	for i := range g.n {
		e := g.record(i)
		if err := w.Upsert(e); err != nil {
			return err
		}
		if e.GetState() == metadatav1.EntryState_ENTRY_STATE_MERGED {
			if err := w.Merge(e.GetRef(), e.GetMergedInto()); err != nil {
				return err
			}
		}
	}
	return w.Finished("gen")
}

func ptr[T any](v T) *T { return &v }

// TestScale measures a large ingest. It only runs when META_SCALE is set to
// the number of records, e.g. META_SCALE=564040 META_SCALE_MERGED=46.
func TestScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("META_SCALE"))
	if n == 0 {
		t.Skip("set META_SCALE to run")
	}
	merged, _ := strconv.Atoi(os.Getenv("META_SCALE_MERGED"))
	ctx := context.Background()
	st := storetest.New(t)
	gen := generator{n: n, mergedPct: merged}
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "gen", Remote: &config.Remote{URL: "x"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return client.InProcess(gen), nil
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: []string{"gen"}}, DisplayLanguage: "en"})
	r := ingest.NewRunner(st, h, w, log, 1000)

	for pass := 1; pass <= 2; pass++ {
		start := time.Now()
		run, err := r.Run(ctx, "gen", "dump", ingest.TriggerManual, true)
		if err != nil {
			t.Fatal(err)
		}
		var entries, redirects int
		var size string
		_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM entries WHERE state = 'active'`).Scan(&entries)
		_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_entries WHERE state = 'merged' AND entry_id IS NOT NULL`).Scan(&redirects)
		_ = st.Pool.QueryRow(ctx, `SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&size)
		t.Logf("pass %d: %d records (%d changed, %d merges) in %s; %d active entries, %d merged records linked; database %s",
			pass, run.Upserts, run.Changed, run.Merges, time.Since(start).Round(time.Second), entries, redirects, size)
	}
}
