package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// flakyProvider fails with failErr for failures calls, then succeeds.
type flakyProvider struct {
	failures int
	failErr  error
	calls    int
}

func (f *flakyProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	f.calls++
	if f.calls <= f.failures {
		return CompletionResponse{}, f.failErr
	}
	return CompletionResponse{Content: "ok"}, nil
}

func withFastBackoff(t *testing.T) {
	t.Helper()
	orig := retryBackoff
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoff = orig })
}

func TestCompleteRetriesTransientErrors(t *testing.T) {
	withFastBackoff(t)
	p := &flakyProvider{failures: 2, failErr: errors.New(`anthropic: API call failed: POST "https://api.anthropic.com/v1/messages": 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}
	r := NewProviderRegistry("anthropic")
	r.Register("anthropic", p)

	resp, err := r.Complete(context.Background(), CompletionRequest{Model: "anthropic/claude-x"})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if resp.Content != "ok" || p.calls != 3 {
		t.Fatalf("expected 3 calls and ok, got calls=%d resp=%q", p.calls, resp.Content)
	}
}

func TestCompleteDoesNotRetryNonTransient(t *testing.T) {
	withFastBackoff(t)
	p := &flakyProvider{failures: 10, failErr: errors.New(`anthropic: API call failed: 401 authentication_error invalid x-api-key`)}
	r := NewProviderRegistry("anthropic")
	r.Register("anthropic", p)

	_, err := r.Complete(context.Background(), CompletionRequest{Model: "anthropic/claude-x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if p.calls != 1 {
		t.Fatalf("non-transient error must not retry, got %d calls", p.calls)
	}
}

func TestCompleteGivesUpAfterBackoffExhausted(t *testing.T) {
	withFastBackoff(t)
	p := &flakyProvider{failures: 10, failErr: errors.New("gemini: 503 UNAVAILABLE")}
	r := NewProviderRegistry("gemini")
	r.Register("gemini", p)

	_, err := r.Complete(context.Background(), CompletionRequest{Model: "gemini/g-x"})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if p.calls != 4 {
		t.Fatalf("expected 1 + 3 retries = 4 calls, got %d", p.calls)
	}
}
