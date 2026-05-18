package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunSiege_HitsTargetTotalTimes(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := SiegeConfig{
		URL:        srv.URL,
		Method:     "GET",
		GatewayKey: "test-key",
		Concurrent: 5,
		Total:      50,
		Timeout:    5 * time.Second,
	}

	result, err := runSiege(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("siege: %v", err)
	}

	if result.TotalRequests != 50 {
		t.Errorf("TotalRequests: got %d, want 50", result.TotalRequests)
	}
	if int(hits.Load()) != 50 {
		t.Errorf("server saw: got %d, want 50", hits.Load())
	}
	if result.StatusCounts[200] != 50 {
		t.Errorf("200 count: got %d, want 50", result.StatusCounts[200])
	}
	if result.Errors != 0 {
		t.Errorf("errors: got %d, want 0", result.Errors)
	}
}

func TestRunSiege_CountsMixedStatuses(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every 3rd request is rate-limited.
		n := hits.Add(1)
		if n%3 == 0 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := SiegeConfig{
		URL:        srv.URL,
		Method:     "GET",
		GatewayKey: "test-key",
		Concurrent: 1, // serialize for deterministic counting
		Total:      30,
		Timeout:    5 * time.Second,
	}

	result, err := runSiege(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("siege: %v", err)
	}

	if result.StatusCounts[200] != 20 {
		t.Errorf("200 count: got %d, want 20", result.StatusCounts[200])
	}
	if result.StatusCounts[429] != 10 {
		t.Errorf("429 count: got %d, want 10", result.StatusCounts[429])
	}
}

func TestRunSiege_RejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  SiegeConfig
	}{
		{"zero concurrent", SiegeConfig{URL: "http://x", Method: "GET", GatewayKey: "k", Concurrent: 0, Total: 10}},
		{"zero total", SiegeConfig{URL: "http://x", Method: "GET", GatewayKey: "k", Concurrent: 5, Total: 0}},
		{"no key", SiegeConfig{URL: "http://x", Method: "GET", GatewayKey: "", Concurrent: 5, Total: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runSiege(context.Background(), tt.cfg, nil)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestBuildSiegeURL(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		prefix string
		path   string
		want   string
	}{
		{"root path", "http://localhost:8080", "httpbin", "/", "http://localhost:8080/proxy/httpbin"},
		{"subpath", "http://localhost:8080", "httpbin", "/get", "http://localhost:8080/proxy/httpbin/get"},
		{"no leading slash", "http://localhost:8080", "httpbin", "get", "http://localhost:8080/proxy/httpbin/get"},
		{"https base", "https://api.example.com", "openai", "/v1/chat", "https://api.example.com/proxy/openai/v1/chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSiegeURL(tt.base, tt.prefix, tt.path)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPercentile(t *testing.T) {
	// Build a sorted slice of 100 durations: 1ms, 2ms, ... 100ms.
	durations := make([]time.Duration, 100)
	for i := range durations {
		durations[i] = time.Duration(i+1) * time.Millisecond
	}

	tests := []struct {
		p    float64
		want time.Duration
	}{
		{0.50, 51 * time.Millisecond},
		{0.95, 96 * time.Millisecond},
		{0.99, 100 * time.Millisecond},
		{1.00, 100 * time.Millisecond},
		{0.00, 1 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := percentile(durations, tt.p)
			if got != tt.want {
				t.Errorf("p=%g: got %s, want %s", tt.p, got, tt.want)
			}
		})
	}
}
