package servicestore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

func TestLoaderIsLazyRetriesAndUsesExpectedQueries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if request.URL.Path == "/bootstrap" && call == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		if request.URL.Query().Get("service") != "leave" || request.URL.Query().Get("tenant") != "T1" {
			t.Errorf("query = %q", request.URL.RawQuery)
		}
		if request.URL.Path == "/rules/changes" && request.URL.Query().Get("since") != "cursor-1" {
			t.Errorf("changes query = %q", request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"bundle": store.Bundle{}, "cursor": "cursor-2"})
	}))
	defer server.Close()
	loader, err := New(Config{BaseURL: server.URL, Service: "leave", Tenant: "T1", MaxAttempts: 2, RetryBackoff: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("New opened a connection")
	}
	bundle, err := loader.LoadAll(context.Background())
	if err != nil || bundle.Cursor != "cursor-2" || calls.Load() != 2 {
		t.Fatalf("LoadAll = cursor %q, calls %d, error %v", bundle.Cursor, calls.Load(), err)
	}
	if _, err := loader.LoadChanges(context.Background(), "cursor-1"); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderReportsStalenessAfterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	metrics := newRecordingMetrics()
	loader, err := New(Config{BaseURL: server.URL, Service: "leave", Tenant: "T1", MaxAttempts: 1, Clock: clock, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(12 * time.Second)
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("LoadAll succeeded, want HTTP error")
	}
	if got := metrics.last(telemetry.MetricCacheStaleSeconds); got != 12 {
		t.Fatalf("stale metric = %v, want 12", got)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []Config{
		{},
		{BaseURL: "ftp://example.test", Service: "leave", Tenant: "T"},
		{BaseURL: "http://example.test", Tenant: "T"},
		{BaseURL: "http://example.test", Service: "leave"},
		{BaseURL: "http://example.test", Service: "leave", Tenant: "T", MaxAttempts: -1},
		{BaseURL: "http://example.test", Service: "leave", Tenant: "T", RetryBackoff: -1},
	}
	for index, config := range tests {
		if _, err := New(config); err == nil {
			t.Errorf("New invalid config %d succeeded", index)
		}
	}
}

func TestDirectBundleHeadersAndDecodeFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Error("configured header was not sent")
		}
		_ = json.NewEncoder(writer).Encode(store.Bundle{Cursor: "direct"})
	}))
	defer server.Close()
	loader, err := New(Config{BaseURL: server.URL, Service: "leave", Tenant: "T", Headers: map[string]string{"Authorization": "Bearer token"}, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := loader.LoadAll(context.Background())
	if err != nil || bundle.Cursor != "direct" {
		t.Fatalf("direct bundle = %#v, %v", bundle, err)
	}
	for _, data := range [][]byte{[]byte(`not-json`), []byte(`{"unknown":true}`), []byte(`{} {}`)} {
		if _, err := decodeBundle(data); err == nil {
			t.Errorf("decodeBundle(%q) succeeded", data)
		}
	}
}

func TestNetworkRetriesAndContextCancellation(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, errors.New("offline")
	})}
	loader, err := New(Config{BaseURL: "http://example.test", Service: "leave", Tenant: "T", Client: client, MaxAttempts: 2, RetryBackoff: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadAll(context.Background()); err == nil || attempts.Load() != 2 {
		t.Fatalf("network retries = %d, error %v", attempts.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait canceled error = %v", err)
	}
	if err := wait(context.Background(), 0); err != nil {
		t.Fatalf("zero wait error = %v", err)
	}
}

func TestResponseReadFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
	})}
	loader, err := New(Config{BaseURL: "http://example.test", Service: "leave", Tenant: "T", Client: client, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("response read failure was hidden")
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type recordingMetrics struct {
	mu     sync.Mutex
	values map[string][]float64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{values: make(map[string][]float64)}
}

func (m *recordingMetrics) Counter(string, ...telemetry.Label) telemetry.Counter {
	return recordingInstrument{metrics: m}
}

func (m *recordingMetrics) Histogram(name string, _ ...telemetry.Label) telemetry.Histogram {
	return recordingInstrument{metrics: m, name: name}
}

func (m *recordingMetrics) last(name string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := m.values[name]
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

type recordingInstrument struct {
	metrics *recordingMetrics
	name    string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

var _ io.ReadCloser = failingBody{}

func (i recordingInstrument) Inc()              { i.Add(1) }
func (i recordingInstrument) Add(value float64) { i.Observe(value) }
func (i recordingInstrument) Observe(value float64) {
	i.metrics.mu.Lock()
	i.metrics.values[i.name] = append(i.metrics.values[i.name], value)
	i.metrics.mu.Unlock()
}
