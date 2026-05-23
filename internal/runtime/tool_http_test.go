package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPTool_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"price": 42}`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	out, err := tool.Call(context.Background(), map[string]any{"part_number": "ABC"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["price"] != float64(42) {
		t.Errorf("expected price=42, got %v", out["price"])
	}
}

func TestHTTPTool_EmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	out, err := tool.Call(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty map, got %v", out)
	}
}

func TestHTTPTool_NonObjectResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[1,2,3]`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	_, err := tool.Call(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for non-object response")
	}
	if want := "not a JSON object"; !containsString(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

func TestHTTPTool_NullBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`null`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	_, err := tool.Call(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for null response")
	}
	if want := "JSON null"; !containsString(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

func TestHTTPTool_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	_, err := tool.Call(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if want := "status 500"; !containsString(err.Error(), want) {
		t.Errorf("expected error to contain %q, got: %v", want, err)
	}
}

func TestHTTPTool_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close before calling

	tool := NewHTTPTool(url)
	_, err := tool.Call(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for closed server")
	}
}

func TestHTTPTool_ContextCancelled(t *testing.T) {
	ready := make(chan struct{})
	unblock := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(ready)
		// Block until the test releases us so srv.Close() can drain cleanly.
		select {
		case <-unblock:
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	tool := NewHTTPTool(srv.URL)

	done := make(chan error, 1)
	go func() {
		_, err := tool.Call(ctx, nil)
		done <- err
	}()

	<-ready  // wait until handler is running
	cancel() // cancel the context

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		close(unblock)
		t.Fatal("timed out waiting for cancelled call to return")
	}
	close(unblock) // release the handler so srv.Close() can proceed

	if err == nil {
		t.Fatal("expected error after context cancel")
	}
}

func TestHTTPTool_RequestBody(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	inputs := map[string]any{"part_number": "XYZ-789", "qty": float64(10)}
	tool.Call(context.Background(), inputs)

	if received["part_number"] != "XYZ-789" {
		t.Errorf("expected part_number=XYZ-789 in request body, got %v", received["part_number"])
	}
	if received["qty"] != float64(10) {
		t.Errorf("expected qty=10 in request body, got %v", received["qty"])
	}
}

func TestHTTPTool_ContentTypeHeader(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tool := NewHTTPTool(srv.URL)
	tool.Call(context.Background(), map[string]any{})

	if gotCT != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %q", gotCT)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
