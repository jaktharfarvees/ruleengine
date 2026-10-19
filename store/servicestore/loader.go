package servicestore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	internalclock "github.com/arrowhr/ruleengine/internal/clock"
	"github.com/arrowhr/ruleengine/store"
	"github.com/arrowhr/ruleengine/telemetry"
)

const (
	defaultMaxAttempts      = 3
	defaultRetryBackoff     = 50 * time.Millisecond
	defaultRequestTimeout   = 10 * time.Second
	defaultMaxResponseBytes = int64(32 << 20)
	maxConfiguredAttempts   = 10
	maxConfiguredBodyBytes  = int64(256 << 20)
	errorResponseDrainBytes = int64(32 << 10)
)

var forbiddenRequestHeaders = map[string]struct{}{
	"Accept":              {},
	"Accept-Encoding":     {},
	"Connection":          {},
	"Content-Length":      {},
	"Host":                {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

var errResponseTooLarge = errors.New("rule service response exceeds configured limit")

// Clock provides lifecycle time without placing wall-clock reads on the
// evaluation path.
type Clock interface{ Now() time.Time }

// Config defines a lazy Rule Service loader. New performs validation only and
// does not make a network connection. Zero values select bounded production
// defaults: three attempts, a 50 ms initial backoff, a 10 second timeout per
// attempt, and a 32 MiB maximum response.
type Config struct {
	// BaseURL is the Rule Service origin and optional path prefix. HTTPS is
	// mandatory except for an explicitly enabled loopback development server.
	BaseURL string
	Service string
	Tenant  string
	// Headers may contain authentication and tracing headers. Hop-by-hop and
	// other transport-controlled headers are rejected.
	Headers map[string]string
	// Client may supply mTLS credentials or a custom transport. The loader
	// shallow-clones it and always disables redirects.
	Client           *http.Client
	MaxAttempts      int
	RetryBackoff     time.Duration
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	// AllowInsecureHTTP permits HTTP only for localhost or a loopback IP. It is
	// intended for local tests and development, never service-to-service use.
	AllowInsecureHTTP bool
	Metrics           telemetry.Metrics
	Clock             Clock
}

// Loader calls bootstrap and changes-since only when requested.
type Loader struct {
	baseURL          *url.URL
	service          string
	tenant           string
	headers          map[string]string
	client           *http.Client
	maxAttempts      int
	retryBackoff     time.Duration
	requestTimeout   time.Duration
	maxResponseBytes int64
	metrics          telemetry.Metrics
	clock            Clock
	createdAt        time.Time
	lastSuccess      atomic.Int64
}

// New validates configuration without opening a connection.
func New(config Config) (*Loader, error) {
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("service store base URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fmt.Errorf("service store base URL must not contain credentials, query parameters, or a fragment")
	}
	if parsed.Scheme == "http" && (!config.AllowInsecureHTTP || !isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("service store base URL must use HTTPS; insecure HTTP is permitted only for an explicitly enabled loopback endpoint")
	}
	if !validIdentifier(config.Service) || !validIdentifier(config.Tenant) {
		return nil, fmt.Errorf("service store requires service and tenant")
	}
	headers, err := validateAndCloneHeaders(config.Headers)
	if err != nil {
		return nil, err
	}
	attempts := config.MaxAttempts
	if attempts == 0 {
		attempts = defaultMaxAttempts
	}
	if attempts < 1 || attempts > maxConfiguredAttempts {
		return nil, fmt.Errorf("service store max attempts must be between 1 and %d", maxConfiguredAttempts)
	}
	backoff := config.RetryBackoff
	if backoff == 0 {
		backoff = defaultRetryBackoff
	}
	if backoff < 0 {
		return nil, fmt.Errorf("service store retry backoff must not be negative")
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, fmt.Errorf("service store request timeout must be positive")
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1 || maxResponseBytes > maxConfiguredBodyBytes {
		return nil, fmt.Errorf("service store maximum response size must be between 1 and %d bytes", maxConfiguredBodyBytes)
	}
	metrics := config.Metrics
	if metrics == nil {
		metrics = telemetry.Nop()
	}
	clock := config.Clock
	if clock == nil {
		clock = internalclock.System{}
	}
	return &Loader{
		baseURL: parsed, service: config.Service, tenant: config.Tenant,
		headers: headers, client: hardenedClient(config.Client), maxAttempts: attempts,
		retryBackoff: backoff, requestTimeout: requestTimeout,
		maxResponseBytes: maxResponseBytes, metrics: metrics, clock: clock, createdAt: clock.Now(),
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
	requestContext, cancel := context.WithTimeout(ctx, l.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return store.Bundle{}, false, fmt.Errorf("build Rule Service request: %w", err)
	}
	for key, value := range l.headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Accept", "application/json")
	response, err := l.client.Do(request)
	if err != nil {
		return store.Bundle{}, ctx.Err() == nil, fmt.Errorf("call Rule Service: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, errorResponseDrainBytes))
		retry := retryableStatus(response.StatusCode)
		return store.Bundle{}, retry, fmt.Errorf("rule service returned status %d", response.StatusCode)
	}
	if !isJSONContentType(response.Header.Get("Content-Type")) {
		return store.Bundle{}, false, fmt.Errorf("rule service returned unsupported content type %q", response.Header.Get("Content-Type"))
	}
	if response.ContentLength > l.maxResponseBytes {
		return store.Bundle{}, false, fmt.Errorf("read Rule Service response: %w (%d bytes)", errResponseTooLarge, l.maxResponseBytes)
	}
	data, err := readBounded(response.Body, l.maxResponseBytes)
	if err != nil {
		retry := !errors.Is(err, errResponseTooLarge) && ctx.Err() == nil
		return store.Bundle{}, retry, fmt.Errorf("read Rule Service response: %w", err)
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

func hardenedClient(source *http.Client) *http.Client {
	if source != nil {
		client := *source
		client.CheckRedirect = rejectRedirect
		return &client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, CheckRedirect: rejectRedirect}
}

func rejectRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%w (%d bytes)", errResponseTooLarge, maximum)
	}
	return data, nil
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" ||
		(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validIdentifier(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func validateAndCloneHeaders(source map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(source))
	seen := make(map[string]struct{}, len(source))
	for key, value := range source {
		canonical := http.CanonicalHeaderKey(key)
		if !validHeaderName(key) || !validHeaderValue(value) {
			return nil, fmt.Errorf("service store header %q is invalid", key)
		}
		if _, forbidden := forbiddenRequestHeaders[canonical]; forbidden {
			return nil, fmt.Errorf("service store header %q is transport-controlled", key)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("service store header %q is duplicated", canonical)
		}
		seen[canonical] = struct{}{}
		result[canonical] = value
	}
	return result, nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !isTokenCharacter(character) {
			return false
		}
	}
	return true
}

func isTokenCharacter(character byte) bool {
	if character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))
}

func validHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\r' || character == '\n' || character == 0x7f || character < 0x20 && character != '\t' {
			return false
		}
	}
	return true
}
