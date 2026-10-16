# Resolution

How the library decides which rule governs one employee.

This is the heart of the design, and the part worth reading carefully: get the
mental model right and the rest follows.

## The model

A tenant's organisation is **not** a fixed ladder. It is whatever that tenant
defines: an ordered list of levels, each with a type the tenant names, and as
few as one or as many as they need. A rule is **anchored to exactly one place**
— one node, or one employee.

> Compute the subject's node path. Among the rules whose anchor lies on that
> path, **the one anchored furthest from the root wins** — whatever the levels
> are called and however many there are.

That one sentence replaces the six fixed precedence levels of plan v1.0, and it
is why a tenant can insert a Team level between Department and Branch years
after go-live without touching a rule, a schema or a line of code. See
[ADR 2](adr/0002-precedence-derived-from-hierarchy.md).

**Filters narrow, they never promote.** A filter on `employeeGroup` restricts
which subjects a rule covers *within* its anchor. It cannot lift a rule above a
rule anchored deeper. This is the single most common misunderstanding, and
`RT06` exists to keep it true.

**Precedence is derived, never authored.** There is no precedence field on a
rule. `hierarchy.DerivePrecedence` is the only thing that produces a
`DerivedPrecedence`, and it reads the tenant's own tree.

## The algorithm

`resolve.Resolve` is pure: no I/O, no clock, no logging, no mutation. Given a
path, a candidate set and a hierarchy snapshot it is a function.

1. The **path** arrives deepest first: index 0 is the subject's own node, the
   last entry is the tenant root.
2. Drop any candidate whose **tenant** differs. Record `cross-tenant`. This
   cannot happen through the catalog, which is tenant-keyed, so it is defence in
   depth and an alertable anomaly if it ever fires.
3. Drop any candidate whose **status** is not evaluable. Only `active` and
   `scheduled` are.
4. Drop any candidate whose **effective interval** does not contain the
   business time.
5. Keep candidates whose **anchor is on the path**. An employee anchor matches
   only that employee; a node anchor matches when its node appears anywhere on
   the path. Record `not-on-path` for the rest.
6. Keep candidates whose **filters all match**. Record `filters` and a
   human-readable reason for the rest.
7. If any survivor is an **employee exception**: exactly one wins immediately;
   more than one is a data error and returns a tie.
8. Otherwise rank by **anchor order descending, then depth descending**. Order
   is primary because it is the declared semantic level and survives a ragged
   tree; depth breaks the case where one level repeats on a single path.
9. If exactly one holds the top rank, **it wins**.
10. Otherwise break by **filter count descending** — more specific wins.
11. Otherwise, if every remaining candidate has a **tie-break priority**, the
    lowest wins.
12. Otherwise **return a tie**. Do not pick one.
13. Mark the winner; mark every other survivor `lower-order` with a reason
    naming both level names and orders.

Candidate ordering is stable and independent of map iteration: candidates are
sorted by `(order desc, depth desc, filters desc, priority asc, key asc,
version asc)` before any decision, so two runs over the same data produce the
same `Evaluated` list byte for byte.

### Versions are resolved before anchors

A *version* carries its own anchor. Publishing a company override is a new
version of the same rule key — plan Appendix A.2 does exactly that, and says
"this version wins over A.1 for Canada employees".

So the store collapses versions **before** resolution sees them: for each
distinct scope (anchor plus filters), the latest version that is evaluable and
effective at the business time. Without that collapse, a routine version bump
would hand resolution two versions of one rule at one anchor, which it would
correctly report as an unresolved tie — failing every decision for that rule.
See [ADR 10](adr/0010-store-selects-the-latest-version-per-rule.md).

A run manifest's pin therefore constrains **which rules can win**, not which
version the winner must be. A run pinned to 1.0.0 sees the rule set as it was
at 1.0.0, which is what makes a payroll re-run reproducible.

## The RWB worked example

The fixture tenant, four levels deep:

```
G001  RWB                        (group,      order 0)
 └─ C002  RWB CA                 (company,    order 1)   country Canada
     ├─ D002  Engineering        (department, order 2)
     │   ├─ BR001  Colombo 03    (branch,     order 3)   timezone Asia/Colombo
     │   └─ BR002  Kandy         (branch,     order 3)
     └─ D003  Finance            (department, order 2)
         └─ BR003  Galle         (branch,     order 3)
```

`BR001` sits under the **Canadian** company but declares a **Sri Lankan**
timezone. That oddity is deliberate: it proves each locale attribute resolves
from its own nearest declaring ancestor, independently. Do not clean it up.

### Trace: leave eligibility for E1001 at BR001

Two rules share the key `urn:arrowhr:rule:leave:eligibility:min-service:v1`:

| Version | Anchor | Logic |
|---|---|---|
| 1.0.0 | `G001` | `serviceMonths >= 6` |
| 1.1.0 | `C002` | `serviceMonths >= 3` |

The employee joined five months ago.

**Step 1 — path.** `BR001 → D002 → C002 → G001`, deepest first.

**Step 2–4 — drops.** Both are RWB, both active, both effective. Nothing
dropped.

**Step 5 — anchors on path.** `C002` is on the path (index 2). `G001` is on the
path (index 3). Both survive. A rule anchored at `BR003` would be dropped here,
because Galle is on a different branch.

**Step 6 — filters.** Neither has any.

**Step 7 — employee exceptions.** None.

**Step 8 — rank.** `C002` is a company, order 1, depth 1. `G001` is the group,
order 0, depth 0. Company outranks group.

**Step 9 — winner.** Version 1.1.0, anchored `C002`.

**The result.** Five months meets `>= 3`, so the employee is eligible — and
would not have been under the group's six-month baseline. That difference is
the point of the whole design.

**The explanation**, which `ruleplay` prints and the audit record stores:

```
rule        urn:arrowhr:rule:leave:eligibility:min-service:v1 @ 1.1.0
resolved at C002 (Company, order 1, depth 1)
path        BR001 -> D002 -> C002 -> G001 (deepest first)
candidates:
  WINNER     1.1.0 anchored C002
  eliminated 1.0.0 anchored G001 — Company(1) outranks Group(0)
```

Move the same employee to `BR004` — under `D004`, under `C004`, a different
company — and the Canadian anchor is no longer on their path. The group
baseline wins, and five months is not enough.

### Attributes resolve independently

For the same employee at `BR001`:

| Attribute | Value | Declared by |
|---|---|---|
| country | Canada | `C002` |
| currency | CAD | `C002` |
| locale | en-CA | `C002` |
| timezone | Asia/Colombo | `BR001` |

Each walks up from the subject's node until it finds a node that declares it.
That is why a Colombo branch of a Canadian company reports both truthfully, and
why a rule should anchor at the node it governs rather than filter on something
that looks like a location.

## The tie policy

**An unresolved tie fails closed and is reported. The engine never guesses.**

Two active rules can end up equally ranked: same anchor, equally specific
filters, no tie-break priority. The RT07 fixtures are exactly that.

An engine could pick one by incidental order, substitute a default, or refuse.
Picking one makes the decision depend on file order, so the same employee can
get different answers on different replicas and a payroll replay months later
cannot be reproduced. Substituting a default invents a policy nobody authored,
in a domain where the output is somebody's pay.

So resolution returns `Tie`, `FailClosed` and `PRECEDENCE_TIE_UNRESOLVED`,
listing every candidate still tied, and the pipeline increments a counter meant
to alert. A held decision is recoverable — an administrator sets a priority or
narrows a filter. A wrong payroll figure is not.

This pushes the work to publish time, where it belongs. `rulelint` warns about
the pair before anything is published, and the Rule Service's overlap lint
should make it nearly impossible. A tie in production is a near-miss worth
alerting on, not routine noise. See
[ADR 4](adr/0004-fail-closed-on-tie.md).

## The fail-closed policy

Every failure produces a `Decision` with `Failed` set, a reason code, and the
candidate list built so far. Never an empty result with a nil error, and never
a substituted default for a money or compliance outcome.

A business refusal is also a `Decision`, with `Failed` **unset**: the rules
said no, and nothing is broken. Only a Go error means retry, alert or fix
configuration.

| Situation | Reason code | Fails? |
|---|---|---|
| no hierarchy for the tenant | `HIERARCHY_UNAVAILABLE` | yes |
| the subject's node is unknown or inactive | `SUBJECT_NODE_UNRESOLVABLE` | yes |
| no rule's anchor is on the path | `NO_RULE_RESOLVED` | yes |
| two rules equally ranked | `PRECEDENCE_TIE_UNRESOLVED` | yes |
| a rule said no | `OK` | **no** |

## RT01 to RT14

The resolution suite is the contract with Rule Studio: the same fixtures prove
the same outcomes in both implementations. Each test exists to protect one
behaviour from a plausible regression.

| | Behaviour | What breaks without it |
|---|---|---|
| RT01 | deeper anchor wins | the whole precedence model |
| RT02 | a department rule covers both its branches | subtree coverage; teams would anchor per branch |
| RT03 | the root baseline applies when nothing deeper matches | a tenant with one rule gets no decision |
| RT04 | an employee exception beats every node anchor | approved individual exceptions silently ignored |
| RT05 | an off-path anchor never wins | a rule for Galle deciding for Kandy |
| RT06 | filters narrow but never promote | the most common misreading of the model |
| RT07 | overlapping filters at one anchor tie and fail closed | a guessed answer in a money domain |
| RT08 | inserting a level does not change an existing outcome | the reason the fixed-level model was replaced |
| RT09 | attributes resolve from the nearest declaring ancestor, independently | a Colombo branch reported as Canadian |
| RT10 | inactive nodes fail closed | decisions made against a closed branch |
| RT11 | a cross-tenant candidate is dropped and counted | a tenant boundary crossed silently |
| RT12 | an explicit priority resolves a same-anchor overlap | no way to fix a tie without editing filters |
| RT13 | a repeated level on one path is decided by depth | a ragged tree ranking wrongly |
| RT14 | the evaluated list is deterministic | audit records that differ between replicas |

Three property tests back them: monotonicity (a strictly deeper matching anchor
always wins), determinism (1000 random fixtures resolved twice are identical),
and no-panic (never panics on any generated input, including empty paths, nil
filters and malformed anchors).

## Reading a surprise

When a decision is not what somebody expected, in order:

1. Run `ruleplay` against the same rules and request. The candidate list names
   every rule considered and why each lost.
2. Check the **path**. A rule that is not on it was never a candidate — usually
   an anchor at the wrong node.
3. Check **order and depth** on the winner. If a shallower rule won, the deeper
   one was dropped earlier: look at its status, its effective dates, or its
   filters.
4. Check the **filters** on the rule you expected. The reason text says which
   one failed and what the subject's value was.
5. If it is a tie, the two rules are equally specific at one anchor. That is a
   publishing mistake, not an evaluation one.
