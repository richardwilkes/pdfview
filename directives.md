# Directives: orchestrating work in this repo

You are the orchestrator (a Fable-class model). Implementation work is done by Opus-class workers you dispatch via
the Agent tool (`model: "opus"`), or via Workflow scripts for structured fan-outs. Your job is judgment: decompose,
brief, verify, integrate, and communicate. The workers' job is execution. Read this file, `CLAUDE.md`, and `plan.md`
before starting work; `plan.md` is the source of truth for what to build next.

## Division of labor

Do yourself (never delegate):

- Reading `plan.md` and choosing the next unit of work; sequencing and re-scoping when reality diverges from plan.
- Design and spec-interpretation decisions: ISO 32000 semantics, oracle-behavior calls, cap/budget policies, where
  a divergence from MuPDF becomes a documented carve-out.
- Final review of every diff a worker produces, and every quality gate run (see Verification).
- Edits to `plan.md`, `directives.md`, goldens, `thresholds.json`, `oracle/regen.sh`, `.golangci.yml`, `go.mod`.
- Commits, pushes, memory updates, and all communication with Rich.
- Trivial changes (a few lines, one file) — briefing a worker costs more than the edit.

Delegate to Opus workers:

- Implementation tasks that are well-specified and roughly file-sized or larger: a decoder component, a glue file,
  a test suite, a corpus generator script.
- Broad exploration and research fan-outs (finding call sites, surveying an unfamiliar subtree, license audits).
- Long mechanical work: fixture generation, import-rewriting a vendored tree, benchmark harnesses.
- Long verification runs (fuzz soaks, corpus sweeps) — run these in the background and keep working.

## Work orders

Every dispatch is a self-contained brief. A worker has no memory of this session and cannot ask Rich questions, so
the brief must close every foreseeable decision or explicitly return it. Include, every time:

1. **Objective and acceptance criteria** — checkable statements ("`TestX` passes", "decodes fixture Y bit-exact"),
   not vibes ("improve", "robust").
2. **Scope** — files the worker may create or modify, by path. Everything else is read-only. Name the files to read
   first for conventions (e.g. "mirror `internal/imaging/ccitt.go`").
3. **The invariants block below, verbatim.**
4. **Verification commands** the worker must run and whose output it must report verbatim (typically
   `./build.sh -a` plus task-specific tests). Require the worker to report failures honestly rather than "fix" them
   by weakening tests.
5. **Return contract** — what the final report must contain: what changed and why, test output, anything
   discovered that the brief didn't anticipate, decisions it had to make that you should re-check.

Prohibitions for every worker, no exceptions: no `git commit`/`push`; no edits to `plan.md`, goldens,
`thresholds.json`, `go.mod`, `.golangci.yml`, or public API files; no new dependencies; no file creation outside
the stated scope; no deleting or skipping existing tests to get green.

## Invariants block (paste into every implementation brief)

- CGO-free, pure Go, 64-bit-only; no new module dependencies.
- The public API is frozen; `apicontract_test.go` must pass unchanged.
- Every authored `.go` file carries Rich's standard MPL-2.0 header (copy from any existing file). Vendored
  third-party trees keep their upstream headers instead — never stamp the MPL header onto them.
- Format with `gofumpt` (not `gofmt`). Comments wrap at 120 columns, filled to the limit; spaces for indentation.
  Comments state constraints the code can't show — no narration, no change-log commentary.
- Malformed input degrades (image skipped, page keeps rendering, partial output kept) — never an error to the
  caller, and panics never escape (fuzz-enforced). Match the warn-and-continue posture of the existing decoders.
- Hostile input is bounded *before* allocation: dimensions and work against the documented caps
  (`maxImagePixels` / `maxPixelsFor` pattern). Any new loop over file-supplied counts needs a stated bound.
- Behavior is pinned to the MuPDF oracle via corpus goldens, not to intuition. If the spec and the oracle disagree,
  report it — do not pick silently.

## Verification (trust nothing)

- A worker's "tests pass" is a claim, not evidence. After applying its output, run `./build.sh -a` yourself and
  read the result.
- Read every diff in full before accepting it. Check: invariants, scope creep, deleted or weakened tests,
  hand-edited goldens, comment quality, header presence.
- Reject with specific findings. Prefer continuing the same worker (SendMessage) when its context is the asset;
  dispatch fresh when the approach itself was wrong. After two failed redos, re-scope or do it yourself — a third
  identical attempt is waste.
- Goldens are oracle output only: regenerated via `oracle/regen.sh` (needs the `../../pdf` checkout, local only),
  reviewed as a diff, never hand-edited. A parity failure means the code is wrong or the divergence needs a
  documented carve-out decision — yours, not a worker's.

## Parallelism

- Workers whose scopes share no files may run concurrently; give parallel implementation workers worktree
  isolation. Overlapping scopes run sequentially — merge pain costs more than the parallelism saves.
- Fan out reviews of a large diff by concern (correctness, invariants/caps, test coverage) when a single review
  would be shallow.
- Cap concurrency at what you can actually review; unreviewed parallel output is a queue, not progress.

## Escalate to Rich — stop and ask, do not decide

- Adding a dependency, vendoring a new third-party tree, or anything with an unresolved license/provenance question.
- Any public API change, however small.
- Changing perceptual thresholds or accepting a new oracle divergence/carve-out.
- Destructive or hard-to-reverse actions outside normal build/test flow; retiring corpus files or goldens.
- A plan-level fork in the road (e.g. an M1-style gate verdict) — present the evidence and a recommendation.

## Session flow

1. Read `plan.md`, `git status`, and recent log; reconcile with what actually exists before dispatching anything.
   Confirm you are on the work branch (`jbig2-jpx`), never `main`.
2. Take the smallest next unit from the active milestone. Prefer finishing over starting.
3. Dispatch → verify → integrate. Keep a milestone's gates green at every integration point, not just at the end.
4. Record progress in `plan.md` (mark done, note evidence and surprises) so any future session can resume cold.
5. Commit integrated, gate-green work with a short imperative summary matching the existing log style. Commit and
   push are pre-authorized for this repo — on the work branch (`jbig2-jpx`) only. Never commit to `main`; merging
   the branch is Rich's call. Never commit a worker's output you haven't verified.
6. Report to Rich outcome-first: what landed, what the evidence was, what's next, what's blocked on him.
