package ruleengine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/arrowhr/ruleengine/schema"
)

// newDecisionID returns a random RFC 4122 version 4 identifier.
//
// Every decision gets one, and an audit record is found by it months later, so
// it has to be unique across every replica of every service without
// coordination — which is what a random 122-bit identifier gives.
//
// It is generated here rather than taken from a dependency because the module
// has none (see docs/adr/0005-dependency-choices.md), and a v4 identifier is
// sixteen random bytes with six bits set.
func newDecisionID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		// crypto/rand does not fail on any supported platform, and a decision
		// is worth more than its identifier, so this degrades rather than
		// refuses: the identifier says plainly that it is not unique.
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40 // version 4
	bytes[8] = (bytes[8] & 0x3f) | 0x80 // RFC 4122 variant
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], bytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], bytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], bytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], bytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], bytes[10:16])
	return string(encoded)
}

// validateRequestSchema validates a request against the published boundary
// schema, which is what ValidationStrict adds over the field checks.
//
// It runs on the request as it arrives, so a consumer that assembled one in Go
// is held to the same contract as one that decoded it from JSON.
func validateRequestSchema(request EvalRequest) error {
	if err := schema.Validate(schema.EvaluationRequest, request); err != nil {
		return fmt.Errorf("evaluation request failed schema validation: %w", err)
	}
	return nil
}

// ValidateRequest validates a request against the published boundary schema.
//
// It is exported so a service can validate once where JSON enters the system —
// its HTTP handler — rather than paying for it on every evaluation. The schema
// walk is the most expensive part of a decision by a wide margin: around 120
// microseconds and 660 allocations, against 17 microseconds for the rest of
// the pipeline. See docs/PERFORMANCE.md.
//
// ValidationStrict runs this on every Evaluate, which is the right default for
// a service whose requests arrive as JSON from elsewhere. A service that
// builds its requests in Go, where the type system already guarantees their
// shape, is paying for a check it cannot fail.
func ValidateRequest(request EvalRequest) error { return validateRequestSchema(request) }
