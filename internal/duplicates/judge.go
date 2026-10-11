// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A decision model judges whether two entries are the same work. It
// answers with probabilities, not prose. Judges are optional and off by
// default; nothing is sent anywhere unless the hoster turns one on.

// Judge kinds.
const (
	KindOpenAI   = "openai"   // OpenAI Decisions: POST /v1/decisions
	KindTypeSafe = "typesafe" // TypeSafe: POST /v1/systemone
	KindClef     = "clef"     // Cloudflare Workers AI: …/ai/run/@cf/cloudflare/clef
)

// Relation choices the judge picks from.
const (
	ChoiceSame      = "same_work"
	ChoiceRelated   = "related_work"
	ChoiceDifferent = "different"
)

// Verdict is a judge's answer about a pair.
type Verdict struct {
	Judge string `json:"judge"`
	Model string `json:"model"`
	// Same is the probability that the two are the same work.
	Same float64 `json:"same"`
	// Choice is the most likely of same_work, related_work and different,
	// with every option's probability.
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	// Tokens the request cost (input tokens; estimated when the API
	// doesn't say).
	Tokens int64     `json:"tokens"`
	At     time.Time `json:"at"`
}

// Side is what a judge is told about one entry.
type Side struct {
	Titles      []TitleText `json:"titles"`
	People      []string    `json:"people,omitempty"`
	Type        string      `json:"type,omitempty"`
	Status      string      `json:"status,omitempty"`
	Language    string      `json:"original_language,omitempty"`
	Year        int         `json:"year,omitempty"`
	Description string      `json:"description,omitempty"`
	// Cover image bytes, sent to judges that take images when covers are
	// turned on.
	Cover     []byte `json:"-"`
	CoverType string `json:"-"`
}

// TitleText is a title with its language.
type TitleText struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

// Judge asks a decision model about a pair.
type Judge interface {
	// Key identifies the judge and model, for the verdict cache.
	Key() string
	Judge(ctx context.Context, a, b Side) (Verdict, error)
}

// JudgeConfig is how to reach a decision model.
type JudgeConfig struct {
	Kind   string
	URL    string // base URL; Clef: the model's full run URL
	Model  string
	APIKey string
	// Covers sends cover images to judges that take them.
	Covers bool
	// Retries on 429, 529 and 503 before giving up. Default 5.
	Retries int
	HTTP    *http.Client
}

// ErrRefused is returned when a model declines to answer.
var ErrRefused = errors.New("the model refused to answer")

// NewJudge returns the judge of a configuration.
func NewJudge(c JudgeConfig) (Judge, error) {
	if c.Retries <= 0 {
		c.Retries = 5
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	switch c.Kind {
	case KindOpenAI:
		if c.URL == "" {
			c.URL = "https://api.openai.com"
		}
		if c.Model == "" {
			c.Model = "gpt-6-luna"
		}
		return &openAIJudge{c: c}, nil
	case KindTypeSafe:
		if c.URL == "" {
			c.URL = "https://api.typesafe.ai"
		}
		if c.Model == "" {
			c.Model = "jev-latest"
		}
		return &stateJudge{c: c, path: "/v1/systemone"}, nil
	case KindClef:
		if c.URL == "" {
			return nil, errors.New("clef needs the model's run URL (…/accounts/<account>/ai/run/@cf/cloudflare/clef)")
		}
		if c.Model == "" {
			c.Model = "clef"
		}
		return &stateJudge{c: c, envelope: true}, nil
	}
	return nil, fmt.Errorf("unknown judge kind %q (use openai, typesafe or clef)", c.Kind)
}

const (
	sameInstructions = "Two catalogue entries describe comics or books (manga, manhwa, manhua, novels and the like). " +
		"Are they the same work: the same series, possibly listed twice, under another title, in another language or edition?"
	relationInstructions = "How are the two entries related?"
)

var relationChoices = []struct{ value, description string }{
	{ChoiceSame, "The same work listed twice (any title, language or edition)"},
	{ChoiceRelated, "Different works that belong together: a sequel, prequel, spin-off, side story or adaptation"},
	{ChoiceDifferent, "Unrelated works, even if the titles look alike"},
}

// state is the pair as the models read it.
func state(a, b Side) string {
	raw, _ := json.Marshal(map[string]Side{"entry_a": a, "entry_b": b})
	return string(raw)
}

// estimate is a rough token count for APIs that don't report usage.
func estimate(s string) int64 { return int64(len(s)/4 + 1) }

// post sends a JSON request, retrying with back-off on 429, 529 and 503.
func post(ctx context.Context, c JudgeConfig, url string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	delay := time.Second
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		res, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		switch {
		case res.StatusCode < 300:
			return json.Unmarshal(data, out)
		case (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == 529 || res.StatusCode == http.StatusServiceUnavailable) && attempt < c.Retries:
			wait := delay
			if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			delay = min(2*delay, time.Minute)
		default:
			return fmt.Errorf("decision model: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
		}
	}
}

// openAIJudge speaks OpenAI's Decisions API.
type openAIJudge struct{ c JudgeConfig }

func (j *openAIJudge) Key() string { return KindOpenAI + ":" + j.c.Model }

func (j *openAIJudge) Judge(ctx context.Context, a, b Side) (Verdict, error) {
	text := state(a, b)
	content := []map[string]any{{"type": "input_text", "text": text}}
	if j.c.Covers {
		for _, s := range []Side{a, b} {
			if len(s.Cover) > 0 {
				content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(s)})
			}
		}
	}
	choices := make([]map[string]string, len(relationChoices))
	for i, c := range relationChoices {
		choices[i] = map[string]string{"value": c.value, "description": c.description}
	}
	body := map[string]any{
		"model": j.c.Model,
		"input": []map[string]any{{"role": "user", "content": content}},
		"questions": []map[string]any{
			{"type": "predicate", "name": "same", "instructions": sameInstructions},
			{"type": "choice", "name": "relation", "instructions": relationInstructions, "choices": choices},
		},
	}
	var res struct {
		Model   string `json:"model"`
		Answers []struct {
			Type          string  `json:"type"`
			Name          string  `json:"name"`
			Probability   float64 `json:"probability"`
			Choice        string  `json:"choice"`
			Confidence    float64 `json:"confidence"`
			Probabilities []struct {
				Value       string  `json:"value"`
				Probability float64 `json:"probability"`
			} `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := post(ctx, j.c, strings.TrimRight(j.c.URL, "/")+"/v1/decisions", body, &res); err != nil {
		return Verdict{}, err
	}
	v := Verdict{Judge: KindOpenAI, Model: j.c.Model, Probabilities: map[string]float64{}, Tokens: res.Usage.InputTokens, At: time.Now().UTC()}
	if res.Model != "" {
		v.Model = res.Model
	}
	for _, ans := range res.Answers {
		switch {
		case ans.Type == "refusal":
			return Verdict{}, fmt.Errorf("%w (%s)", ErrRefused, ans.Name)
		case ans.Name == "same":
			v.Same = ans.Probability
		case ans.Name == "relation":
			v.Choice, v.Confidence = ans.Choice, ans.Confidence
			for _, p := range ans.Probabilities {
				v.Probabilities[p.Value] = p.Probability
			}
		}
	}
	if v.Tokens == 0 {
		v.Tokens = estimate(text)
	}
	return v, nil
}

func dataURL(s Side) string {
	typ := s.CoverType
	if typ == "" {
		typ = "image/jpeg"
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(s.Cover)
}

// stateJudge speaks the state + questions-map APIs of TypeSafe and Clef.
type stateJudge struct {
	c    JudgeConfig
	path string
	// envelope: the answer is wrapped in Cloudflare's {"result": …}.
	envelope bool
}

func (j *stateJudge) Key() string {
	kind := KindTypeSafe
	if j.envelope {
		kind = KindClef
	}
	return kind + ":" + j.c.Model
}

func (j *stateJudge) Judge(ctx context.Context, a, b Side) (Verdict, error) {
	text := state(a, b)
	criteria := map[string]string{}
	for _, c := range relationChoices {
		criteria[c.value] = c.description
	}
	body := map[string]any{
		"model": j.c.Model,
		"state": json.RawMessage(text),
		"questions": map[string]any{
			"same": map[string]any{"type": "noul", "instructions": sameInstructions,
				"criteria": map[string]string{"true": "The same work", "false": "Different works"}},
			"relation": map[string]any{"type": "choice", "instructions": relationInstructions, "criteria": criteria},
		},
	}
	if j.envelope && j.c.Covers {
		var images []string
		for _, s := range []Side{a, b} {
			if len(s.Cover) > 0 {
				images = append(images, base64.StdEncoding.EncodeToString(s.Cover))
			}
		}
		if len(images) > 0 {
			body["images"] = images
		}
	}
	type answers struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	var res struct {
		answers
		Result  *answers `json:"result"`
		Success *bool    `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := post(ctx, j.c, strings.TrimRight(j.c.URL, "/")+j.path, body, &res); err != nil {
		return Verdict{}, err
	}
	got := res.answers
	if res.Result != nil {
		got = *res.Result
	}
	if res.Success != nil && !*res.Success {
		var msgs []string
		for _, e := range res.Errors {
			msgs = append(msgs, e.Message)
		}
		return Verdict{}, fmt.Errorf("decision model: %s", strings.Join(msgs, "; "))
	}
	kind := KindTypeSafe
	if j.envelope {
		kind = KindClef
	}
	v := Verdict{Judge: kind, Model: j.c.Model, Probabilities: map[string]float64{}, Tokens: got.Usage.InputTokens, At: time.Now().UTC()}
	if got.Model != "" {
		v.Model = got.Model
	}
	var same struct {
		Type        string   `json:"type"`
		Noul        *float64 `json:"noul"`
		Probability *float64 `json:"probability"`
		Value       *float64 `json:"value"`
	}
	if raw, ok := got.Answers["same"]; ok {
		// A bare number, or an object with noul / probability / value.
		var n float64
		if json.Unmarshal(raw, &n) == nil {
			v.Same = n
		} else if err := json.Unmarshal(raw, &same); err == nil {
			if same.Type == "refusal" {
				return Verdict{}, ErrRefused
			}
			for _, p := range []*float64{same.Noul, same.Probability, same.Value} {
				if p != nil {
					v.Same = *p
					break
				}
			}
		}
	} else {
		return Verdict{}, errors.New("decision model: no answer to \"same\"")
	}
	if raw, ok := got.Answers["relation"]; ok {
		var rel struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		}
		if err := json.Unmarshal(raw, &rel); err == nil {
			v.Choice, v.Confidence = rel.Choice, rel.Confidence
			for k, p := range rel.Probabilities {
				v.Probabilities[k] = p
			}
		}
	}
	if v.Tokens == 0 {
		v.Tokens = estimate(text)
	}
	return v, nil
}
