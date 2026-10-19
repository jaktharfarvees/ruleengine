# Contributing

Use Go 1.26 or later. Use the exact development toolchain recorded in
`.go-version` when possible; CI separately exercises the minimum supported Go
line. Keep changes phase-scoped and add table-driven tests beside the
implementation. Every exported identifier must have a Go doc comment, and
evaluation code must not read a clock or perform I/O.

Before opening a pull request, run:

```sh
make check
```

Do not add dependencies outside the approved build specification without an
architecture decision record. Do not commit employee data, credentials, build
caches, generated binaries, or coverage output.

Commits should be focused and the changelog should describe user-visible API or
behavior changes. Breaking changes to public packages require a major version.

