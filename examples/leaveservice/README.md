# examples/leaveservice

A minimal, runnable example of wiring the rule engine into a service.

```sh
go run ./examples/leaveservice
```

It reads the repository's own fixtures, so it needs no setup.

## What it shows

**Construction.** One engine, built at start-up with a validated `Config`, kept
for the life of the process, closed on shutdown. The fact budget is explicit
because there is no default: those numbers are a decision about how much a
decision may cost, and they belong in the service's own configuration.

**Decision points as a list.** The checks a leave application runs through are
named in one place, and `EvaluateAll` runs them together so a user sees every
problem at once instead of one per submission. This is the division of labour
the whole design rests on: *the code decides what is checked, the rules decide
how it is checked for this employee.* A new tenant policy changes a rule, not
this list.

**Shadow mode.** The engine runs beside the hard-coded check it will replace and
the two answers are compared. The legacy answer stays authoritative, so a
disagreement is a logged metric rather than a wrong decision for somebody.

The example produces a real disagreement, which is the point:

```
== shadow mode ==
DISAGREE: legacy false, rule true (C002 @ 1.1.0)
```

The legacy code requires six months of service for everyone. The rule anchored
at the Canadian company requires three, and this employee has five. That is
either a bug in the rule or a policy the legacy code never knew about — and
finding out which, before cut-over, is what shadow mode is for.

## Swapping in real infrastructure

Three lines change when this becomes a deployed service:

| Example | Production |
|---|---|
| `filestore.New("testdata")` | `servicestore.New(...)` reading from the Rule Service |
| `ruletest.NewInMemoryExecutor(...)` | the service's own `dataset.Executor`, proved with `dataset.ConformanceSuite` |
| `audit.NewSlogSink(logger)` | an `audit.Sink` writing to the audit store |

Nothing else moves. With a `CacheReloadInterval` set, also call
`engine.Start()` so the cache reconciles in the background; `Close` stops it.

## Cutting over

Shadow until the disagreements are understood and boring, then enforce one
check at a time: `off → shadow → enforce(canary tenants) → enforce(all)`, gated
on metrics rather than dates.

Do not delete the legacy code before a root-anchored baseline rule exists for
every tenant. Without one, a tenant that never configured a rule gets no
decision at all.
