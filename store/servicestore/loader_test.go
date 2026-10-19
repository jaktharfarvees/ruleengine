package servicestore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	config := localConfig(server.URL)
	config.MaxAttempts = 2
	config.RetryBackoff = time.Nanosecond
	loader, err := New(config)
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
	config := localConfig(server.URL)
	config.MaxAttempts = 1
	config.Clock = clock
	config.Metrics = metrics
	loader, err := New(config)
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
		{BaseURL: "https://example.test", Tenant: "T"},
		{BaseURL: "https://example.test", Service: "leave"},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", MaxAttempts: -1},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", MaxAttempts: maxConfiguredAttempts + 1},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", RetryBackoff: -1},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", RequestTimeout: -1},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", MaxResponseBytes: -1},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T", MaxResponseBytes: maxConfiguredBodyBytes + 1},
		{BaseURL: "https://user:password@example.test", Service: "leave", Tenant: "T"},
		{BaseURL: "https://example.test?token=secret", Service: "leave", Tenant: "T"},
		{BaseURL: "https://example.test#fragment", Service: "leave", Tenant: "T"},
		{BaseURL: "http://example.test", Service: "leave", Tenant: "T", AllowInsecureHTTP: true},
		{BaseURL: "https://example.test", Service: " leave", Tenant: "T"},
		{BaseURL: "https://example.test", Service: "leave", Tenant: "T\n2"},
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
		writer.Header().Set("Content-Type", "application/vnd.arrowhr.bundle+json")
		_ = json.NewEncoder(writer).Encode(store.Bundle{Cursor: "direct"})
	}))
	defer server.Close()
	config := localConfig(server.URL)
	config.Headers = map[string]string{"Authorization": "Bearer token"}
	config.MaxAttempts = 1
	loader, err := New(config)
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
	loader, err := New(Config{BaseURL: "https://example.test", Service: "leave", Tenant: "T", Client: client, MaxAttempts: 2, RetryBackoff: time.Nanosecond})
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
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: failingBody{}}, nil
	})}
	loader, err := New(Config{BaseURL: "https://example.test", Service: "leave", Tenant: "T", Client: client, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("response read failure was hidden")
	}
}

func TestHTTPSIsRequiredUnlessLoopbackHTTPIsExplicitlyEnabled(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://127.0.0.1:8080", Service: "leave", Tenant: "T"}); err == nil {
		t.Fatal("unencrypted loopback URL succeeded without explicit opt-in")
	}
	if _, err := New(localConfig("http://127.0.0.1:8080")); err != nil {
		t.Fatalf("explicit loopback HTTP failed: %v", err)
	}
	if _, err := New(localConfig("http://[::1]:8080")); err != nil {
		t.Fatalf("explicit IPv6 loopback HTTP failed: %v", err)
	}
}

func TestLoaderRejectsRedirectWithoutLeakingCredentials(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	config := localConfig(origin.URL)
	config.Headers = map[string]string{"Authorization": "Bearer secret"}
	config.MaxAttempts = 1
	loader, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadAll(context.Background()); err == nil || !strings.Contains(err.Error(), "status 307") {
		t.Fatalf("redirect error = %v", err)
	}
	if redirected.Load() {
		t.Fatal("redirect target was called")
	}
}

func TestLoaderEnforcesResponseContract(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		maximum     int64
		want        string
	}{
		{name: "missing content type", body: `{}`, want: "unsupported content type"},
		{name: "non JSON content type", contentType: "text/plain", body: `{}`, want: "unsupported content type"},
		{name: "invalid content type", contentType: "application/json; charset=", body: `{}`, want: "unsupported content type"},
		{name: "oversized response", contentType: "application/json", body: `{"cursor":"too-large"}`, maximum: 8, want: "exceeds configured limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("Accept"); got != "application/json" {
					t.Errorf("Accept = %q", got)
				}
				if test.contentType != "" {
					writer.Header().Set("Content-Type", test.contentType)
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			config := localConfig(server.URL)
			config.MaxAttempts = 1
			config.MaxResponseBytes = test.maximum
			loader, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.LoadAll(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadAll error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoaderBoundsEveryAttemptWithAContextDeadline(t *testing.T) {
	var sawDeadline atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, sawDeadlineValue := request.Context().Deadline()
		sawDeadline.Store(sawDeadlineValue)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	loader, err := New(Config{
		BaseURL: "https://example.test", Service: "leave", Tenant: "T", Client: client,
		MaxAttempts: 1, RequestTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("timed-out request succeeded")
	}
	if !sawDeadline.Load() {
		t.Fatal("request did not carry a deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request timeout took %v", elapsed)
	}
}

func TestLoaderRetriesOnlyTransientStatuses(t *testing.T) {
	for _, test := range []struct {
		status int
		calls  int32
	}{
		{status: http.StatusServiceUnavailable, calls: 2},
		{status: http.StatusNotImplemented, calls: 1},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
			}))
			defer server.Close()
			config := localConfig(server.URL)
			config.MaxAttempts = 2
			config.RetryBackoff = time.Nanosecond
			loader, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = loader.LoadAll(context.Background())
			if got := calls.Load(); got != test.calls {
				t.Fatalf("calls = %d, want %d", got, test.calls)
			}
		})
	}
}

func TestLoaderRejectsUnsafeOrAmbiguousHeaders(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Accept": "text/plain"},
		{"Accept-Encoding": "gzip"},
		{"Connection": "close"},
		{"Host": "attacker.test"},
		{"Bad Header": "value"},
		{"X-Test": "safe\r\nInjected: true"},
		{"Authorization": "one", "authorization": "two"},
	} {
		config := Config{BaseURL: "https://example.test", Service: "leave", Tenant: "T", Headers: headers}
		if _, err := New(config); err == nil {
			t.Errorf("New accepted headers %#v", headers)
		}
	}
}

func TestDefaultClientUsesModernTLSAndCallerClientIsNotMutated(t *testing.T) {
	loader, err := New(Config{BaseURL: "https://example.test", Service: "leave", Tenant: "T"})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := loader.client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("default transport TLS config = %#v", loader.client.Transport)
	}
	originalRedirect := func(*http.Request, []*http.Request) error { return nil }
	original := &http.Client{CheckRedirect: originalRedirect}
	loader, err = New(Config{BaseURL: "https://example.test", Service: "leave", Tenant: "T", Client: original})
	if err != nil {
		t.Fatal(err)
	}
	if original.CheckRedirect == nil {
		t.Fatal("caller's client was mutated")
	}
	if err := original.CheckRedirect(nil, nil); err != nil {
		t.Fatalf("caller's redirect policy was changed: %v", err)
	}
	if loader.client == original {
		t.Fatal("loader retained the caller's mutable client pointer")
	}
	if err := loader.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("loader redirect policy error = %v", err)
	}
}

func localConfig(baseURL string) Config {
	return Config{BaseURL: baseURL, Service: "leave", Tenant: "T1", AllowInsecureHTTP: true}
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
