// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/panelarr/meta/internal/duplicates"
)

var sideA = duplicates.Side{Titles: []duplicates.TitleText{{Text: "Lantern Keeper", Language: "en"}}, People: []string{"Hana Imaginary"}, Year: 2012, Type: "manga"}
var sideB = duplicates.Side{Titles: []duplicates.TitleText{{Text: "The Lantern Keeper", Language: "en"}}, People: []string{"Hana Imaginary"}, Year: 2012, Type: "manga", Cover: []byte{1, 2, 3}, CoverType: "image/png"}

// mock answers like a decision API, recording what it was sent.
func mock(t *testing.T, path string, answer func(body map[string]any) (int, any)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got = append(got, body)
		status, out := answer(body)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestOpenAIDecisions(t *testing.T) {
	t.Parallel()
	calls := 0
	srv, got := mock(t, "/v1/decisions", func(body map[string]any) (int, any) {
		calls++
		if calls == 1 {
			return http.StatusTooManyRequests, map[string]string{"error": "slow down"}
		}
		return http.StatusOK, map[string]any{
			"model": "gpt-6-luna-2026",
			"answers": []any{
				map[string]any{"type": "predicate", "name": "same", "probability": 0.93},
				map[string]any{"type": "choice", "name": "relation", "choice": "same_work", "confidence": 0.88,
					"probabilities": []any{map[string]any{"value": "same_work", "probability": 0.9}, map[string]any{"value": "different", "probability": 0.1}}},
			},
		}
	})
	j, err := duplicates.NewJudge(duplicates.JudgeConfig{Kind: duplicates.KindOpenAI, URL: srv.URL, APIKey: "test-key", Covers: true})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	v, err := j.Judge(context.Background(), sideA, sideB)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second || calls != 2 {
		t.Fatalf("no back-off after 429: %d calls in %s", calls, time.Since(start))
	}
	if v.Same != 0.93 || v.Choice != duplicates.ChoiceSame || v.Probabilities["different"] != 0.1 || v.Confidence != 0.88 ||
		v.Model != "gpt-6-luna-2026" || v.Tokens == 0 || j.Key() != "openai:gpt-6-luna" {
		t.Fatalf("verdict: %+v", v)
	}
	body := (*got)[1]
	if body["model"] != "gpt-6-luna" {
		t.Fatalf("model: %v", body["model"])
	}
	qs := body["questions"].([]any)
	if q := qs[0].(map[string]any); q["type"] != "predicate" || q["name"] != "same" {
		t.Fatalf("question: %v", q)
	}
	if q := qs[1].(map[string]any); q["type"] != "choice" || len(q["choices"].([]any)) != 3 {
		t.Fatalf("choice question: %v", q)
	}
	content := body["input"].([]any)[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "The Lantern Keeper") || !strings.Contains(text, "Hana Imaginary") {
		t.Fatalf("state: %s", text)
	}
	if len(content) != 2 || !strings.HasPrefix(content[1].(map[string]any)["image_url"].(string), "data:image/png;base64,") {
		t.Fatalf("cover: %v", content)
	}
}

func TestOpenAIRefusal(t *testing.T) {
	t.Parallel()
	srv, _ := mock(t, "/v1/decisions", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"answers": []any{map[string]any{"type": "refusal", "name": "same"}}}
	})
	j, _ := duplicates.NewJudge(duplicates.JudgeConfig{Kind: duplicates.KindOpenAI, URL: srv.URL, APIKey: "test-key"})
	if _, err := j.Judge(context.Background(), sideA, sideB); !errors.Is(err, duplicates.ErrRefused) {
		t.Fatalf("refusal: %v", err)
	}
}

func TestTypeSafe(t *testing.T) {
	t.Parallel()
	calls := 0
	srv, got := mock(t, "/v1/systemone", func(map[string]any) (int, any) {
		calls++
		if calls == 1 {
			return 529, map[string]string{"error": "overloaded"}
		}
		return http.StatusOK, map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"same":     map[string]any{"type": "noul", "noul": 0.12},
				"relation": map[string]any{"type": "choice", "choice": "related_work", "confidence": 0.7, "probabilities": map[string]float64{"same_work": 0.1, "related_work": 0.8, "different": 0.1}},
			},
			"usage": map[string]int{"input_tokens": 321, "output_tokens": 4},
		}
	})
	j, _ := duplicates.NewJudge(duplicates.JudgeConfig{Kind: duplicates.KindTypeSafe, URL: srv.URL, APIKey: "test-key", Covers: true})
	v, err := j.Judge(context.Background(), sideA, sideB)
	if err != nil {
		t.Fatal(err)
	}
	if v.Same != 0.12 || v.Choice != duplicates.ChoiceRelated || v.Tokens != 321 || v.Model != "jev-1.13.0" || calls != 2 {
		t.Fatalf("verdict: %+v (%d calls)", v, calls)
	}
	body := (*got)[1]
	qs := body["questions"].(map[string]any)
	if qs["same"].(map[string]any)["type"] != "noul" || len(qs["relation"].(map[string]any)["criteria"].(map[string]any)) != 3 {
		t.Fatalf("questions: %v", qs)
	}
	if _, ok := body["state"].(map[string]any)["entry_a"]; !ok || body["images"] != nil || body["model"] != "jev-latest" {
		t.Fatalf("body: %v", body)
	}
}

func TestClef(t *testing.T) {
	t.Parallel()
	path := "/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef"
	srv, got := mock(t, path, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"success": true, "errors": []any{}, "result": map[string]any{
			"model": "clef",
			"answers": map[string]any{
				"same":     map[string]any{"type": "noul", "probability": 0.81},
				"relation": map[string]any{"choice": "same_work", "probabilities": map[string]float64{"same_work": 0.8}},
			},
		}}
	})
	j, err := duplicates.NewJudge(duplicates.JudgeConfig{Kind: duplicates.KindClef, URL: srv.URL + path, APIKey: "test-key", Covers: true})
	if err != nil {
		t.Fatal(err)
	}
	v, err := j.Judge(context.Background(), sideA, sideB)
	if err != nil || v.Same != 0.81 || v.Choice != duplicates.ChoiceSame || v.Tokens == 0 {
		t.Fatalf("verdict: %+v %v", v, err)
	}
	if imgs, _ := (*got)[0]["images"].([]any); len(imgs) != 1 {
		t.Fatalf("images: %v", (*got)[0]["images"])
	}
	if _, err := duplicates.NewJudge(duplicates.JudgeConfig{Kind: duplicates.KindClef}); err == nil {
		t.Fatal("clef without a URL")
	}
	if _, err := duplicates.NewJudge(duplicates.JudgeConfig{Kind: "oracle"}); err == nil {
		t.Fatal("unknown kind")
	}
}

// countJudge answers every pair the same and counts the calls.
type countJudge struct {
	calls  atomic.Int32
	tokens int64
	fail   bool
}

func (c *countJudge) Key() string { return "count:v1" }
func (c *countJudge) Judge(context.Context, duplicates.Side, duplicates.Side) (duplicates.Verdict, error) {
	c.calls.Add(1)
	if c.fail {
		return duplicates.Verdict{}, errors.New("upstream said no")
	}
	return duplicates.Verdict{Judge: "count", Same: 0.9, Choice: duplicates.ChoiceSame, Tokens: c.tokens}, nil
}

func TestJudgeOpenCachesAndCaps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := seed(t)
	if _, err := duplicates.NewFinder(f.st.Pool, duplicates.FinderOptions{}).Run(ctx, time.Time{}, uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	j := &countJudge{tokens: 100}
	rep, err := duplicates.JudgeOpen(ctx, f.st.Pool, j, duplicates.JudgeOptions{}, nil)
	if err != nil || rep.Judged != 1 || rep.Tokens != 100 || j.calls.Load() != 1 {
		t.Fatalf("first round: %+v %v", rep, err)
	}
	var verdict []byte
	if err := f.st.Pool.QueryRow(ctx, `SELECT verdict FROM duplicate_candidates`).Scan(&verdict); err != nil || !strings.Contains(string(verdict), `"same": 0.9`) {
		t.Fatalf("stored verdict: %s %v", verdict, err)
	}
	// Judged pairs aren't asked again.
	if rep, _ := duplicates.JudgeOpen(ctx, f.st.Pool, j, duplicates.JudgeOptions{}, nil); rep.Judged != 0 || j.calls.Load() != 1 {
		t.Fatalf("second round: %+v", rep)
	}
	// An unchanged pair found again reuses the cached verdict.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE duplicate_candidates SET verdict = NULL`); err != nil {
		t.Fatal(err)
	}
	if rep, _ := duplicates.JudgeOpen(ctx, f.st.Pool, j, duplicates.JudgeOptions{}, nil); rep.Cached != 1 || j.calls.Load() != 1 {
		t.Fatalf("cache: %+v", rep)
	}
	// A token budget already spent stops the round before asking.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE duplicate_candidates SET verdict = NULL; DELETE FROM duplicate_verdicts`); err != nil {
		t.Fatal(err)
	}
	big := &countJudge{tokens: 10}
	if rep, _ := duplicates.JudgeOpen(ctx, f.st.Pool, big, duplicates.JudgeOptions{MaxTokens: 1, MaxPairs: 10}, nil); rep.Judged != 1 {
		t.Fatalf("budget: %+v", rep)
	}
	// Failures are noted and leave the pair for later.
	if _, err := f.st.Pool.Exec(ctx, `UPDATE duplicate_candidates SET verdict = NULL; DELETE FROM duplicate_verdicts`); err != nil {
		t.Fatal(err)
	}
	var notes []string
	rep, err = duplicates.JudgeOpen(ctx, f.st.Pool, &countJudge{fail: true}, duplicates.JudgeOptions{}, func(_, msg, _ string) { notes = append(notes, msg) })
	if err != nil || rep.Failed != 1 || len(notes) != 1 {
		t.Fatalf("failure: %+v %v %v", rep, err, notes)
	}
}
