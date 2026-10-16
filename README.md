# ArrowHR Rule Engine

ArrowHR Rule Engine is a shared Go library for deterministic, explainable leave,
attendance, and payroll decisions inside consuming services. It is a library,
not a service: it exposes no HTTP or gRPC server and opens no network connection
of its own.

## Status

The repository is being implemented in gated phases. Phases 0 to 9 are complete
and have been audited against implementation plan v2.0: the core types and
reason-code catalogue, the hierarchy snapshot and cache, deterministic
resolution, the field registry and engine interface, the production
decision-table engine with a closed expression grammar and exact-decimal
arithmetic, datasets with bounded fact resolution over all four fact kinds, and
the rule store with its copy-on-write cache, loaders and version selection, and
the six-stage evaluation pipeline with audit, telemetry and authorisation, and
the developer tools, example wiring and test kit. The release documentation is
next. The module is not yet released.

Two offline tools come with it. `cmd/rulelint` validates rules, registries,
manifests and hierarchies against each other before anything is published;
`cmd/ruleplay` evaluates one request against local files and explains which
rule won, at which anchor, and why every other candidate lost. See
[examples/leaveservice](examples/leaveservice) for the wiring a service copies,
including the shadow-mode pattern for adopting a rule without betting on it.

A business refusal is a `Decision` with `Failed` set and a reason code, never a
Go error; an error means retry, alert or fix configuration. See the package
documentation for the distinction and the six stages.

A field's value comes from a declared fact source, never from raw SQL: the
library compiles a validated query and the owning service executes it, so all
fourteen services inherit the same guardrails. A service implements
`dataset.Executor` and proves it with `dataset.ConformanceSuite`.

Decisions are recorded in [docs/adr](docs/adr); questions still open with the
architect, and gaps a later phase closes, are listed in
[docs/OPEN_QUESTIONS.md](docs/OPEN_QUESTIONS.md).

Resolution returns the complete, stably ordered candidate explanation. Registry
validation rejects unknown or mistyped values and prevents hierarchy placement
from being authored as a subject filter.

The module targets Go 1.24 and follows semantic versioning. After the first
release, the current and previous minor versions will receive fixes while
consumer services upgrade.

## Development

Run the local quality gate:

```sh
make check
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites and workflow. Full
integration guidance and examples will be added with the corresponding
implementation phases.

## Security

Report vulnerabilities through the private process in [SECURITY.md](SECURITY.md).
Do not include employee or tenant data in a public issue.

## License

MIT. See [LICENSE](LICENSE).

