---
name: karpathy-diff
description: >-
  Review an in-progress code change (a git diff) against Andrej Karpathy's
  principles before you commit or accept it. Catches scope creep, collateral
  file edits, drive-by refactors, reformatting, deletions of code or comments
  the task never called for, over-engineered new code, and missing
  verification. Use this whenever the user wants to review a diff or their
  changes, check whether a change is safe to commit or accept, asks "did the
  agent touch anything it shouldn't", "review my changes", "check this before
  I commit", says "karpathy diff" or "/karpathy:diff", or has just had an
  agent make a batch of edits. Trigger before commits and after agent-made
  edits even if the user doesn't mention Karpathy.
---

# Karpathy Diff Review

Review a code change *before it is committed or accepted*, against Andrej
Karpathy's principles for LLM coding. The governing question is simple:

> **Does every changed line trace to the task that was asked?**

Coding agents reliably do more than they were asked — they refactor adjacent
code, reformat, rename, delete comments and code they don't fully understand,
and over-build the part they were asked for. None of that is visible if you
accept diffs without reading them, which is how most changes get accepted. This
review is the safety net under that.

Default to **report first, fix on approval**. Report what doesn't trace to the
task; never revert or rewrite the user's change without their say-so.

## 1. Get the change

Unless the user points somewhere specific, review **all uncommitted work**:

```
git diff HEAD
```

Honor an explicit target if given: staged only (`git diff --staged`), a commit
range (`git diff main...HEAD`), a single commit, or a path. Also run
`git status` to catch new/untracked files and `git diff --stat` for the shape
of the change. If there is no diff, say so and stop.

## 2. Establish the task

The trace test needs to know what the change was *meant* to do.

- If this review happens in a session where the change was just made, the task
  is in the conversation — use it.
- If the user stated the intent (in the command argument or the request), use that.
- If intent is genuinely unavailable, **ask one question**: "What was this
  change meant to do?" Don't guess the task — guessing it defeats the review.

State the task you're reviewing against at the top of the report so the user
can correct it.

## 3. Calibrate to the project

Read the repo's `CLAUDE.md` / `AGENTS.md` if present. Projects set their own
rules, and the review must respect them — a project whose defaults explicitly
sanction deleting legacy code or resetting local data has *not* committed a
violation when the agent does so. Calibrate to the project's stated defaults;
flag against the task, not against a generic ideal.

## The trace test

Walk the diff hunk by hunk. Sort every hunk into one of three buckets:

- **Traces** — directly implements the task. Leave it alone; this is the work.
- **Doesn't trace** — has no path back to the task. This is a finding.
- **Can't tell** — name it and ask, rather than guessing.

A clean change is one where every hunk traces. The categories below are the
specific ways hunks fail to trace.

## What to flag

**Surgical Changes — the core.**

- **Collateral files** — files changed that the task did not require.
- **Drive-by refactors** — renaming or restructuring working code next to the
  task but not part of it.
- **Reformatting / style drift** — whitespace, quote style, reordered imports.
  It bloats the diff and hides the real change. Match existing style.
- **Unexplained deletions** — comments or code removed that the task never
  called for. This is Karpathy's specific complaint: agents "change/remove
  comments and code they don't sufficiently understand as side effects." Treat
  every deletion as guilty until it traces to the task.
- **Pre-existing dead code removed** — if the change deletes code that was
  already dead before, that is out of scope unless the task asked for cleanup.

**Simplicity First — in the new code itself.**

- Speculative abstraction, unrequested configurability, error handling for
  cases that cannot occur, far more lines than the task needs. If the new code
  is 200 lines and the task needed 50, say so.

**Goal-Driven Execution — verification.**

- No test or check accompanies the change. For a bug fix, the strongest signal
  is a test that reproduces the bug; if the diff fixes a bug but adds no such
  test, flag it.

**Orphans the change created.**

- Imports, variables, or functions that *this change* made unused but left
  behind. (Removing those is in scope — it's cleaning up your own mess.)

## Scope: what is NOT a finding

- **The intended change.** The work the task asked for is supposed to be in the
  diff. Never flag it — the "What's clean" section exists to say so explicitly.
- **Project-sanctioned behavior** (see step 3) — deletion or rewrites the
  repo's own rules permit.
- **Orphan cleanup** caused by this change — that is correct surgical behavior,
  not scope creep.

## Report format

Use this structure:

```
# Karpathy Diff Review

**Task:** <the intent being reviewed against>
**Change:** <N files, +X / -Y lines>
**Traceability:** <X of Y hunks trace to the task>
**Findings:** <N> (<N> critical, <N> warning, <N> nit)

## Findings
### [CRITICAL] <title>
- **Location:** <file:line or hunk>
- **Issue:** <what does not trace, and what it does>
- **Why it matters:** <concrete risk — what could break or rot>
- **Fix:** <specific: revert / restore / split out / remove>

(repeat per finding; order Critical -> Warning -> Nit; consolidate repeated
instances of the same problem into one finding rather than itemizing each)

## What's clean
<name the hunks or files that correctly trace to the task — don't skip this; a
review that only lists faults gives the user no way to trust the verdict>

## Proposed fixes
<a concrete action per finding: which lines to revert, which comment to
restore, which unrelated change to pull into its own commit>
```

Severity:

- **Critical** — changes that can alter behavior outside the task's scope:
  logic edits to untouched areas, deleted code or comments with no traceable
  reason, collateral edits to unrelated files.
- **Warning** — reformatting, drive-by refactors, over-engineered new code,
  missing verification.
- **Nit** — minor: a stray blank line, a trivially reordered import.

After the report, ask: "Want me to apply the proposed fixes?" Then stop.

## Applying fixes

When approved:

- Make only the fixes the findings name. Revert the reformatting, restore the
  deleted comment, remove the orphaned import — and leave every hunk that
  traces to the task untouched.
- If a flagged change is legitimate work that simply belongs on its own (a real
  refactor the agent did as a drive-by), don't discard it — tell the user to
  commit it separately, or move it aside, rather than deleting the work.
- Re-run `git diff` after applying so the user sees the tightened change.
