# 8. Provide an exact decimal foundation in the offline build

Date: 2026-10-14

## Status

Accepted for the initial offline build environment.

## Context

The build specification selects `github.com/shopspring/decimal` for exact money
and rate arithmetic. The build environment cannot download modules that are not
already cached, and the dependency is not present locally. Adding an unresolved
runtime dependency would make the library impossible to compile or test.

## Decision

`internal/decimalx` currently implements immutable exact decimals over
`math/big.Rat`, rejects binary floating-point input, preserves explicit scale,
rounds deterministically, and applies ISO 4217 minor-unit scales. Its API is
internal so this temporary implementation creates no consumer-facing type
commitment.

## Consequences

Money and rate calculations can remain exact during the offline build, including
the required `0.1 + 0.2 == 0.3` behavior. When dependency resolution is
available, `internal/decimalx` can be reimplemented over the approved module
without changing any public package API.
