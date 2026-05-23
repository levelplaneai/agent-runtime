package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPTool calls a remote HTTP endpoint to implement a tool.
// The runtime POSTs the rendered tool args as JSON and decodes the JSON response.
type HTTPTool struct {
	URL    string
	client *http.Client
}

// NewHTTPTool returns an HTTPTool that posts to url with a 30-second timeout.
func NewHTTPTool(url string) *HTTPTool {
	return &HTTPTool{
		URL:    url,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Call marshals inputs as JSON, POSTs to the tool URL, and returns the decoded response object.
// The caller's context is threaded through so cancellation and deadlines propagate.
func (t *HTTPTool) Call(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	body, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("tool HTTP call to %s: marshaling inputs: %w", t.URL, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tool HTTP call to %s: building request: %w", t.URL, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tool HTTP call to %s: %w", t.URL, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tool HTTP call to %s: reading response body: %w", t.URL, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := rawBody
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("tool HTTP call to %s: status %d: %s", t.URL, resp.StatusCode, snippet)
	}

	var out map[string]any
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("tool HTTP call to %s: response is not a JSON object: %w", t.URL, err)
	}
	// json.Unmarshal("null", &out) sets out=nil without error; treat that as an error.
	if out == nil {
		return nil, fmt.Errorf("tool HTTP call to %s: response body was JSON null", t.URL)
	}

	return out, nil
}
