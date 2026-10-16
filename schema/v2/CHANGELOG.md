# Schema changelog

## v2

- Initial evaluation request and response schemas.
- Initial rule, field-registry, dataset, hierarchy, and run-manifest schemas.
- RuleCatalog includes tenant-authorized, per-rule-set default policies.
- Authored rule precedence is intentionally absent; precedence is hierarchy-derived.

- Schema identifiers use the URN form the plan mandates
  (`urn:arrowhr:schema:<artifact>:v2`) rather than an HTTPS URL. Every `$ref`
  is a local fragment, so nothing resolves the identifier over the network.

The v2 contracts are governed as FULL_TRANSITIVE: required fields and their
types are immutable within v2, and additions must be optional. Breaking
changes require a new versioned schema directory. `make schema-compat` runs the
locked compatibility baseline used by CI.
