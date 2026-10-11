// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// restClient is the small HTTP client the engine adapters share: JSON in
// and out, an API key header, and errors that carry the engine's message.
type restClient struct {
	base   string
	header string
	key    string
	hc     *http.Client
}

func newRESTClient(base, header, key string) *restClient {
	return &restClient{
		base: strings.TrimRight(base, "/"), header: header, key: key,
		hc: &http.Client{Timeout: 5 * time.Minute},
	}
}

// EngineError is an error answer of a search engine.
type EngineError struct {
	Status  int
	Message string
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("search engine: HTTP %d: %s", e.Status, e.Message)
}

// do sends body (JSON, or raw bytes with contentType set) and decodes the
// answer into out when it isn't nil.
func (c *restClient) do(ctx context.Context, method, path string, body any, contentType string, out any) error {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	if c.key != "" {
		req.Header.Set(c.header, c.key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Message == "" {
			msg.Message = strings.TrimSpace(string(data))
		}
		return &EngineError{Status: res.StatusCode, Message: msg.Message}
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	return json.Unmarshal(data, out)
}
