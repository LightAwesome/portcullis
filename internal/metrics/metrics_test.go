package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordRequest_IncrementsCounter(t *testing.T) {
	requestsTotal.Reset()
	requestDurationSeconds.Reset()

	RecordRequest("GET", "httpbin", "200", "test-client", 0.05)
	RecordRequest("GET", "httpbin", "200", "test-client", 0.07)
	RecordRequest("GET", "httpbin", "429", "test-client", 0.01)

	if got := testutil.ToFloat64(requestsTotal.WithLabelValues("GET", "httpbin", "200", "test-client")); got != 2 {
		t.Errorf("200 count: got %v, want 2", got)
	}
	if got := testutil.ToFloat64(requestsTotal.WithLabelValues("GET", "httpbin", "429", "test-client")); got != 1 {
		t.Errorf("429 count: got %v, want 1", got)
	}
}

func TestRecordRateLimited_IncrementsCounter(t *testing.T) {
	rateLimitedTotal.Reset()

	RecordRateLimited("client-a", "openai")
	RecordRateLimited("client-a", "openai")
	RecordRateLimited("client-b", "openai")

	if got := testutil.ToFloat64(rateLimitedTotal.WithLabelValues("client-a", "openai")); got != 2 {
		t.Errorf("client-a: got %v, want 2", got)
	}
	if got := testutil.ToFloat64(rateLimitedTotal.WithLabelValues("client-b", "openai")); got != 1 {
		t.Errorf("client-b: got %v, want 1", got)
	}
}

func TestRecordUpstreamError_CategoryLabel(t *testing.T) {
	upstreamErrorsTotal.Reset()

	RecordUpstreamError("openai", "timeout")
	RecordUpstreamError("openai", "timeout")
	RecordUpstreamError("openai", "unreachable")

	if got := testutil.ToFloat64(upstreamErrorsTotal.WithLabelValues("openai", "timeout")); got != 2 {
		t.Errorf("timeout: got %v, want 2", got)
	}
	if got := testutil.ToFloat64(upstreamErrorsTotal.WithLabelValues("openai", "unreachable")); got != 1 {
		t.Errorf("unreachable: got %v, want 1", got)
	}
}

func TestRecordLogDropped_IncrementsCounter(t *testing.T) {
	// Counter, not CounterVec, so it has no Reset method.
	before := testutil.ToFloat64(logsDroppedTotal)

	RecordLogDropped()
	RecordLogDropped()

	after := testutil.ToFloat64(logsDroppedTotal)
	if got := after - before; got != 2 {
		t.Errorf("dropped logs increment: got %v, want 2", got)
	}
}

func TestOrUnknown(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"foo", "foo"},
		{"", "unknown"},
		{" ", " "}, // whitespace is not empty
	}

	for _, tt := range tests {
		if got := orUnknown(tt.in); got != tt.want {
			t.Errorf("orUnknown(%q): got %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRecordRequest_EmptyLabelsBecomeUnknown(t *testing.T) {
	requestsTotal.Reset()
	requestDurationSeconds.Reset()

	RecordRequest("", "httpbin", "200", "", 0.01)

	if got := testutil.ToFloat64(requestsTotal.WithLabelValues("unknown", "httpbin", "200", "unknown")); got != 1 {
		t.Errorf("unknown-labeled counter: got %v, want 1", got)
	}
}
