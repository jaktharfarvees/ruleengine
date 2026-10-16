# 6. Use a closed validator for embedded boundary schemas

Date: 2026-10-14

## Status

Accepted for the initial offline build environment.

## Context

Phase 1 must validate seven embedded JSON Schema 2020-12 documents. The build
environment cannot download the approved `santhosh-tekuri/jsonschema/v6` module,
and adding an unavailable module would make the repository impossible to build
or test. The schemas use a deliberately small, closed keyword set.

## Decision

The schema package implements and tests exactly the keyword subset used by its
embedded schemas: local references, object and array shapes, required and
additional properties, scalar types, constants, enumerations, minimum lengths
and values, item counts, patterns, formats, and `anyOf`. Unknown schema names
and malformed embedded documents fail closed. It does not claim to validate
arbitrary third-party schemas.

## Consequences

The module remains reproducibly buildable without network access. Schema
features outside the closed embedded subset cannot be added without extending
the validator and its tests or adopting the approved full validator dependency.

