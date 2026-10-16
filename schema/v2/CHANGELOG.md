# Schema changelog

## v2

- Initial evaluation request and response schemas.
- Initial rule, field-registry, dataset, hierarchy, and run-manifest schemas.
- Authored rule precedence is intentionally absent; precedence is hierarchy-derived.

- Schema identifiers use the URN form the plan mandates
  (`urn:arrowhr:schema:<artifact>:v2`) rather than an HTTPS URL. Every `$ref`
  is a local fragment, so nothing resolves the identifier over the network.
