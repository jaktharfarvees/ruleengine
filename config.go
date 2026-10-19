package ruleengine

import (
	"fmt"
	"time"

	"github.com/arrowhr/ruleengine/fact"
)

// ValidationMode controls how strictly a request and its facts are checked.
type ValidationMode int

const (
	// ValidationStrict validates the request against the published schema and
	// every fact against the registry. It is the default and the only mode a
	// production service should run.
	ValidationStrict ValidationMode = iota
	// ValidationSchemaOnly validates the request against the published schema
	// but skips checking facts and results against the field registry. It
	// trades the registry's per-decision cost for the loss of its guarantees,
	// so a mistyped fact reaches the engine rather than being refused: use it
	// only where a caller has already validated against the same registry.
	ValidationSchemaOnly
	// ValidationOff skips boundary validation entirely. It exists for tests
	// and logs a warning on every construction, because a decision made on
	// unvalidated input is not a decision anybody should rely on.
	ValidationOff
)

// String returns the mode's name.
func (v ValidationMode) String() string {
	switch v {
	case ValidationStrict:
		return "strict"
	case ValidationSchemaOnly:
		return "schema-only"
	case ValidationOff:
		return "off"
	default:
		return fmt.Sprintf("ValidationMode(%d)", int(v))
	}
}

// Config is what a service must decide before it can evaluate anything.
//
// Everything here arrives from the consumer. The library reads no environment
// variables, so a service can audit its own configuration in one place.
type Config struct {
	// Tenant and Domain are the service's own identity. Every request is
	// checked against them, so one service cannot accidentally evaluate
	// another domain's rules or another tenant's subject.
	Tenant string
	Domain string
	// FactBudget bounds what one request may spend fetching facts. There is no
	// default: an unset budget is a configuration error, because a silent
	// default on a fact budget is a silent change to a payroll figure.
	FactBudget fact.Budget
	// Validation controls boundary strictness. The zero value is strict.
	Validation ValidationMode
	// FailClosedDomains is retained for source compatibility. V2 default
	// eligibility is controlled solely by exact, tenant-authorized
	// RuleCatalog.defaults entries; no global domain allow/deny list is used.
	//
	// Deprecated: publish or omit a tenant catalog default instead.
	FailClosedDomains []string
	// CacheReloadInterval is how often the engine reconciles its cache in the
	// background. Zero means no background goroutine at all: the service
	// reloads the cache itself, which is the right choice for a batch process
	// that wants to pin its rules for the whole run.
	CacheReloadInterval time.Duration
	// MaxCandidatesPerKey caps how many candidate rules one key may resolve
	// over. Exceeding it is a data error worth failing on rather than a cost
	// worth paying per decision.
	MaxCandidatesPerKey int
	// TraceEnabled asks the engine for a per-row evaluation trace. It is
	// useful for support and for Rule Studio, and costs allocation per
	// decision, so it is off by default.
	TraceEnabled bool
	// RequireDurableAudit refuses construction unless the configured decision
	// logger implements audit.DurableDecisionLogger. Enable it for money,
	// compliance, and any workflow whose decision record may not be lost. These
	// high-risk records include exact effective inputs in addition to their hash
	// so the decision can be reproduced; the audit store must be access-controlled.
	RequireDurableAudit bool
	// RequireIdempotency marks this engine's decisions as money-moving. Every
	// request must carry a UUID idempotencyKey and an IdempotencyStore must be
	// configured; retries return the first stored result without re-evaluation.
	RequireIdempotency bool
	// IdempotencyRetention controls how long completed keys remain reserved.
	// Zero uses the safe minimum of 24 hours. A configured value below 24 hours
	// is rejected; payroll services should normally choose a longer period.
	IdempotencyRetention time.Duration
}

// defaultMaxCandidatesPerKey is generous enough that no sane rule set reaches
// it and small enough that a runaway catalog is caught.
const defaultMaxCandidatesPerKey = 200

// Validate reports whether the configuration can produce a decision.
func (c Config) Validate() error {
	if c.Tenant == "" {
		return fmt.Errorf("%w: a tenant is required", ErrConfig)
	}
	if c.Domain == "" {
		return fmt.Errorf("%w: a domain is required", ErrConfig)
	}
	if err := c.FactBudget.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrConfig, err)
	}
	switch c.Validation {
	case ValidationStrict, ValidationSchemaOnly, ValidationOff:
	default:
		return fmt.Errorf("%w: unknown validation mode %d", ErrConfig, int(c.Validation))
	}
	if c.CacheReloadInterval < 0 {
		return fmt.Errorf("%w: cache reload interval must not be negative", ErrConfig)
	}
	if c.MaxCandidatesPerKey < 0 {
		return fmt.Errorf("%w: max candidates per key must not be negative", ErrConfig)
	}
	if c.IdempotencyRetention < 0 {
		return fmt.Errorf("%w: idempotency retention must not be negative", ErrConfig)
	}
	if c.IdempotencyRetention > 0 && c.IdempotencyRetention < minimumIdempotencyRetention {
		return fmt.Errorf("%w: idempotency retention must be at least %s", ErrConfig, minimumIdempotencyRetention)
	}
	if c.RequireIdempotency && !c.RequireDurableAudit {
		return fmt.Errorf("%w: money-moving idempotency requires durable audit", ErrConfig)
	}
	for _, domain := range c.FailClosedDomains {
		if domain == "" {
			return fmt.Errorf("%w: fail-closed domains must not contain an empty name", ErrConfig)
		}
	}
	return nil
}

func (c Config) maxCandidates() int {
	if c.MaxCandidatesPerKey > 0 {
		return c.MaxCandidatesPerKey
	}
	return defaultMaxCandidatesPerKey
}
