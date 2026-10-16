// Package ruleengine decides leave, attendance and payroll outcomes inside a
// consuming service.
//
// It is a library, not a service: it opens no listener and makes no network
// call of its own. A service builds one Engine at start-up, keeps it, and asks
// it for decisions. Everything the engine reaches for — the rules, the data
// behind a fact, the audit sink, the metrics — the service supplies.
//
// # A business refusal is a Decision, not an error
//
// This is the most important thing to know about the API. Evaluate returns
// (Decision, error), and the two mean different things:
//
//   - A Decision with Failed set is a decision. The rules said no, or a
//     precondition could not be met, and the reason codes say which. The
//     caller shows it to a user or records it. Nothing is broken.
//   - A non-nil error means the caller should retry, alert, or fix its
//     configuration. The engine could not get far enough to decide.
//
// So `if err != nil` is not how you find out whether leave was approved. A
// refused request and an approved one both return a nil error.
//
// # Fail closed, loudly, with a reason
//
// Every failure path produces a Decision carrying a reason code from the
// catalogue and the candidate list built so far. The engine never substitutes
// a default for a money or compliance outcome, and never returns an empty
// result with a nil error. A held decision is recoverable; a wrong payroll
// figure is not.
//
// # The six stages
//
// Each decision runs the same pipeline:
//
//  1. Resolve   — validate the envelope, place the subject in the tenant's
//     hierarchy, and pick the rule that governs them. Precedence is derived
//     from the hierarchy: the deepest anchor on the subject's path wins.
//  2. Version   — record which immutable version decided. A run manifest's pin
//     comes first, then an explicit pin, then the latest effective version.
//  3. Facts     — resolve only the winning rule's declared inputs, under a
//     budget and a timeout. The only stage that performs I/O.
//  4. Validate  — check every declared input against the field registry.
//  5. Evaluate  — run the rule's logic over the facts.
//  6. Map       — build the decision, record it for audit, measure it.
//
// # Evaluating several checks at once
//
// A real request usually has several checks. EvaluateAll runs them all and
// collects every failure, so a user sees every problem at once rather than one
// per submission, and resolves each fact once across the whole call.
//
// # Determinism
//
// The same rule version, fact-source versions, node path and business time
// always produce the same output. Time enters only through the request, as
// AsOf and ValidAt; nothing on the decision path reads a clock. That is what
// makes a payroll run reproducible months later.
package ruleengine
