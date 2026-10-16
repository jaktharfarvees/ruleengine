package ruleengine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arrowhr/ruleengine/rule"
)

var (
	// ErrNotReady indicates that required immutable snapshots are not loaded.
	ErrNotReady = errors.New("rule engine not ready")
	// ErrHierarchyUnavailable indicates that hierarchy data cannot be read.
	ErrHierarchyUnavailable = errors.New("hierarchy unavailable")
	// ErrPinnedVersionUnavailable indicates that a required pin cannot be obtained.
	ErrPinnedVersionUnavailable = errors.New("pinned version unavailable")
	// ErrEngine indicates that the pure decision engine failed.
	ErrEngine = errors.New("decision engine failed")
	// ErrFactBudget indicates that bounded fact-fetch limits were exceeded.
	ErrFactBudget = errors.New("fact budget exceeded")
	// ErrFactTimeout indicates that a fact operation exceeded its deadline.
	ErrFactTimeout = errors.New("fact fetch timed out")
	// ErrConfig indicates invalid library construction settings.
	ErrConfig = errors.New("invalid rule engine configuration")
)

// Error carries structured, non-PII context for an operational failure.
type Error struct {
	Code    ReasonCode
	Field   string
	RuleKey rule.RuleKey
	Dataset string
	Err     error
}

// Error returns a redacted operational message.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	parts := []string{e.Code.String()}
	if e.Field != "" {
		parts = append(parts, fmt.Sprintf("field=%q", e.Field))
	}
	if e.RuleKey != "" {
		parts = append(parts, fmt.Sprintf("rule=%q", e.RuleKey))
	}
	if e.Dataset != "" {
		parts = append(parts, fmt.Sprintf("dataset=%q", e.Dataset))
	}
	if e.Err != nil {
		parts = append(parts, "cause available through errors.Unwrap")
	}
	return strings.Join(parts, ": ")
}

// Unwrap exposes the operational cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is supports errors.Is for structured codes and documented sentinels.
func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == nil {
		return false
	}
	if other, ok := target.(*Error); ok && other.Code != "" && e.Code == other.Code {
		return true
	}
	if errors.Is(e.Err, target) {
		return true
	}
	sentinel := sentinelFor(e.Code)
	return sentinel != nil && sentinel == target
}

func sentinelFor(code ReasonCode) error {
	switch code {
	case ReasonHierarchyUnavailable:
		return ErrHierarchyUnavailable
	case ReasonPinnedVersionUnavailable:
		return ErrPinnedVersionUnavailable
	case ReasonEngineError:
		return ErrEngine
	case ReasonFactBudgetExceeded:
		return ErrFactBudget
	case ReasonFactTimeout:
		return ErrFactTimeout
	default:
		return nil
	}
}
