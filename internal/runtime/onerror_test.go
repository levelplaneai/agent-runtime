package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aditya-vinodh/agent-runtime/internal/bundle"
)

var errTest = errors.New("test error")

func nodeWithOnError(t *testing.T, onError string) bundle.Node {
	t.Helper()
	return bundle.Node{OnError: onError}
}

// alwaysFails returns a fn that always returns errTest.
func alwaysFails() func() (map[string]any, error) {
	return func() (map[string]any, error) {
		return nil, errTest
	}
}

// succeedsOnAttempt returns a fn that fails the first (n-1) calls then succeeds.
func succeedsOnAttempt(n int) func() (map[string]any, error) {
	calls := 0
	return func() (map[string]any, error) {
		calls++
		if calls < n {
			return nil, errTest
		}
		return map[string]any{"ok": true}, nil
	}
}

func TestApplyErrorPolicy_Fail(t *testing.T) {
	node := nodeWithOnError(t, "fail")
	_, err := ApplyErrorPolicy(context.Background(), "", node, alwaysFails())
	if !errors.Is(err, errTest) {
		t.Fatalf("want errTest, got %v", err)
	}
}

func TestApplyErrorPolicy_FailDefault(t *testing.T) {
	// Empty on_error should behave identically to "fail".
	node := nodeWithOnError(t, "")
	_, err := ApplyErrorPolicy(context.Background(), "", node, alwaysFails())
	if !errors.Is(err, errTest) {
		t.Fatalf("want errTest, got %v", err)
	}
}

func TestApplyErrorPolicy_FailSuccess(t *testing.T) {
	node := nodeWithOnError(t, "fail")
	out, err := ApplyErrorPolicy(context.Background(), "", node, func() (map[string]any, error) {
		return map[string]any{"x": 1}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["x"] != 1 {
		t.Errorf("got %v, want x=1", out)
	}
}

func TestApplyErrorPolicy_Skip(t *testing.T) {
	node := nodeWithOnError(t, "skip")
	out, err := ApplyErrorPolicy(context.Background(), "", node, alwaysFails())
	if err != nil {
		t.Fatalf("skip should suppress error, got %v", err)
	}
	if out == nil {
		t.Fatal("skip should return non-nil empty map")
	}
	if len(out) != 0 {
		t.Errorf("skip should return empty map, got %v", out)
	}
}

func TestApplyErrorPolicy_SkipSuccess(t *testing.T) {
	// skip should pass through a successful result unchanged.
	node := nodeWithOnError(t, "skip")
	out, err := ApplyErrorPolicy(context.Background(), "", node, func() (map[string]any, error) {
		return map[string]any{"v": 42}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["v"] != 42 {
		t.Errorf("got %v, want v=42", out)
	}
}

func TestApplyErrorPolicy_RetrySucceedsOnSecondAttempt(t *testing.T) {
	node := nodeWithOnError(t, "retry:3")
	out, err := ApplyErrorPolicy(context.Background(), "", node, succeedsOnAttempt(2))
	if err != nil {
		t.Fatalf("expected success on attempt 2, got error: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("got %v, want ok=true", out)
	}
}

func TestApplyErrorPolicy_RetryExhausted(t *testing.T) {
	// retry:2 means 3 total attempts; alwaysFails should exhaust them.
	node := nodeWithOnError(t, "retry:2")
	_, err := ApplyErrorPolicy(context.Background(), "", node, alwaysFails())
	if !errors.Is(err, errTest) {
		t.Fatalf("want errTest after exhausted retries, got %v", err)
	}
}

func TestApplyErrorPolicy_RetryContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	fn := func() (map[string]any, error) {
		attempts++
		if attempts == 1 {
			cancel() // cancel after the first attempt
		}
		return nil, errTest
	}

	node := nodeWithOnError(t, "retry:5")
	_, err := ApplyErrorPolicy(ctx, "", node, fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected fn called once before cancel, got %d", attempts)
	}
}

func TestApplyErrorPolicy_InvalidPolicy(t *testing.T) {
	cases := []string{"retry", "retry:0", "retry:-1", "retry:abc", "foo", "FAIL"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			node := nodeWithOnError(t, raw)
			_, err := ApplyErrorPolicy(context.Background(), "", node, alwaysFails())
			if err == nil {
				t.Errorf("on_error %q: expected parse error, got nil", raw)
			}
		})
	}
}
