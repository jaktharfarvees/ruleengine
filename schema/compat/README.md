# Schema compatibility snapshots

The files under `v2/initial` are immutable snapshots of the six published v2
contracts. `make schema-compat` compares every snapshot recursively with its
current `schema/v2` counterpart.

Within a major schema version, the compatibility policy permits only:

- new optional object properties; and
- new reusable definitions under `$defs`.

Existing properties, required sets, identifiers, types, references, enums,
conditionals, alternatives, formats, patterns, constants, bounds, and
additional-property rules are immutable. Removing a published schema is also
incompatible. Titles, descriptions, comments, examples, and defaults are
documentation and may change.

Do not edit an existing snapshot to make a failing check pass. A deliberate
breaking contract requires a new major schema directory. When an additive
release is published, preserve it as another named v2 snapshot and run the
checker against every historical v2 snapshot.
