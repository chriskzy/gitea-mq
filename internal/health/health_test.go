package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/health"
)

func get(t *testing.T, h http.Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestReadiness(t *testing.T) {
	var dbErr error
	calls := 0
	now := time.Unix(0, 0)
	r := &health.Readiness{
		Checks: []health.Check{
			{Name: "database", Probe: func(context.Context) error { calls++; return dbErr }},
			{Name: "gitea", Probe: func(context.Context) error { return nil }},
		},
		Timeout: time.Second,
		TTL:     10 * time.Second,
		Now:     func() time.Time { return now },
	}

	code, body := get(t, r)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthy: code=%d body=%v", code, body)
	}

	// Within the TTL the cached answer is served without probing.
	dbErr = errors.New("connection refused")
	if code, _ := get(t, r); code != http.StatusOK || calls != 1 {
		t.Fatalf("cached: code=%d calls=%d", code, calls)
	}

	now = now.Add(11 * time.Second)
	code, body = get(t, r)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("failing db: code=%d want 503", code)
	}
	checks := body["checks"].(map[string]any)
	if checks["database"] != "connection refused" || checks["gitea"] != "ok" {
		t.Fatalf("checks = %v", checks)
	}
}

func TestReadiness_Timeout(t *testing.T) {
	r := &health.Readiness{
		Checks: []health.Check{{Name: "gitea", Probe: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}},
		Timeout: 10 * time.Millisecond,
	}
	if code, _ := get(t, r); code != http.StatusServiceUnavailable {
		t.Fatalf("hung probe: code=%d want 503", code)
	}
}
