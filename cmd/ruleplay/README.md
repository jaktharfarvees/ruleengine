# ruleplay

Evaluates one request against rules on disk and explains the result.

This is the fastest way to answer "why did this employee get that?". It reads
only local files and opens no connection.

## Usage

```sh
ruleplay -root testdata -request testdata/requests/leave-eligibility-br001.json
```

```
  decision:
    rule        urn:arrowhr:rule:leave:eligibility:min-service:v1 @ 1.1.0
    resolved at C002 (Company, order 1, depth 1)
    path        BR001 -> D002 -> C002 -> G001 (deepest first)
    reasons     OK
    result      eligible=true reasonCode=ELIGIBLE
    candidates:
      WINNER     ...:min-service:v1 @ 1.1.0 anchored C002
      eliminated ...:min-service:v1 @ 1.0.0 anchored G001 - Company(1) outranks Group(0)
    facts:
      joinDate                     passed (given)
      serviceMonths                derived
```

The candidate list is the part to read. It names every rule considered, the
anchor each sits at, and for each loser the reason it lost — which is normally
where a resolution surprise turns out to be an anchor at the wrong node.

## Flags

| Flag | Meaning |
|---|---|
| `-root` | directory holding `rules/`, `registry/`, `datasets/`, `hierarchy/` |
| `-request` | the request JSON (required) |
| `-rows` | dataset rows, so dataset-sourced facts resolve offline |
| `-trace` | ask the engine for a per-row evaluation trace |
| `-json` | print the whole decision as JSON instead of the report |

Exit codes: `0` the decision stands, `2` it failed closed, `1` the tool could
not run. So it is usable in a script that asserts an outcome.

## Resolving dataset facts

A rule needing a dataset-sourced field has nothing to read from unless you
supply rows. The `-rows` file maps a dataset to its rows, each row a column
map, including the tenant column a real store would carry:

```json
{
  "leave.balances": [
    {"tenantId": "RWB", "employeeId": "E1001", "leaveType": "ANNUAL",
     "balanceDays": "12.5", "asOfDate": "2026-10-14"}
  ]
}
```

Without it, the field's declared `onEmpty` applies, which is itself worth
seeing: it is what production would do if the store held no row either.

## When a decision fails

The report ends with the thing to check first for each reason code — a missing
anchor, an inactive node, a fact nobody supplied — rather than only the code.
