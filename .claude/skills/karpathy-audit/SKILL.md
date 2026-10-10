---
name: karpathy-audit
description: >-
  Audit a CLAUDE.md, AGENTS.md, .cursor/rules file, or other AI-agent
  instruction/memory file against Andrej Karpathy's four LLM-coding principles.
  Checks two things: whether the file tells the agent to follow the principles
  (coverage), and whether the file itself earns its context budget - lean,
  unambiguous, current, contradiction-free, well-layered, and sized for every
  model that reads it (quality). Use this whenever the user wants to review,
  audit, critique, tighten, clean up, or improve a CLAUDE.md / AGENTS.md /
  .cursor/rules / agent memory file, says "karpathy audit" or "/karpathy:audit",
  asks to configure or set up Karpathy audit checks, asks whether their agent
  instructions are any good, or wants to apply Karpathy or vibe-coding
  guidelines to a project. Trigger even if they just say "check my CLAUDE.md"
  without naming Karpathy.
---

# Karpathy Audit

Audit an AI-agent instruction file (`CLAUDE.md`, `AGENTS.md`, `.cursor/rules/*.mdc`,
or a global memory file) against Andrej Karpathy's four principles for LLM coding.

The core instruction audit has two independent halves:

- **Coverage** — does the file actually instruct the agent to follow the four
  principles?
- **Quality** - judged by the same four principles plus the
  context-engineering axes (progressive disclosure, layering, memory routing,
  reader tier), is the file itself a good artifact?

The second half is the interesting one. An instruction file is itself something
an LLM reads on every turn. The same failure modes Karpathy describes in
generated code — bloat, vagueness, stale cruft — show up in the file just as
easily. Audit it the way you would audit code.

The audit also includes repo documentation checks by default: stale-doc
candidates and index quality for folders with many docs. Stale roadmaps, specs,
TODOs, and missing indexes are hidden repo state that misleads agents. Users can
opt out through `/karpathy setup` or `/karpathy configure`.

Default to **report first, apply on approval**. Never rewrite the file before
the user has seen the findings and a concrete diff.

Never stage or commit `CLAUDE.md`, `AGENTS.md`, `.cursor/rules`, or other
agent instruction / memory files. You may propose changes, and you may edit
them after explicit approval, but the user decides whether those changes belong
in git.
If the instruction file is ignored by Git, treat it as checkout-local: audit it
when the user explicitly asks, but do not prompt the user to add, stage, or
commit it.

## Scope: what is NOT a defect

Before flagging anything, be clear about what an instruction file is *for*. A
good instruction file legitimately contains:

- Repo architecture, directory maps, key-file tables
- Build / test / lint / deploy commands
- Domain vocabulary and project-specific conventions
- Decisive project defaults (e.g. "treat local data as disposable")

None of that is bloat. Do not tell the user to delete useful context. The
boundary is compactness: a short directory map or small key-file table is
legitimate orientation, while an exhaustive catalog - or anything the agent
could derive by inspecting the code or filesystem itself - is the
progressive-disclosure defect axis CE6 flags. This skill audits *behavioral
guidance and file hygiene* - not whether a given piece of project context
deserves to exist. When in doubt, leave it and say so.

## The four principles

1. **Think Before Coding** — State assumptions; ask when the request is
   ambiguous; present tradeoffs; never silently pick one interpretation.
2. **Simplicity First** — Write the minimum that solves the actual problem. No
   speculative abstractions, no unrequested features or configurability.
3. **Surgical Changes** — Touch only what the task requires. No drive-by
   refactors, reformatting, or edits to code you weren't asked to change.
4. **Goal-Driven Execution** — Define verifiable success criteria up front
   (usually a test or a command), then loop until they pass.

## Workflow

1. **Locate and read the target file.** If the user named one, use it. Otherwise
   look for `./CLAUDE.md`, then `AGENTS.md`, then `.cursor/rules/*.mdc`. If none
   exists, say so and offer to draft one — don't audit a file that isn't there.
2. **Discover the instruction layers.** Before the quality audit, check only
   this fixed list and read every accessible source:
   - System and developer instructions visible in the current session. Do not
     try to reconstruct hidden prompts.
   - The `SKILL.md` files activated for the current audit. Do not scan installed
     skills.
   - Project `AGENTS.md` and `CLAUDE.md` files at the repo root and along the
     direct directory path from the repo root to the target. Do not search
     sibling directories or recurse through the repository.
   - Global memory at the harness's known location: Claude
     `~/.claude/CLAUDE.md`; Codex `~/.codex/AGENTS.override.md` followed by
     `~/.codex/AGENTS.md`; or one other location explicitly documented by the
     active harness.
   - Auto-memory exposed by the active harness, including Claude
     `~/.claude/projects/<project>/memory/MEMORY.md` when that project path is
     known. Do not enumerate project-memory directories to guess a match.

   Mark a layer **Unreadable** when it exists conceptually but its content or
   location is hidden, unknown, absent, or denied. Record the reason instead of
   silently omitting the layer. This checklist is the complete discovery scope:
   do not run a recursive filesystem sweep.
3. **Run the coverage audit** (below).
4. **Run the quality audit** (below), comparing the target with every
   accessible instruction layer for CE8 and CE11.
5. **Run docs checks** unless `.karpathy.json` disables them.
6. **Write the report** in the format below. Then stop.
7. **Apply on approval.** Only after the user approves, make the edits -
   surgically. Preserve their headings, voice, and ordering; change only the
   lines the findings name; never reformat the whole file. (Yes — the skill
   itself follows principle 3.) Do not stage or commit the instruction file.

## Coverage audit

For each of the four principles, classify it as **Present**, **Partial**, or
**Missing**, and cite the evidence (a quoted line, or its absence).

Judge by *substance, not wording*. A file that says "ask me before making
assumptions you can't verify" covers principle 1 even though it never says
"Think Before Coding". Don't demand the canonical phrasing. Use **Partial**
when the file reaches a principle only as a side effect — e.g. an anti-legacy
project default that happens to bias toward simplicity, without ever telling
the agent to write the minimum.

**Counting rule:** the "N/4" in the report header counts **Present only**.
Partial does not count toward N; show it in the table but not the score.

Coverage gaps belong in the Coverage section **only**. Never also list a
missing principle as a quality finding — that double-counts. The quality audit
judges the file as an artifact; it does not re-score the four principles.

Important nuance: behavioral principles often belong in the **global**
`~/.claude/CLAUDE.md`, not in every project file — that way they apply
everywhere without being repeated. If you're auditing a project-level file and
coverage is Missing, do not assume it's a defect. Flag it, but ask whether the
principles live in the user's global memory file, and recommend global as the
home for them unless the user wants project-specific overrides. Before
recommending that hoist, verify which harnesses actually load the global file
(axis CE11) - consolidating into a file only some harnesses read silently
strips the principles from the others.

## Quality audit

Turn the principles back on the file itself, plus the context-engineering
axes that reflect how models actually consume instruction files. One rubric:
**CE1–CE4** are the Karpathy principles turned inward, **CE5–CE10** are
the newer context axes, and **CE11** (reader tier) gates every proposed cut.

**How to weigh a finding.** An instruction file's cost is not its byte size.
It rides the prompt cache, so its real cost is token size multiplied by how
many cached requests replay it. Rank findings by that product: a 1 KB file
replayed on every turn outweighs a 12 KB file read once per session. State
the replay tier when you rank (every-turn base > per-session digest >
per-task read > rarely loaded).

**CE1 - Lean** (Simplicity turned inward). Flag content that costs tokens
every turn and earns nothing:

- Generic advice that just restates competent default behavior ("write clean
  code", "use meaningful names", "follow best practices"). A capable model does
  this already; the line is noise and dilutes the instructions that matter.
- The same instruction stated in two places.
- A changelog or roadmap living inside the file — long lists of shipped items,
  struck-through "done" entries, PR numbers. That belongs in `docs/`, not in a
  file loaded on every turn.

**CE2 - Unambiguous** (Think Before Coding turned inward). Flag instructions
the agent cannot act on without guessing, or check itself against: "handle
errors appropriately", "make it performant", "keep it clean". Rewrite each
into something concrete and checkable. One narrow, informational check lives
here: does the file *name the commands* that prove a change is good - test,
typecheck, lint, build? That is distinct from coverage of principle 4 (the
behavioral instruction to define success criteria and loop). A file can list
every command yet never tell the agent to use them as a loop - only the
command gap is reported here; the behavioral gap is a Coverage row. If the
commands are absent, flag it; if present, say so.

**CE3 - Current** (no staleness). Flag content that is correct today and
misleading in a month:

- Words like "currently", "for now", "this week", "in flight", "transient", "TODO".
- Dated markers: phase numbers, sprint names, specific PR/commit references.
- Struck-through items left in place after completion, and resolved or
  superseded entries retained in full - collapse a resolved entry to its
  one-line durable takeaway.

Recommend moving volatile state out of the file and keeping only durable facts.

**CE4 - Contradiction-free.** Flag pairs of instructions that can't both be
followed (e.g. "never delete data" alongside "treat local data as
disposable"). Contradictions are the worst defect - they guarantee the agent
guesses. Also flag **latent contradictions**: a project default that would
collide with the four principles once they are added (this skill recommends
adding them). Example: a default that licenses deleting legacy code anywhere
vs. principle 3's "no edits to code you weren't asked to change." Rate these
Critical - the skill's own workflow is about to activate the conflict - and
resolve them by scoping the project default to the task, not by dropping it.

**CE5 - Rules → judgment.** A frontier model handles many situations better
via judgment than via an enumerated rule. Flag over-constraining "never do X
/ always do Y" scaffolding that could become an outcome statement ("ship a
change only after a named check passes" rather than a ten-step procedure).
**Hard limit - keep the rule, do not soften:** safety, destructive or
irreversible operations, security, credential/privacy handling, and
merge/release authority. Rule density in a safety contract is correct, not
bloat; when a file is mostly such rules, report the density as justified
rather than hunting for trims. Check the reader tier (CE11) before proposing any
judgment-based trim.

**CE6 - Progressive disclosure.** Flag a monolithic file that should be a
light core plus an on-demand tree (skills, referenced docs loaded only when
relevant). The file should briefly orient, spend its tokens on real gotchas,
point to deeper material, and never state what code or filesystem inspection
would show. Exhaustive file/field/config catalogs inlined in an always-loaded
file are the canonical hit - move them to the doc that owns them, leaving an
orienting summary and a pointer.

**CE7 - Interfaces over examples.** Flag long inline tool-usage examples that
a better parameter or interface description would replace. This fires rarely
on prose-heavy instruction files; when the file's weight is specs and
catalogs rather than worked examples, say so and move on.

**CE8 - Cross-layer duplication.** Flag content repeated across the layers a
model sees: system prompt, skills, project `AGENTS.md`/`CLAUDE.md`, global
memory file, auto-memory. Name the single best owner. **Check the reader tier
(CE11) before proposing the merge** - apparent duplication is sometimes the only
copy a given reader gets.

**CE9 - Memory routing.** Flag content living in an instruction file that
belongs in a durable record or auto-memory instead: volatile state (versions,
quota levels, "currently exhausted"), one-off incident narration, and stable
reference data (identities, IDs, key locations). The instruction file keeps
the durable *rule*; the evidence and state move to records loaded on demand.

**CE10 - References over prose.** Flag a prose spec that should be a link or
`@`-reference to the authoritative code, test, doc, or artifact - especially
values that can drift from a source of truth (a re-typed config schema, a
duplicated palette, an enumerated requirement list a tool already enforces).
Prefer the pointer; it cannot rot out of sync.

**CE11 - Reader tier (required on every finding).** Before proposing any cut,
identify who reads this file:

- **Frontier-model only** (e.g. a private preference file read by one strong
  model). Judgment-heavy trims (CE5) and aggressive compression are safe here.
- **Also mid-tier agents.** These models demonstrably need explicit rules a
  frontier model can infer. Keep the explicit rule.
- **Also lower-tier / app agents.** Most reader-constrained; explicit,
  concrete, checkable rules are correct here. Trim least.

Verify inheritance, do not assume it. A "global" file only reaches the
harnesses that actually load it. Check before recommending a hoist-to-global:
consolidating a rule into a file only some harnesses read silently strips it
from the others. When in doubt, the safe home for a rule every harness
working a repo must obey is the per-repo file.

Tag each finding's proposed change:

- **safe-for-all-readers** - the cut does not rely on model judgment to
  recover the dropped guidance: a pointer to a named file, de-duplication of
  true boilerplate, or removal of stale/volatile state.
- **claude-only** - the cut is safe only where the reader is a frontier
  model; it would degrade a mid- or lower-tier reader (an enumerated rule
  turned into a judgment call, an explicit anti-fabrication rule compressed).
  Do not apply on a shared surface while mid-tier readers depend on it.

A cross-layer duplication (CE8) that is the only copy a mid- or lower-tier
reader sees is reclassified **KEEP** by this axis - a load-bearing
redundancy. Say so explicitly; it is a finding in its own right, not an
oversight.

Quick reference:

```
CE1  Lean            - costs tokens every turn, earns nothing?
CE2  Unambiguous     - can the agent act on and check it? verification commands named?
CE3  Current         - will it rot? resolved entry kept in full?
CE4  Contradiction   - can two rules both be followed? (worst defect)
CE5  Rules→judgment  - over-constrained? (KEEP safety/destructive/security/merge rules)
CE6  Disclosure      - monolith that should be core + on-demand tree? states what inspection shows?
CE7  Interfaces      - long example a parameter description would replace?
CE8  Duplication     - repeated across layers? name one owner (check reader tier first)
CE9  Memory routing  - volatile/reference content that belongs in a durable record?
CE10 References      - prose spec that should be a link/@-ref to a source of truth?
CE11 Reader tier     - who reads it? tag every cut safe-for-all vs claude-only; verify inheritance
```

## Docs audit

Docs checks are controlled by `.karpathy.json` at the repo root:

```json
{
  "audit": {
    "staleDocs": true,
    "indexChecks": true,
    "docPaths": ["README.md", "docs", "knowledge", "specs", "roadmap.md", "TODO.md"],
    "indexThreshold": 5
  }
}
```

If the config file is absent, use the opinionated defaults: D1 and D2 on, with
the default docs scope and index threshold shown above. If both checks are false
because the user opted out, audit the target instruction file and mention that
docs checks are disabled. If the config is invalid, report the config error and
do not invent docs findings.

When either docs check is enabled, run the helper internally:

```bash
python3 <skill-dir>/scripts/audit_tool.py docs-check --repo . --json
```

Do not ask the user to run this helper. Summarize its result in the audit
report. Keep docs findings separate from instruction-file quality findings;
they are repo documentation findings, not evidence that `CLAUDE.md` or
`AGENTS.md` is bad.

**Stale docs (`D1`)** flags stale-candidate evidence:

- TODO/FIXME/TBD markers and temporal language such as "currently", "for now",
  "current sprint", "in flight", or "this week".
- Dated roadmap/plan claims older than the helper's threshold.
- Broken local Markdown links in configured doc paths.

Treat temporal-language findings as warnings unless you verify they are
actively misleading. Broken local links can be critical because they make the
doc untrustworthy for agents immediately.

**Doc indexes (`D2`)** checks configured doc folders for navigation quality:

- A folder with at least `indexThreshold` direct doc files should have
  `README.md` or `index.md`.
- Existing indexes should mention their direct child docs by filename or stem.
- This is about high-signal navigation for agents. Do not require exhaustive
  indexes for tiny folders.

Recommended fixes should be concrete and small: remove or date-bound stale
claims, repair broken links, add `docs/index.md`, or add missing entries to an
existing index. Ask before semantic rewrites.

## Audit setup / configure

`/karpathy setup` and `/karpathy configure` are aliases for configuring the
audit docs checks. Use the same helper internally:

```bash
python3 <skill-dir>/scripts/audit_tool.py setup --repo .
python3 <skill-dir>/scripts/audit_tool.py setup --repo . --toggle D1
python3 <skill-dir>/scripts/audit_tool.py setup --repo . --enable stale-docs
python3 <skill-dir>/scripts/audit_tool.py setup --repo . --disable doc-indexes
python3 <skill-dir>/scripts/audit_tool.py setup --repo . --yes
python3 <skill-dir>/scripts/audit_tool.py setup --repo . --reset
```

Follow the Pipelane review setup shape:

- Bare setup/configure is read-only. It prints grouped rows and controls, but
  writes no files.
- Always show the always-on instruction rows (`A1`, `A2`) and the mutable docs
  rows (`D1`, `D2`).
- Let the user toggle by row ID (`D1`, `D2`) or check ID (`stale-docs`,
  `doc-indexes`).
- `--yes` saves the recommended docs checks: stale docs on and doc
  indexes on.
- `--reset` restores opinionated defaults: stale docs on and doc indexes on.
- Writes are limited to `.karpathy.json`. Never stage or commit it
  automatically.
- After a mutation, reprint the grouped setup state plus a short "Setup
  actions" list.

If a client rejects `/karpathy setup` or `/karpathy configure` as slash-command
syntax, tell the user to use `/karpathy:setup`, `/karpathy:configure`, or type
`karpathy setup` / `karpathy configure` as plain text.

## Lessons block maintenance (pipelane repos)

Pipelane seeds a managed `## Lessons` block in CLAUDE.md (and the capture
instruction in AGENTS.md) delimited by `<!-- pipelane:lessons:start -->` /
`<!-- pipelane:lessons:end -->`, with an append-only entries region inside
`<!-- pipelane:lessons:entries:start -->` / `:entries:end -->`. When the audited
file carries a `pipelane:lessons` marker (or the repo otherwise uses pipelane),
run these extra checks. Skip this section entirely for non-pipelane files.

**Coverage (is the block there?):** If the repo uses pipelane but CLAUDE.md has
no `pipelane:lessons:start` marker, or the marker exists but the capture
instruction prose is gone, flag it and route the fix to `/pipelane setup` — that
command owns and re-syncs the block. Do not hand-write the block yourself.

**Quality (operate INSIDE `lessons:entries` only — never edit the
pipelane-owned instruction prose):**

- **Dedupe** near-identical lessons into one; keep the earliest date.
- **Contradictions** — flag pairs that can't both hold ("always X" vs "never
  X"); keep both dates and surface them, don't silently pick a winner.
- **Stale references** — drop a lesson whose referenced file or symbol no longer
  exists in the repo (verify the absence first).
- **Anti-bloat** — if the entries region exceeds ~40 lines, recommend lifting
  the older or narrowly-conditional lessons into a dedicated skill or doc and
  leaving only the broadly-applicable ones inline.

Pruning entries is the one case where this skill edits a managed region: still
report-first, apply on approval, and preserve the markers plus every remaining
entry verbatim.

## Report format

**Consolidate.** Group repeated instances of one problem into a single finding
— flag "staleness throughout" once, not each PR number separately. A report
padded with twenty nits violates the skill's own values; aim for the few
findings that actually change how the file performs.

Use this structure exactly:

```
# Karpathy Audit — <filename>

**Coverage:** <N>/4 principles present
**Quality:** <N> findings (<N> critical, <N> warning, <N> nit)

## Coverage
| Principle | Status | Evidence |
|-----------|--------|----------|
| Think Before Coding   | Present/Partial/Missing | ... |
| Simplicity First      | ...                     | ... |
| Surgical Changes      | ...                     | ... |
| Goal-Driven Execution | ...                     | ... |

## Quality findings
### [CRITICAL] <title>
- **Axis:** <which axis fired: CE1–CE11>
- **Location:** <line / section>
- **Reader tier:** <who reads this file>
- **Cut safety:** <safe-for-all-readers / claude-only>
- **Impact:** <token size × replay tier, not raw bytes>
- **Issue:** <what is wrong>
- **Why it matters:** <concrete consequence for the agent>
- **Fix:** <specific change>

(repeat per finding; order Critical -> Warning -> Nit)

## Layer access
| Layer | Source checked | Status |
|-------|----------------|--------|
| System/developer | <visible session instructions> | Read / Unreadable: <reason> |
| Active skills | <activated SKILL.md paths> | Read / Unreadable: <reason> |
| Project | <direct-path AGENTS.md/CLAUDE.md locations> | Read / Unreadable: <reason> |
| Global memory | <known harness location> | Read / Unreadable: <reason> |
| Auto-memory | <harness-exposed or known project memory> | Read / Unreadable: <reason> |

## Cross-layer findings
<When at least two layers were readable: duplication across files or layers,
the proposed single owner, and the reader-tier verdict on each consolidation -
including whether hoisting would strip a reader. Otherwise state that no
cross-layer conclusion could be verified.>

## Optional docs checks
<Only include when configured or explicitly skipped. Show enabled checks,
config path, findings grouped Critical -> Warning, and small proposed fixes.>

## What's good
<1-3 things the file does well — be specific. Don't skip this; an audit
that only lists faults is not trustworthy and gives the user no baseline.>

## Proposed changes
<A concrete diff, or an exact before/after for each change you would make.>
```

Severity:

- **Critical** — actively causes bad agent behavior: contradictions (including
  latent ones), no verification commands, instructions vague enough to force
  guessing, or a claude-only cut proposed on a surface a mid/lower-tier
  reader depends on.
- **Warning** - degrades quality: bloat, staleness, redundancy with a safe
  owner, ambiguity, drift-prone re-typed values.
- **Nit** - minor: wording, ordering, formatting, trivial self-inconsistency.

After the report, ask: "Want me to apply the proposed changes?" Then stop.

## Applying changes

When approved:

- Make the smallest edits that resolve the findings. Do not restructure the
  file or restyle untouched sections.
- If injecting principle coverage, merge — extend a partial section rather than
  duplicating it. Never blindly append a second copy of something already there.
- Prefer recommending the global `~/.claude/CLAUDE.md` as the home for the four
  behavioral principles; only write them into a project file if the user asks.
  Before moving anything, verify the global file is actually loaded by every
  harness in play (axis CE11) - a hoist that strips a reader is a regression.
- When you move volatile content out, put it somewhere sensible (`docs/`)
  rather than deleting it, unless the user says delete.
- Leave `CLAUDE.md`, `AGENTS.md`, `.cursor/rules`, and similar agent files
  unstaged and uncommitted. Tell the user what changed and let them decide
  whether to include it in version control.

## Canonical principles block

When the user wants the four principles written into a file, use this block.
It is deliberately compact — principle 2 applies to it too.

```markdown
## Working principles

- **Think before coding.** State assumptions. When the request is ambiguous,
  ask or present the options — don't silently pick one. Surface tradeoffs and
  push back when a simpler path exists.
- **Simplicity first.** Write the minimum that solves the actual problem. No
  speculative abstractions, unrequested features, or configurability.
- **Surgical changes.** Touch only what the task requires. No drive-by
  refactors, reformatting, or edits to code you weren't asked to change.
- **Goal-driven execution.** Define how success will be verified — a test, a
  command — before implementing, then loop until it passes.
```
