package local

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newTestKeygenClient(t *testing.T, handler http.HandlerFunc) *keygenClient {
	t.Helper()
	client := newKeygenClient("https://keygen.test", "acct_test", "lic_test")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := &responseRecorder{header: make(http.Header)}
		handler.ServeHTTP(rec, req)
		return rec.Response(), nil
	})
	return client
}

type responseRecorder struct {
	code   int
	header http.Header
	body   strings.Builder
}

func (r *responseRecorder) Header() http.Header {
	return r.header
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *responseRecorder) Response() *http.Response {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return &http.Response{
		StatusCode: r.code,
		Header:     r.header.Clone(),
		Body:       io.NopCloser(strings.NewReader(r.body.String())),
	}
}

func TestFetchLicense_active(t *testing.T) {
	expiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "License lic_test" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attributes": map[string]any{
					"status": "ACTIVE",
					"expiry": expiry,
					"metadata": map[string]any{
						"max_users": float64(10),
					},
				},
			},
		})
	})

	lic, err := client.FetchLicense(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lic.Status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", lic.Status)
	}
	if lic.MaxUsers != 10 {
		t.Errorf("MaxUsers = %d, want 10", lic.MaxUsers)
	}
	if lic.Expiry == nil {
		t.Error("Expiry should not be nil")
	}
}

func TestFetchLicense_httpError(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := client.FetchLicense(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestActivateMachine_created(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if err != nil {
		t.Fatalf("unexpected error on 201: %v", err)
	}
}

func TestActivateMachine_conflict(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if err != nil {
		t.Fatalf("409 should be treated as success, got: %v", err)
	}
}

func TestActivateMachine_limitExceeded(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if !errors.Is(err, ErrMachineLimitExceeded) {
		t.Errorf("expected ErrMachineLimitExceeded, got %v", err)
	}
}

func TestValidateMachine_active(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "machine-1"}},
		})
	})
	active, err := client.ValidateMachine(context.Background(), "fp-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !active {
		t.Error("expected active=true")
	}
}

func TestValidateMachine_deactivated(t *testing.T) {
	client := newTestKeygenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	active, err := client.ValidateMachine(context.Background(), "fp-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active {
		t.Error("expected active=false for empty data")
	}
}
