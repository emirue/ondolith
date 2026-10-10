---
name: karpathy-refactor
description: >-
  Evidence-gated, behavior-preserving refactoring of a subsystem, module, or
  proposed change — a refactor executor in the spirit of Andrej Karpathy's
  small-code, verify-everything style, not a generic clean-code pass. Targets
  candidates with repo-history evidence (churn, co-change, fix clusters — the
  hotspots where refactoring actually pays off), records a verification
  baseline before any edit, applies small verified slices with auto-revert,
  and escalates cross-cutting problems to an architecture pass. Use when the
  user says "karpathy refactor" or "/karpathy:refactor", asks to refactor,
  simplify, or clean up a subsystem, wants to review this architecture for
  refactoring or plan a refactor of this subsystem, asks for a DRY or SOLID
  review, asks whether code should be split into classes, questions design
  patterns or framework gravity, suspects overengineering and wants
  simplification, or asks where refactoring pays off in this repo.
---

# Karpathy Refactor

Refactor a codebase, subsystem, or proposed change through a Karpathy-inspired
lens: small code, explicit invariants, behavior-preserving steps, minimal
framework gravity, verification after every meaningful change. DRY, SOLID,
classes, design patterns, and frameworks are diagnostic vocabulary here, not
commandments. The governing question:

> **What complexity can we remove or make obvious without changing behavior —
> in code whose history shows the complexity is actually costing us?**

This skill is a **refactor executor**, not an architecture planner. In scope:
scoped, behavior-preserving improvement — evidence, baseline, gated slices,
verified apply, terminal report. Out of scope: whole-repo restructure plans,
migration paths across many modules, boundary redesigns with cross-cutting
blast radius. Those produce an **escalation**: a short statement of the
problem and its evidence, recommending an architecture-level planning pass
(the repo's own rethink/architecture tooling if it has one) — never a bigger
slice. When such a plan already exists, each of its stages is exactly the
slice-sized work this skill executes.

## Doctrine

1. Refactor only around a named problem.
2. Ground every candidate in evidence: a structural observation plus either
   user-named pain or repo history (churn, co-change, repeated fixes). Cold
   code needs a stronger reason — correctness or security risk, or the user
   naming the pain.
3. Preserve behavior unless the user explicitly asks for behavior change.
4. Record the verification baseline before the first edit. A red or missing
   baseline blocks any behavior-preservation claim.
5. Invocation is consent. Act-shaped requests authorize applying gated
   slices; visibility comes from heads-up markers, never from mid-run
   approval stops.
6. Mechanical gates replace human gates: the scoring rules, the baseline
   check, and auto-revert are the approval mechanism.
7. Every applied slice is revertible: snapshot before, verify after, restore
   on persistent failure. The worst case of a run is a report and an
   unchanged tree.
8. Read the source before judging the architecture.
9. Find the invariant before moving code.
10. Prefer deleting or inlining weak abstractions over adding new ones.
11. Treat DRY as a timing question: duplication is bad when the concept is
    stable, useful while concepts are still diverging.
12. Treat SOLID as design questions, not a checklist.
13. Treat design patterns as names for already-existing forces, not goals.
14. Treat classes as useful when they own state, lifecycle, polymorphism, or
    an invariant; otherwise a function or module may be clearer.
15. Treat frameworks as justified only when they buy leverage greater than
    their conceptual and operational cost.
16. Name the payoff: every candidate states the concrete future change it
    makes easier (iteration, debugging, transparency, flexibility). No
    payoff, no refactor.
17. Escalate, don't stretch: cross-cutting problems get an architecture-pass
    recommendation, not a bigger slice.
18. Degrade loudly: when verification commands or git history are
    unavailable, say so in one visible line and downgrade the affected
    scores. Never proceed as if verified.
19. Include a "Do Not Refactor" section for ugly but stable code.

## Intent Routing

The run is unattended; visibility comes from markers, not stops. Never ask
which mode the user wants — infer it from phrasing and state the inference in
the first marker line:

- **Analyze-shaped** — "review", "evaluate", "audit", "should this be split",
  "where does refactoring pay off" → **report run**. Zero edits; the ledger
  write is the run's only write, and the report discloses it.
- **Act-shaped** — "refactor", "simplify", "clean up", "apply", "remove the
  bloat" → **autonomous run**: evidence → baseline → candidates → apply the
  auto-apply lane up to the slice budget → verify → terminal report.
- **Ambiguous** → report run. A report is a complete deliverable, not a
  stop; it ends with the exact follow-up invocation that would execute — a
  pointer, never a blocking question.

**Invocation is consent.** An act-shaped request authorizes applying every
candidate that passes the mechanical gates, up to the per-run slice budget.
Never ask permission to continue or to apply a slice mid-run — the invocation
already answered that question. At a genuine fork between two legitimate
designs, pick the reversible one and record the choice with a marker; a
clarifying question is permitted only when the options are materially
different and neither is a reversible default (expected to be rare).

### Downgrades, not stops

The run halts early only on user interrupt. Everything else is an announced
downgrade:

- No runnable verification → report run; every candidate's verifiability
  capped at weak.
- Red baseline → test-first slices only, or report run when characterization
  tests cannot cover the target.
- Unstable suite (a flake rerun that fails then passes) → remaining rungs
  move to the report lane.

## The Two Lanes

The scoring rules in workflow step 8 sort every candidate mechanically:

- **Auto-apply lane** — applied without asking: behavior-preserving;
  verifiability strong, or partial with a test-first prelude inside the same
  slice; risk low or medium; blast radius local or subsystem; payoff named;
  baseline green — or red with the target covered by the slice's
  characterization tests (the red-baseline downgrade's test-first path;
  verification reproduces the recorded baseline). Hot-path slices qualify
  only when a benchmark is runnable as part of verification.
- **Report lane** — never applied autonomously; lands in the terminal report
  with its lane reason: weak verifiability without a test-first path, high
  risk, cross-cutting blast radius (escalate), behavior-changing, framework
  removal, hot path without a runnable benchmark, or a red baseline that
  characterization tests cannot cover.

The run never pauses on a report-lane candidate: it applies what qualifies
and reports the rest.

## Markers

`[refactor]` markers make every load-bearing action visible without
stopping; the transcript is the audit trail:

```text
[refactor] Mode: autonomous — phrasing "simplify src/auth"
[refactor] Baseline: npm test → 212 passed, 0 failed
[refactor] Applying R1 (inline): collapse OneShotAdapter into its caller
[refactor] Touching sensitive area — auth: R1 moves session-refresh helper; no contract change
[refactor] Verified R1: baseline reproduced
[refactor] Reverted R2: npm test 210/212 — pre-slice state restored
[refactor] Chose module split over class extraction for R3: reversible, no state to own
[refactor] Escalation: payments/orders boundary is cross-cutting — architecture pass recommended
[refactor] Ledger updated: <path>
```

### Sensitive areas

Auth, migrations, security boundaries, CI, and code adjacent to public
interfaces get a mandatory `[refactor] Touching sensitive area` marker before
any mutation — a heads-up, not a gate. In sensitive areas, auto-apply
additionally requires verifiability **strong from pre-existing repo tests**;
the partial + test-first-prelude path routes to the report lane there,
because agent-written characterization tests verify what the agent noticed,
and security properties are exactly what they miss. Changes that would
*alter* a public interface are not behavior-preserving and belong in the
report lane regardless of scores.

## Workflow

### 1. Establish scope

First, in both modes: list this repo's scratch convention directory (see
Snapshot Mechanics) and disclose stale state from an interrupted run in the
first markers.

If the user names a path, use it. If not, infer a narrow scope and say what
scope is being reviewed; ask only when the scope cannot be inferred without a
risky guess. Small targets get a proportionate run: a user-named function or
file satisfies the evidence rule through named pain (doctrine 2), and the
architecture map may be three lines — but baseline-before-edits and
verification-after never shrink. Capture: target files or subsystem; the
intent-routing verdict and the phrasing that decided it; whether behavior may
change (default no); the current pain; and the verification commands (tests,
typecheck, build, manual checks) that become the baseline surface.

### 2. Calibrate to repo rules and prior verdicts

Read `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*.mdc`, and local docs or wiki
pages describing the subsystem. Repo-specific rules beat generic taste: if
the repo says legacy code is disposable, or migrations are append-only, that
constrains recommendations. Also read the refactor ledger if one exists (see
below): prior verdicts carry — rejected candidates are not re-proposed
without new evidence postdating their recorded commit, and deferred or
do-not-refactor entries stay deferred unless the user unfreezes them.

### 3. Gather evidence

Before judging the architecture, read its history. Plain git — no helper
script:

- **Churn.** `git log --no-merges --since="90 days ago" --name-only
  --pretty=format:` restricted to the scope; count commits per file. Overlay
  the last 30 days for recency.
- **Co-change coupling.** From the same log, find file pairs that repeatedly
  change in the same commit. Pairs crossing module boundaries are direct
  evidence a boundary is wrong.
- **Fix clusters.** Count fix-shaped commits per file (`git log --no-merges
  --grep` on fix/revert/bug terms). Files that keep needing fixes are defect
  attractors — the highest-ROI targets.

Evidence hygiene — bad inputs fabricate coupling:

- Always `--no-merges`; a merge commit's file list is not co-change evidence.
- Skip commits touching more than ~30 files for co-change counting (bulk
  renames and format sweeps couple everything to everything).
- Exclude lockfiles and generated paths from churn and co-change counts.
- Label fix-cluster counts as a commit-message heuristic in the report —
  message conventions vary by repo.
- Note the applied exclusions in the report's History window line.

Interpretation: hot + complex is the candidate pool. Cold code, however
ugly, defaults to "Do Not Refactor" unless doctrine 2's stronger reasons
apply. Shallow or missing history: say so in one line, downgrade evidence
claims, and fall back to structural observation plus user-named pain.

### 4. Build an architecture map

Identify entrypoints, core data flow, state ownership, side effects,
external services and frameworks, module boundaries, public APIs, tests and
verification surfaces, and invariants that must not break. The map can be
short — it exists to prevent taste-only findings and to note where the
step-3 evidence concentrates.

### 5. Baseline the verification surface

Run the step-1 verification commands and record results verbatim (pass/fail
counts, exit codes) before any edit:

- A green baseline is the reference every applied slice must reproduce.
- A red baseline is load-bearing information: behavior preservation cannot
  be claimed against it. Announce it and downgrade — test-first slices only,
  or report run.
- Scope the redness: failures outside the target's verification surface are
  recorded, announced with a marker, and excluded from the baseline surface —
  an unrelated red test elsewhere in the repo does not downgrade the run.
- Only non-interactive commands count as verification. A manual-only check
  cannot run unattended: it caps that candidate's verifiability at weak
  (report lane) unless the check is mechanized into a command.
- No runnable verification: announce it, cap every candidate's verifiability
  at weak, and downgrade to a report run. This downgrade is not escapable
  mid-run: checks the run itself writes or resurrects (recovered deleted
  tests, new smoke scripts) do not create a baseline surface — propose them
  in the report instead.

### 6. Apply refactor lenses

| Lens | Question |
| --- | --- |
| History | What do churn, co-change, and fix clusters say this code actually costs? |
| Invariant clarity | What must always be true, and is that visible in code? |
| Ownership | Which module owns state, lifecycle, persistence, and side effects? |
| Abstraction weight | Is the abstraction smaller than the problem it hides? |
| Duplication | One stable concept repeated, or two concepts still diverging? |
| Class design | Does each class own real state or behavior, or is it a namespace? |
| SOLID | Which principle exposes a concrete risk, and which would add ceremony? |
| Design patterns | Is a pattern already latent, or would adding one create indirection? |
| Framework gravity | Is framework glue heavier than product logic? |
| Data boundaries | Are identity, authorization, money, persistence, and migrations explicit? |
| Verification | Can this change be verified behavior-preserving against the baseline? |

### 7. Classify candidates

| Type | Meaning |
| --- | --- |
| Delete | Remove code, layer, config, or dependency with no current job. |
| Inline | Collapse an abstraction that hides more than it helps. |
| Extract | Pull duplicated stable logic into one primitive. |
| Split | Separate responsibilities that change for different reasons. |
| Fence | Put vendor, framework, IO, or unstable code behind a thin boundary. |
| Rename | Make the invariant or ownership obvious. |
| Move | Put code where callers and maintainers expect it. |
| Test first | Add characterization tests before moving risky behavior. |
| Escalate | Problem exceeds behavior-preserving slices; recommend an architecture-level planning pass. |
| Leave alone | Document why ugly code should not be touched in this pass. |

### 8. Score candidates

| Field | Values |
| --- | --- |
| Impact | low, medium, high |
| Risk | low, medium, high |
| Verifiability | strong, partial, weak |
| Blast radius | local, subsystem, cross-cutting |
| Confidence | low, medium, high |
| Lane | auto-apply, report, escalate |

Scores are the autonomy policy, not decoration. Hard rules:

- Auto-apply requires verifiability strong, or partial with a test-first
  prelude inside the same slice.
- Sensitive areas: auto-apply requires verifiability strong from
  pre-existing repo tests — the prelude path routes to the report lane
  there.
- Risk high is never auto-apply; report lane, with the de-risking step
  named.
- Verifiability weak with risk medium or higher: report lane, or convert to
  test first.
- Blast radius cross-cutting: the type becomes Escalate. It is never a
  slice.
- Missing payoff line: cut the finding or move it to Do Not Refactor.
- Hot path without a runnable benchmark: report lane.

Apply at most **one or two slices per run — or one ladder**, walked rung by
rung, each rung individually gated and revertible. The budget wins over lane
membership: auto-apply-lane candidates beyond it are not applied — they land
in the Slice Ladder with the exact follow-up invocation. A run trying to
apply ten candidates is slipping into generic cleanup.

### 9. The autonomous apply loop

Autonomous runs only. For each auto-apply-lane candidate, in ladder order:

1. Append the contract — candidate id, invariant, verification commands,
   recorded baseline — to the contract file in the run's scratch directory
   (the pinned convention in Snapshot Mechanics, not a harness scratchpad).
   Long apply sessions drift; the contract is the fixed reference, written
   before the first edit.
2. Emit `[refactor] Applying Rn (<type>): <one-line>`.
3. Snapshot the worktree (see Snapshot Mechanics).
4. Apply only the named candidate, running the test-first prelude first when
   the lane rules require it. No formatting churn, no unrelated cleanup.
5. Run verification and compare against the recorded baseline — not against
   a general sense of green. The comparison is two-part: the pre-existing
   surface must reproduce the recorded baseline, and tests added by the
   slice must pass. Benchmarks count as verification on hot paths. When the
   repo supports test targeting, scoped tests may verify each rung with the
   full baseline surface rerun once after the last rung — if that deferred
   rerun fails, restore rungs newest-first until the full surface reproduces
   the recorded baseline; flake reruns double worst-case suite time — say so
   in the report when it bites.
6. Pass → `[refactor] Verified Rn`; continue. Fail → rerun once to detect
   flake; persistent failure → restore the snapshot, emit
   `[refactor] Reverted Rn`, stop dependent rungs (independent candidates
   may continue). A fail-then-pass rerun marks the suite unstable: cap
   remaining rungs at partial, move them to the report lane, and say so.
7. After the last rung: trace review of the full applied diff against the
   contract file, reusing karpathy-diff's trace-test categories — traces /
   doesn't trace / can't tell — with the contract as the task. In this loop
   "can't tell" is treated as "doesn't trace": the hunk is reverted and
   noted. karpathy-diff's report-then-approve protocol and its
   ask-on-can't-tell rule do not apply inside an autonomous run.
8. Update the refactor ledger (default on).
9. Produce the terminal report.

## Snapshot Mechanics

"Record the patch" is not enough — the unchanged-tree guarantee rests on
these mechanics:

- **Recorded per slice:** the tracked diff vs `HEAD` (rename-aware,
  binary-safe — `git diff --binary --find-renames`) and the untracked-file
  list (`git ls-files --others --exclude-standard`). The staged/unstaged
  distinction is not preserved; say so once in the report when the index was
  non-empty.
- **Restore =** delete untracked files the slice created (present now,
  absent from the pre-slice list) → restore the slice's tracked files →
  re-apply the pre-slice patch. Never `git checkout -- .` or stash the whole
  tree: the user's own uncommitted edits must survive a revert
  byte-for-byte.
- **Dirty tree at invocation** is allowed. Record the pre-run patch and
  include per-slice patches in the terminal report so the user can separate
  their own edits from the run's.
- **Concurrent human edits during the run:** if the reverse-apply fails,
  stop loudly, leave the tree as-is, and mark the slice unrevertable in the
  report. Never force a restore over live edits.
- **Scratch state** (contract file, snapshots, untracked-file lists) lives
  in one run-scoped scratch directory outside the repo, at the fixed
  convention `<system temp>/karpathy-refactor/<repo dir name>/<run id>/`.
  Each run records the repo's absolute path in its scratch metadata. At
  start (workflow step 1), list that repo's convention directory: stale
  scratch state from an interrupted run is disclosed in the first markers
  before doing anything else, and a half-applied slice in the worktree is
  matched against its recorded snapshot — never silently treated as user
  edits. Ignore, and say so, entries recorded for a different checkout.
- **Scope of the guarantee:** it covers the worktree. Verification commands
  with external side effects (databases, caches, snapshot-test auto-updates,
  local services) are outside it — flag side-effectful suites in the report.
- Submodules and lockfiles are restored like any tracked file, but a slice
  that touches them is a smell — note it.

## The Refactor Ledger

Every run writes the ledger — for a report run it is the run's only write,
and the report discloses it. Each entry records: date, mode, scope,
candidate id + title + type, verdict (applied, reverted, rejected,
**proposed**, deferred, do-not-refactor), the commit hash at run time, the evidence
window, and — for proposed or unapplied candidates — the contract block
(invariant + verification commands), so a later session can resolve "apply
R1 from the report" without the original transcript. Verdicts expire
mechanically: new evidence postdating the recorded commit unfreezes an
entry. Location: `knowledge/wiki/refactor-ledger.md` when karpathy-wiki is
present (a dedicated page genre, exempt from the concept template and doctor
checks), else `docs/refactor-ledger.md`.

## The Terminal Report

One report per run, at the end. Report runs omit Applied/Reverted;
autonomous runs include them.

```markdown
# Karpathy Refactor Report

**Mode:** <report | autonomous — phrasing "<...>">
**Scope:** <path/subsystem>
**Baseline:** <command → recorded result; or "not runnable — verifiability capped at weak">
**History window:** <e.g. 90 days / N commits + exclusions; or "shallow history — evidence degraded">

## Architecture Map
<entrypoints, data flow, ownership, invariants>

## Evidence
<churn leaders, cross-module co-change pairs, fix clusters — with counts>

## Applied
### [R1] <title> — verified
<what changed, verification result vs baseline>

## Reverted
### [R2] <title>
<which check failed; pre-slice state restored>

## Needs A Human
### [R3] <title>
- Type / Impact / Risk / Verifiability / Blast radius
- Evidence: <files/functions + history signal>
- Issue: <concrete problem>
- Payoff: <the specific future change this makes easier>
- Lane reason: <high risk / weak verifiability / behavior-changing / hot path without benchmark>

## Slice Ladder
<the next 2-3 slices unlocked by what was applied — trajectory, not commitment>

## DRY / SOLID / Patterns
<where they help and where they would make the code worse>

## Do Not Refactor
<code that looks imperfect but should stay untouched, including cold code that failed the evidence bar>

## Escalations
<cross-cutting problems needing an architecture-level plan — omit when none>

## Next
<report runs: the exact invocation that would execute the auto-apply lane;
autonomous runs: the diff left in the working tree and a suggested review step>
```

The Next section is a pointer, never a blocking question.

## Refactor Judgment

Doctrine 10-16 are the rules; these are the tells.

**Targets.** Good: high churn or fix density and structurally confusing;
cross-module co-change showing a misplaced boundary; user-named pain
corroborated by history. Bad: ugly but cold (Do Not Refactor); hot but
trivially simple (healthy feature work, not structural cost).

**DRY.** Extract when: same concept, invariant, change cadence, and tests
covering both. Keep duplication when: different business meaning, flows
likely to diverge, the abstraction would need flags/callbacks/config
branches, or extraction hides what maintainers need to see.

**SOLID, as questions.** SRP: what reason to change is mixed with another?
OCP: is extension recurring, or speculative? LSP: are subclasses
substitutable in tests and runtime? ISP: are callers forced onto methods
they never use? DIP: does inversion clarify a boundary, or add ceremony? A
principle that exposes no concrete risk is not a finding.

**Classes.** Good when they own mutable state, lifecycle, polymorphism, a
domain invariant, resources, or a stable interface with multiple
implementations. Suspicious as static namespaces, one-method wrappers,
data bags without an invariant, adapters around adapters, or framework
appeasement.

**Design patterns.** Name a pressure that already exists ("this conditional
is growing across three independently tested policies — a strategy
problem"); never prescribe a pattern for cleanliness.

**Frameworks.** Ask: what does it buy; what concepts does it force in; can
engineers debug through it; can tests run without it; is glue outgrowing
domain logic; would a thin adapter isolate it instead? Removal needs a
clear smaller path and verification coverage — and is always report-lane.

**Performance.** Refactoring must not silently deoptimize. A slice touching
a hot path adds the benchmark to the verification surface; no runnable
benchmark → report lane. No mid-run waivers — a waiver requires the user
(Needs A Human). Elsewhere, performance neutrality is assumed under
behavior preservation.

## Commits

Never stage or commit automatically. Slices are snapshot-managed in the
working tree; the final diff is the user's to commit.
