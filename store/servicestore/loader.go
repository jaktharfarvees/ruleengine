package servicestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

// Clock provides lifecycle time without placing wall-clock reads on the
// evaluation path.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Config defines a lazy Rule Service loader. New performs validation only and
// does not make a network connection.
type Config struct {
	BaseURL      string
	Service      string
	Tenant       string
	Headers      map[string]string
	Client       *http.Client
	MaxAttempts  int
	RetryBackoff time.Duration
	Metrics      telemetry.Metrics
	Clock        Clock
}

// Loader calls bootstrap and changes-since only when requested.
type Loader struct {
	baseURL      *url.URL
	service      string
	tenant       string
	headers      map[string]string
	client       *http.Client
	maxAttempts  int
	retryBackoff time.Duration
	metrics      telemetry.Metrics
	clock        Clock
	createdAt    time.Time
	lastSuccess  atomic.Int64
}

// New validates configuration without opening a connection.
func New(config Config) (*Loader, error) {
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("service store base URL must be an absolute HTTP URL")
	}
	if config.Service == "" || config.Tenant == "" {
		return nil, fmt.Errorf("service store requires service and tenant")
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	attempts := config.MaxAttempts
	if attempts == 0 {
		attempts = 3
	}
	if attempts < 1 {
		return nil, fmt.Errorf("service store max attempts must be positive")
	}
	backoff := config.RetryBackoff
	if backoff == 0 {
		backoff = 50 * time.Millisecond
	}
	if backoff < 0 {
		return nil, fmt.Errorf("service store retry backoff must not be negative")
	}
	metrics := config.Metrics
	if metrics == nil {
		metrics = telemetry.Nop()
	}
	clock := config.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Loader{
		baseURL: parsed, service: config.Service, tenant: config.Tenant,
		headers: cloneHeaders(config.Headers), client: client, maxAttempts: attempts,
		retryBackoff: backoff, metrics: metrics, clock: clock, createdAt: clock.Now(),
	}, nil
}

// LoadAll calls the bootstrap endpoint.
func (l *Loader) LoadAll(ctx context.Context) (store.Bundle, error) {
	return l.load(ctx, "/bootstrap", nil)
}

// LoadChanges calls the changes-since endpoint. The service response must be a
// complete coherent replacement bundle, even if its internal work is driven by
// a change cursor.
func (l *Loader) LoadChanges(ctx context.Context, since string) (store.Bundle, error) {
	query := url.Values{}
	query.Set("since", since)
	return l.load(ctx, "/rules/changes", query)
}

func (l *Loader) load(ctx context.Context, path string, query url.Values) (store.Bundle, error) {
	if l == nil {
		return store.Bundle{}, fmt.Errorf("service loader is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 1; attempt <= l.maxAttempts; attempt++ {
		bundle, retry, err := l.request(ctx, path, query)
		if err == nil {
			now := l.clock.Now()
			l.lastSuccess.Store(now.UnixNano())
			l.metrics.Histogram(telemetry.MetricCacheStaleSeconds).Observe(0)
			return bundle, nil
		}
		lastErr = err
		if !retry || attempt == l.maxAttempts {
			break
		}
		if err := wait(ctx, l.retryBackoff*time.Duration(attempt)); err != nil {
			lastErr = err
			break
		}
	}
	l.metrics.Histogram(telemetry.MetricCacheStaleSeconds).Observe(l.staleSeconds())
	return store.Bundle{}, lastErr
}

func (l *Loader) request(ctx context.Context, path string, query url.Values) (store.Bundle, bool, error) {
	endpoint := *l.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	values := endpoint.Query()
	values.Set("service", l.service)
	values.Set("tenant", l.tenant)
	for key, entries := range query {
		for _, value := range entries {
			values.Add(key, value)
		}
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return store.Bundle{}, false, fmt.Errorf("build Rule Service request: %w", err)
	}
	for key, value := range l.headers {
		request.Header.Set(key, value)
	}
	response, err := l.client.Do(request)
	if err != nil {
		return store.Bundle{}, ctx.Err() == nil, fmt.Errorf("call Rule Service: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return store.Bundle{}, retry, fmt.Errorf("Rule Service returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return store.Bundle{}, true, fmt.Errorf("read Rule Service response: %w", err)
	}
	bundle, err := decodeBundle(data)
	if err != nil {
		return store.Bundle{}, false, err
	}
	return bundle, false, nil
}

func decodeBundle(data []byte) (store.Bundle, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return store.Bundle{}, fmt.Errorf("decode Rule Service response: %w", err)
	}
	if raw, wrapped := probe["bundle"]; wrapped {
		var envelope struct {
			Bundle store.Bundle `json:"bundle"`
			Cursor string       `json:"cursor,omitempty"`
		}
		if err := strictDecode(data, &envelope); err != nil {
			return store.Bundle{}, fmt.Errorf("decode Rule Service response envelope: %w", err)
		}
		if envelope.Cursor != "" {
			envelope.Bundle.Cursor = envelope.Cursor
		}
		_ = raw
		return envelope.Bundle, nil
	}
	var bundle store.Bundle
	if err := strictDecode(data, &bundle); err != nil {
		return store.Bundle{}, fmt.Errorf("decode Rule Service bundle: %w", err)
	}
	return bundle, nil
}

func strictDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func (l *Loader) staleSeconds() float64 {
	start := l.createdAt
	if last := l.lastSuccess.Load(); last != 0 {
		start = time.Unix(0, last)
	}
	seconds := l.clock.Now().Sub(start).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

func wait(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneHeaders(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		if key != "" {
			result[key] = value
		}
	}
	return result
}
