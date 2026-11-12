# brag: handoff

Written 2026-11-11 from a cursory read of `github.com/version2beta/brag` at
its third commit ("feat: add portable skill and test contribution
comparison"). You are taking over analysis and development. Rob Martin owns
the project and makes the decisions listed in section 8.

Read this file, then `README.md`, `docs/ROADMAP.md` and
`docs/PHASE4-READINESS.md`, before you touch anything.

## 1. Your job

1. Do a deeper analysis of the code than the one below and write it to
   `docs/ANALYSIS.md` (section 6 says what it must answer). Stop there and
   wait for Rob.
2. After he has answered the open decisions, move the reasoning step out of
   the harness and into the `brag` binary, following the pattern in
   section 4, in the order given in section 7.

## 2. What brag is

A Go CLI that reads bounded Git history and static measurements of a
repository and emits one JSON evidence report. It separates what a selected
contributor did from repository-wide evidence. It does not rank people or
infer impact. Default analysis is read-only and offline.

Rob's intent for it: summarize an author's contribution to a repository in
terms of code quality, test quality, complexity and security, with a model
turning the evidence into a readable report.

## 3. State as found

Each line says how it is known: **read** (from source or docs), **ran**, or
**not checked**.

| Fact | Known by |
| --- | --- |
| Go 1.22, module name `brag`, no dependencies, Apache 2.0 | read |
| All Go code is one `main` package in `cmd/brag/`, about 11,500 lines including tests, 127 test functions | read |
| `go vet ./...` is clean | ran |
| `go test ./...` did not finish in ten minutes on a small container. Several tests run real `go test` and mutation subprocesses | ran, no result |
| Largest files: `dynamic_tests.go`, `mutation_tests.go`, `architecture_security.go`, `engineering.go`, `test_contributions.go`, `contribution_history.go`, `main.go` | read |
| CLI: `brag --base REF`, `brag all`, `--selected NAME=EMAIL`, `--identity`, `--alias`, `--config FILE`, `brag skill install` | read |
| Output schema is chosen by flags: default v2; `--dynamic-tests` v3; `--mutation-tests` v4; `--test-contributions` v5; `brag all --mutation-tests --test-contributions` v6 | read |
| `schema/` holds v1 to v6. `docs/PHASE4-READINESS.md` still says v1 to v5 | read |
| Dynamic, mutation and contribution modes run the analyzed repository's tests as the host user, unsandboxed | read |
| Only Go source is analyzed. Duplication and vulnerability scanning are reported as unavailable | read |
| The binary makes no model or network call. `net/http` appears only as a detection pattern | read |
| Identity config is `.brag.json`, read by default only when it is tracked in Git | read |

Where the reasoning happens today:

- `.pi/extensions/brag.js` (330 lines) registers `/brag` and `/brag all` in
  Pi. It runs the binary, validates the output with its own JavaScript
  JSON Schema validator against a bundled copy of the v2 schema, builds a
  bounded projection (512 KB cap), turns off the session's tools, and sends
  the projection to the session's model with `pi.sendUserMessage`.
- `cmd/brag/skill/SKILL.md`, embedded and installed by `brag skill install`,
  tells any harness's model to run the binary and present a Markdown
  dashboard: an at-a-glance band and six cards (Contributions, Complexity,
  Testing, Documentation, Security, Engineering usability).

So the deterministic half is in the binary and the reasoning half is
whatever model the user's session happens to be on. That has four costs:

1. The report depends on the session model, so two people get different
   reports from the same evidence, and nothing pins or records which model
   wrote it.
2. Nothing checks the model's output. The "every claim cites evidence" rule
   is an instruction, not a test.
3. The reasoning cannot run in CI, from a script, or in another harness
   without rewriting the extension.
4. The schema validator and the projection exist only in JavaScript, so the
   second adapter would have to copy them.

`docs/PHASE4-READINESS.md` already describes the fix in outline: a
separately versioned `brag-agent-report-v1` in which every conclusion cites
a JSON Pointer into the evidence report or a bounded source location. That
contract is the starting point for section 7.

## 4. The pattern to build toward

This is the same pattern as `wtha`, Rob's other tool. Its spec is the
reference for config and reviewer behavior.

1. **Standalone binary.** Everything the tool can do, it does from the
   command line with no harness present.
2. **Deterministic collection first.** Code gathers and measures. The
   evidence report is complete and useful with no model.
3. **The model step is a pipeline, not an agent.** One request in, one
   structured response out, no tools, a pinned model, a versioned prompt
   kept in the source.
4. **Code checks the model.** Every claim must cite evidence that code can
   resolve. A claim whose citation does not resolve is dropped and counted.
   The model interprets; it never supplies a number.
5. **Code renders the report** from a template, using the checked response.
   The model does not write the final Markdown.
6. **If the model is missing or fails, the tool still reports.** It prints
   the deterministic report and one line saying the reasoning step did not
   run and why.
7. **Harness adapters are thin.** A skill or extension runs the binary and
   shows what it printed. It holds no validator, no projection and no
   prompt.

Configuration, in this precedence: flags, environment, project file, user
file, defaults.

- Configure roles, not vendors: a `[reasoner]` with `provider`, `model`,
  `effort` (`off`, `low`, `medium`, `high`, `max`) and limits.
- Credentials come only from the environment (`BRAG_REASONER_API_KEY`, then
  the provider's usual variable) or from an `api_key_cmd` in the user file.
- A key written in any config file is an error. The project file may not
  set `api_key_cmd` or `base_url`, because a cloned repository must not be
  able to run a command or redirect a key.
- Unknown keys are errors.
- Cap input by tokens (`max_input_tokens`), not dollars.
- `brag doctor` prints every resolved value and which layer it came from,
  and never prints the key.

## 5. What is special about brag

- **The evidence contains personal data.** Names, emails, commit ids, paths
  and source excerpts go to the provider. The reasoning step must be opt-in
  (no reasoner configured means no call), and there must be a way to print
  exactly what would be sent without sending it.
- **Repository content is hostile input.** Commit messages, identifiers and
  comments can contain text written to steer a model. The prompt must mark
  evidence as data, the model gets no tools, and a test fixture must
  contain instruction-like text.
- **Test execution stays a separate, explicit choice.** The reasoning step
  must never add `--dynamic-tests`, `--mutation-tests` or
  `--test-contributions` by itself.
- **The status vocabulary is part of the contract.** Unavailable, zero,
  partial, unsupported, not run and failed are different things and the
  report must keep them apart, with denominators.
- **No composite score, no ranking of people.** This is a stated design
  rule of the project, not a gap.

## 6. Deeper analysis: what `docs/ANALYSIS.md` must answer

Give evidence for each answer (file and line, or a command and its output).

1. Does the full test suite pass, and how long does it take? Which tests
   are slow, and can a fast set be split from them with a build tag or
   `-short` so a loop can run it in under a minute?
2. Map the one `main` package: which files form which stage (history,
   identity, static measurement, dynamic, mutation, contribution, output)?
   Where are the seams along which it could be split into `internal/`
   packages, and what would break?
3. For schemas v1 to v6: which are produced by which code path, which are
   tested against their schema, and where do the documents disagree with
   the code (the v5/v6 drift is one known case)?
4. What exactly does the Pi extension put in its projection, and what does
   it leave out? This becomes the specification for the Go projection.
5. For each of the six dashboard cards, which evidence fields support it,
   and which claims a reader would expect that the evidence cannot support?
6. Which measurements are weakest (heuristics, caps, lexical matches)? A
   model will overread these; list them so the prompt can say so.
7. What would it take to analyze a second language? Is the Go-specific code
   behind an interface or spread through the package?
8. Review the dynamic and mutation runners for what they do to the host:
   working directories, environment passed through, timeouts, cleanup.
9. Is `RELEASE-CANDIDATE-MANIFEST.sha256` current, and does anything check
   it?

End the file with a short list of things you would change in section 7's
plan and why.

## 7. Development plan

Do these in order. Each ends with tests passing and one commit.

1. **Fast test set.** Make `go test -short ./...` skip the tests that run
   subprocess test suites. Nothing else changes.
2. **Projection in Go.** `brag project [flags]` (name is Rob's to change)
   emits the bounded model-facing projection that the extension builds
   today, from a report on stdin or from a fresh run. Golden-file tests.
3. **Validation in Go.** `brag validate` checks a report against its
   declared schema version and fails closed on an unknown version. If this
   needs a JSON Schema library, stop and ask (the project has no
   dependencies today); a hand-written check per version is the
   alternative.
4. **Config and doctor.** New `internal/config` and `brag doctor`, as in
   section 4. `wtha` has a small TOML subset parser, a config loader and a
   provider client in `internal/toml`, `internal/config` and `internal/llm`.
   Copy them; do not add a dependency between the two repositories.
5. **Reasoning report contract.** Write `schema/brag-agent-report-v1` from
   the table in `docs/PHASE4-READINESS.md`: provenance (with a digest of
   the input report), summary, evidence records, assessment by dimension,
   limitations. Every claim carries a JSON Pointer and the snapshot it
   refers to.
6. **Reasoner.** `brag report` runs the collection, builds the projection,
   makes one model call, parses the response, and checks it: each pointer
   must resolve in the input report, each quoted number must equal the
   value at its pointer, and a dimension with no evidence must be marked
   unavailable. Failed claims are dropped and counted in the output. One
   retry on an unparseable response, then fall back.
7. **Renderer.** A Go template turns the checked response and the evidence
   into the six-card dashboard. Numbers in the dashboard come from the
   evidence report, never from the model's text.
8. **Thin adapters.** Reduce `.pi/extensions/brag.js` to: find the binary,
   run `brag report`, show the output. Rewrite `SKILL.md` to the same
   shape. Keep the old extension's executable-resolution and PATH
   hardening.
9. **Evaluation.** `brag eval DIR` over saved evidence reports with
   hand-written labels: for each, which claims are supportable. Report how
   often the model's surviving claims are wrong. This is the project's
   Phase 5 and needs Rob to do the labeling.

Tests for steps 5 to 7 use a fake model (a function variable the tests
replace), including a response with a pointer that does not resolve, a
number that does not match, and a response that follows an instruction
planted in a commit message.

## 8. Decisions that are Rob's

- Whether steps 2 to 7 go in `cmd/brag` or new `internal/` packages. The
  recommendation is new code in `internal/`, existing code left alone
  until the analysis says a split is safe.
- Whether a JSON Schema dependency is acceptable.
- Command names (`project`, `validate`, `report`, `doctor`, `eval`), and
  whether bare `brag` keeps printing JSON (recommended, for compatibility).
- Whether source excerpts are sent to the provider by default or only with
  a flag.
- The precision target for the evaluation.
- Whether the config, TOML and provider-client code shared with `wtha`
  should become one small module once a third tool needs it.

## 9. Working rules

- Tests decide whether something is done. Do not edit a test to make it
  pass; if a test is wrong, say so and stop.
- Do not change schemas v1 to v6 or the default output of existing
  commands.
- Do not run `--dynamic-tests`, `--mutation-tests` or
  `--test-contributions` against any repository other than the fixtures.
- Never write a credential to a file, a log, a fixture or a commit.
- The existing documents are long and state many limits. Keep the limits,
  but when you add documentation write it short, and correct statements
  that have drifted from the code instead of adding a note beside them.
- Work on a branch. Small commits, one step each.
- When the code and a document disagree, the code and its tests are the
  fact; record the disagreement in `docs/ANALYSIS.md`.

## 10. How to run this

Analysis (section 6) and the contract work (steps 5 to 7) need a strong
model reading a lot of code at once: GPT-6.1 Sol at high effort. The
mechanical steps (1 to 4, 8) suit the same loop used for `wtha`: Claude
Haiku 5.5 first, Sol on a retry, with a task list and one test command per
task. Have a different model family from the one that wrote the reasoner
review steps 5 to 7 before merging.
