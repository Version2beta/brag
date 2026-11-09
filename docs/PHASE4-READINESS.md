# Phase 4 readiness assessment

**Phase 4A implemented: static-only Pi `/brag` and `/brag all`.** This is a bounded report adapter, not an autonomous agent or broad reasoning assessor. It validates schema-v2 reports and surfaces limited evidence. It adds no source inspection beyond BRAG's default static analysis and runs no dynamic mode. Later Phase 4 work remains unimplemented.

## Decision and boundary

Phase 4A consumes only the CLI's default schema-v2 report and validates it against a bundled contract. It performs no additional source inspection and does not read sidecars; bounded source-line evidence already present in the CLI report may be included in the projection. It creates no separate report schema and sends a bounded projection of validated evidence to Pi's configured model provider for an evidence-linked response. The complete schema-validated CLI report retains its ordered `history.included_commit_ids`; the model-facing projection compacts that list to its boundary, count, and first/last commit IDs so large histories remain inside the message bound. It never adds `--dynamic-tests`, `--mutation-tests`, or `--test-contributions`. Those modes execute repository tests with host-user privileges and are not sandboxed; disabled Go module lookup does not prevent test code from using the network.

This is not readiness for an autonomous or general code-quality assessor. The CLI itself produces measurements and status, not reasoning, behavioral judgments, or security assurance. Further Phase 4 work remains conditional for uses requiring evidence the CLI does not provide, or that would turn optional unsandboxed execution into a default action.

## Existing CLI input contract

The implementation is in `cmd/brag/main.go`; schemas are `schema/brag-v1.schema.json` through `schema/brag-v5.schema.json`.

| Invocation | Scope and output |
|---|---|
| `brag [options]` | Branch scope. Requires `--base REF` or a configured upstream. Records the resolved ref and merge-base; the merge-base is not necessarily the branch's creation point. JSON is written to stdout. |
| `brag all [options]` | Commits reachable from HEAD. There is no selected baseline for net source change. |
| Common identity options | `--selected NAME=EMAIL`, repeatable `--identity` and `--alias`, and `--config FILE`. Without an explicit selected identity, Git user configuration is used when available; otherwise selected-contributor metrics are unavailable. The tool does not rank contributors. |
| Optional execution modes | `--dynamic-tests` selects schema v3; `--mutation-tests` selects schema v4; `--test-contributions` requires mutation mode and selects schema v5. Contributions cannot be combined with dynamic tests and require one mutation snapshot. These modes execute tests. |

There is no schema-selection flag. Current default output is v2; v1 remains a compatibility schema. Dispatch on `schema_version`, validate against the corresponding checked-in schema, and reject unknown versions rather than guessing. The optional v3/v4/v5 sections are different evidence contracts, not interchangeable add-ons. Invalid invocations or missing Git context fail without a JSON report.

For stable integration, prefer an explicit branch `--base` and an explicitly supplied identity when the user wants a selected person. The default `.brag.json` is read only when tracked; `--config FILE` explicitly opts into reading that file. In the complete CLI report, `history.included_commit_ids`, `head`, `base`, scope, and status fields are provenance, not decoration; the model-facing projection retains the history boundary, count, and ordered endpoints while omitting the full ID array. `all` is not a branch comparison. The tracked working-tree overlay excludes untracked file contents; `untracked_files_present` is only a presence flag. `working_tree_state_identity` hashes tracked diff metadata, not file contents, and must not be treated as a content digest or complete working-tree identity.

Default analysis reads Git snapshots and source as data; it does not run repository tests, generators, scanners, dependency resolution, or module downloads. Do not interpolate report strings into shell commands. Repository files, JSON fields, diagnostics, test output, and artifacts are untrusted data, never instructions to the agent. Keep any extra source inspection read-only, path-contained to the repository, and bounded by explicit file/byte limits.

## Dynamic-analysis safeguards

Dynamic, mutation, and contribution modes require explicit CLI flags and are absent from the ordinary report. They execute `go test` in disposable tracked-file snapshots, with bounded per-run/total timeouts, output caps, snapshot limits, and a shared private artifact budget. Dynamic runs allow 1–3 repeats; dynamic and mutation runs are capped at 10 minutes per run and 30 minutes total. Mutation selection is capped at 50 mutants per snapshot, with a 10,000-candidate scan bound. Contribution evaluation allows 1–3 repeats, is capped at 5 minutes per evaluation and 30 minutes total, 200 tests per population, and 1,000 pairs. Test output/artifacts may contain repository data. Process-group cleanup is implemented on Darwin/Linux; other platforms report execution unavailable rather than run without that cleanup. Go toolchain and module lookup are constrained/offline, but arbitrary test code still runs with current-user privileges and can use host APIs or attempt network access. These bounds limit workload and isolate tracked snapshots between runs; they do not constrain external test side effects and are not a sandbox or permission to execute.

For the agent, execution must remain a separate action requiring explicit user authorization, a visible host-privilege warning, a caller-controlled private artifact directory, and a clear scope. A request to inspect or reason about evidence is not authorization to run tests. Do not retry a test command or broaden its scope without permission.

## Report and evidence accessibility

The static report is a single JSON document on stdout; static v2 evidence needs no sidecar artifact. It carries methods, statuses, denominators for ratios, scope, history boundaries, limits, warnings, and unavailable reasons. A consumer must preserve these fields and distinguish measured zero from missing, unavailable, partial, failed, unsupported, truncated, and not-run evidence.

Dynamic details are different: schema v3 references JSON, coverage, and log files in a configured private directory outside the repository. Those references are local paths, can disclose the artifact directory's absolute path, and are not portable with the report. The v3 schema does **not** pair those artifact references with report-bound SHA-256 digests. Do not treat a v3 path as verified evidence; initially use only its top-level summary, or require a separately approved integrity contract before consuming sidecars.

Mutation schema v4 and contribution schema v5 pair supported artifact references with lowercase SHA-256 fields. V5 also requires matrix/evidence path-digest pairs together when present. The CLI verifies the selected manifest digest before reuse; the acceptance fixture also checked matrix and referenced evidence digests. A matching digest proves consistency with the digest in that report, not who produced the report or artifact. Artifact paths may be absent when evidence persistence fails. A future reader must restrict paths to an explicitly authorized private artifact root, reject traversal/symlink escapes, verify the digest before parsing, enforce size/schema limits, and preserve unavailable or inconclusive status. Never fetch a missing artifact from elsewhere.

The v5 matrix and test-output evidence can contain repository test output. Do not expose raw artifacts or absolute local paths in user-facing summaries unless needed and authorized. Keep private evidence local.

## Possible future Phase 4 reasoning-report contract (not implemented in Phase 4A)

Use a separately versioned report (for example, `brag-agent-report-v1`); do not append agent judgments to schemas v1–v5. Avoid a single quality score. Each conclusion should be reviewable from nearby evidence and label observation, interpretation, and uncertainty separately.

| Section | Required content |
|---|---|
| Input provenance | CLI/tool version, report schema version, scope, HEAD/base/merge-base, selected-identity availability, and a digest of the exact input report. Preserve the raw report's statuses. |
| Summary | A short, scoped conclusion with direct references to evidence records. No unsupported global ranking or impact claim. |
| Evidence records | Category, exact source status, observation, interpretation (if any), denominator/units, limitations, and references: JSON Pointer plus snapshot; or repository path and line range for inspected code. Sidecars also require the expected SHA-256 and verification result. |
| Assessment | Separate contract fidelity, behavior/test adequacy, security interpretation, reasoning simplicity, context locality, and verification confidence. Mark a dimension unavailable when evidence is absent. Do not present qualitative judgments as CLI measurements. |
| Limitations and next steps | Explicit unavailable, partial, truncated, unsupported, and unverified evidence; smallest useful next inspection/action. Any execution request must remain a separate explicit user authorization. |

A claim without a traceable input reference is not reportable. Evidence links should identify the snapshot and preserve the distinction between CLI data and additional source observations. Confidence should be explained per claim, not collapsed into an opaque score.

## Evidence sufficiency matrix

| Topic | Existing evidence | Sufficient for / not sufficient for |
|---|---|---|
| Contributor activity | `contributors`, `selected_contributor`, commit counts, coauthor counts, churn, ratios, and exact included history (`main.go`). | Scoped Git activity and attribution accounting. Not effort, quality, impact, or all work; coauthor and author counts can overlap. Selected identity may be unavailable. |
| PRs and code reviews | `unavailable.pull_requests` and `unavailable.code_reviews`; no hosted-service fields. | Explicitly report unavailable. Cannot report PR volume, review quality, or infer those from commits. |
| Source change | Branch `net_source_change` against merge-base; cumulative commit churn and tracked overlay. | Snapshot/churn facts with their methods. Not code quality. `all` has no net-source baseline; untracked content is excluded. |
| Complexity and maintainability | `engineering_evidence.complexity`: custom `brag-go-ast-v2` Go function metrics, distributions, and bounded snapshot comparisons. `unavailable.maintainability` remains explicit. | Point to static Go hotspots and changes. Not readability, maintainability, reasoning difficulty, build-tag behavior, or runtime complexity. Detail and source limits apply. |
| Duplication | `engineering_evidence.duplication` is unavailable by design. | State that it was not measured; do not infer absence of duplication. |
| Test inventory | `engineering_evidence.test_inventory`: conventional top-level Go test/benchmark/example inventory, ratios, and bounded history. | Describe inventory and declared denominators. Not assertions' meaning, test quality, execution, or behavioral adequacy. Parse failures, unsupported languages, caps, and attribution limits matter. |
| Coverage | Static reports mark `test_coverage` unavailable. Schema v3 can report opt-in statement coverage and comparisons requiring compatible coverable-unit identities. | With explicit user authorization, report observed execution coverage and denominator. Not correctness or adequacy; v3 sidecars lack report-bound digests. |
| Mutation evidence | Opt-in v4 mutation run; bounded selected mutants, eligible/population status, counts and score. | Report sampled outcomes with control, status, and denominator. Not whole-suite effectiveness or defect-detection quality; execution is unsandboxed. |
| Test contributions | Opt-in v5; isolated test/mutation outcomes, detector/overlap/set-derived marginal evidence, complete-denominator gates and definition attribution. | Bounded experimental evidence only. It does not prove causality, test quality, or developer impact. All-scope historical/developer groups are unavailable; marginals are not remove-test suite reruns; merge/nested-module history can be partial. |
| Reliability | `unavailable.reliability`; repeated dynamic outcomes may mark observed instability. | Report only observed differences. Not proof of flakiness or general reliability. |
| Architecture | Static Go import graph, direct edges, test-only imports, cycles, fan-in/out and bounded comparisons. | Structural import evidence. Not runtime coupling, build-selected graph, dependency resolution, or design quality. |
| Documentation/generation | Go comment-presence ratios, tracked documentation content changes, canonical generated markers and recognized generator patterns. | Presence/change indicators. Not documentation quality, confirmed hand-authorship, or generator correctness; generators are not run. |
| Security | Static source/dependency indicators in `engineering_evidence.security`; vulnerability scanner is `unavailable` / `not_run`. | Locate selected patterns for review. Not vulnerability detection, exploitability, reachability, or security assurance. |
| Non-Go code | Recognized unsupported-language file counts in applicable static snapshots. | Disclose unsupported population. No non-Go AST or behavioral analysis. |

## Pre-Phase-4A CLI readiness evidence

The following tests and fixture results are historical evidence for the CLI, not tests of the later Pi extension. Relevant checked-in tests include:

- `TestDynamicTestsAreExplicitlyOptIn` (`cmd/brag/main_test.go`) uses an execution marker: the static command leaves it absent, while the explicit dynamic flag runs the fixture test. `TestMutationOptOutIsStatic` (`cmd/brag/mutation_tests_test.go`) protects the mutation opt-out path. `TestDynamicArtifactPathMustBeOutsideRepository` covers the artifact-root boundary.
- `TestV1SchemaRemainsPhase1Compatible`, `TestV2SchemaDeclaresRequiredReportAndEngineeringFields`, `TestV3SchemaDeclaresDynamicRunContract`, `TestV4SchemaRequiresMutationReportAndArtifacts`, and `TestV5SchemaDeclaresContributionSection` (`cmd/brag/schema_test.go`) cover declared contract boundaries; these tests do not replace instance validation.
- `TestContributionCalculationsFailClosedAndDeriveSets`, `TestContributionSuiteInteractionClassifications`, `TestContributionGroupsRequireCompleteSameIDDenominator`, `TestContributionHistoryMergeTopologySkipsResolutionDefinitions`, and `TestContributionCLIUsesSameManifestAndEvidenceForGroups` cover bounded calculations, history, and artifacts.

The prior offline acceptance record reports `go test -count=1 ./...`, `go vet ./...`, build, schema/artifact verification, and cross-compilation passed on the same Go/schema source state. The pre-Phase-4A readiness review did not rerun repository tests or opt-in modes. Separately, a temporary Git copy of the source was used for static branch and `all` commands: both reports validated against schema v2; repeated branch JSON was byte-identical; an uninvoked test marker stayed absent. No Git metadata was created in this checkout.

## Limitations, corrections, and readiness gates

- The pre-implementation readiness review found no CLI/schema correction necessary for the bounded static-first path. Phase 4A adds a separate Pi adapter without changing the CLI or schema contracts; broader source-inspection and reasoning work remains out of scope.
- Do not use schema-v3 sidecars as verified detailed evidence until their integrity is separately established. Do not change a v3 report into a v5 interpretation or silently combine incompatible modes.
- Static AST counts and indicators are evidence, not judgments. Keep unavailable categories visible; never replace missing evidence with zero or a favorable conclusion.
- Phase 3B.2's accepted evidence remains bounded and experimental. It does not establish causal defect detection or developer impact.
- The checkout has no `.git` metadata, so its revision, status, staging, and integration against its own history are unverifiable. Temporary Git fixtures are the available CLI validation boundary.
- The copied-source fixture produced valid v2 JSON reports of 127,220 bytes (branch) and 87,403 bytes (`all`); repeated branch output was byte-identical and the static test-execution tripwire remained absent. These sizes characterize only this fixture, not a maximum or cross-repository performance bound.
- Historical note from the pre-Phase-4A readiness review: all 25 files in the prior acceptance hash manifest matched; that review changed documentation only, and the Go/schema sources were not edited. The root `./brag` executable remains at SHA-256 `bec8e2399477b00c2288bca13a366685daca4aef112928750432799979a09913`. Prior offline full tests, vet, build, schema/artifact checks, and Linux/Windows test-binary cross-compilation passed for the unchanged code/schema sources; those cross-platform binaries were not run.

## Phase 4A implementation boundary

The Pi extension is `.pi/extensions/brag.js`. For use across arbitrary analyzed repositories, install this trusted extension and its sibling schema file in Pi's global extension directory (for example, `~/.pi/agent/extensions/brag.js` and `~/.pi/agent/extensions/brag-v2.schema.json`); do not load executable extensions from the repository being analyzed. Configure `BRAG_BIN` and `BRAG_GIT` as absolute trusted executable paths in the extension host environment. Optionally set `BRAG_SELECTED=NAME=EMAIL` to pass the CLI's existing explicit `--selected` identity to `/brag` or `/brag all`; control characters, empty fields, and values over 512 UTF-8 bytes are rejected. When unset, the CLI may use local Git identity configuration, but never selects an author by ranking or infers identity from repository history. If the resulting selected identity is unavailable, the report is labeled repository-only. An explicitly configured non-empty `BRAG_BASE` passes `--base REF` for branch scope only. Empty, option-like, whitespace/control-containing base values fail closed. `/brag all` passes `all`, optionally with the explicit selected identity, and ignores `BRAG_BASE`. Other command arguments are rejected without process creation. The executable is resolved, BRAG must be outside the analyzed repository, the child environment removes inherited `GIT_*` overrides and sets `PATH` to the configured Git executable directory. Invocation is argv-only with fixed timeout/output limits. It accepts schema v2 only, checks the bundled schema and rejects unknown top-level report properties; it does not read sidecars or perform additional source inspection. The CLI report may already contain bounded source-line excerpts, which can be sent to the provider. Child output/report strings remain untrusted data.

The response uses a bounded projection of scope, status, provenance, denominators, snapshot-level complexity/test-inventory/documentation/architecture/security evidence, working-tree state, and unavailable categories. It begins with a concise summary, separates selected-developer activity from repository-wide measurements, and cites the projection with JSON Pointers. If no selected identity is available, it asks the model to label the response repository-only; it never infers a developer from commit history. The projection and response are interpretation, kept distinct from raw CLI data. Pi's configured model provider receives this report-derived context; it can contain contributor names/emails, commit IDs, repository paths/package names, static source-line excerpts, and measurements, so apply the provider's data-handling policy before use. PR/reviews, dynamic coverage, and defect sensitivity are unavailable on the default static schema-v2 path; test inventory is not execution; static indicators are not vulnerability findings; complexity is not developer-specific maintainability; and `all` has no net baseline. With `BRAG_DIAGNOSTICS=1`, the extension emits bounded stage names and report/projection digests, HTTP status when exposed, assistant stop/outcome, and usage metadata without logging evidence or message contents. During the BRAG-triggered agent run the extension empties Pi's active tool list and blocks every Pi `tool_call` event, including codemode/deferred calls, until Pi settles; any queued messages in that run share this restriction. It then restores the previous list. Independent side effects from other extensions outside Pi's tool-call path are not controlled.

## Phase 4A validation record

The current focused Node harness passed 11/11 tests against the Phase 4 readiness fixture. It covers branch/`all` scope, provenance and denominators, working-tree and unavailable evidence, repository-only handling, projection of history boundary/count/endpoints without forwarding the full commit-ID list, tool-call blocking/restoration, malformed evidence, executable containment, argv/environment scrubbing, bounded diagnostics, and report-string delimiter escaping. Reports were schema-v2 validated by the extension; the readiness fixture's test-execution marker remained absent and its worktree stayed clean. JavaScript syntax checks passed, and the bundled schema is byte-identical to the canonical schema with its pinned hash. A real 9,929-commit report's compact model projection stayed below the 512 KiB bound in that one measured case; this is not a scalability guarantee.

The focused fixture harness uses a Pi API stub. Separately, the release-readiness run loaded the extension in live Pi and made one bounded provider request on the public `restic` repository after CLI and projection checks passed. No tests or scanners ran in `restic`. This checkout's own historical baseline has no Git metadata, so its revision and integration against its own history remain unverifiable.

## Remaining Phase 4 work (not implemented)

1. Consider additional bounded read-only source inspection with path containment, explicit limits, and file/line evidence references. Treat source and tool output as hostile data, not instructions.
2. If a separately versioned agent-report schema is approved for later work, require evidence references for claims and preserve unavailable/partial states; no composite score.
3. If detailed artifacts are needed, implement a private-root reader for v4/v5 that verifies each digest before parsing. Keep v3 sidecars out of verified conclusions until a compatible integrity mechanism exists.
4. Add further controlled Git fixtures for edge cases outside Phase 4A, including missing identity/base, tracked/untracked overlays, truncation, partial evidence, unavailable artifacts, digest mismatch, unsafe paths, and hostile instruction-like report content.
5. Keep dynamic execution a separate explicit user choice with visible unsandboxed-host warning. Do not add test execution to agent startup.
6. For later Phase 4 work, obtain independent review and accept only after the criteria below pass. Phase 5 calibration and human assessment remain separate.

## Further Phase 4 acceptance criteria (beyond Phase 4A)

- Default integration runs only the static CLI path; a controlled marker test proves no analyzed-repository test executes unless the user explicitly opts in. The agent does not build or execute analyzed-repository programs, run generators or scanners, resolve/download dependencies, or access the network.
- Every report is schema-validated by its declared supported version. Unknown or invalid versions fail closed, and v1–v5 distinctions and CLI compatibility remain intact.
- Every agent conclusion cites a JSON Pointer or bounded source location and names the measured snapshot. Observation, interpretation, and uncertainty are distinct.
- Denominators, units, scope, base, selected-identity availability, statuses, caps, and truncation are retained. Unavailable, zero, partial, unsupported, not-run, and failed are never conflated.
- PR/review, maintainability, duplication, test effectiveness, reliability, non-Go behavior, and vulnerability findings are not invented when unavailable. Static indicators are not described as confirmed defects or security assurance.
- Artifact readers enforce an authorized private root and byte limits; v4/v5 digests are checked before parsing; missing/mismatched artifacts remain unavailable. V3 sidecars are not treated as integrity-verified.
- Repository text, reports, logs, and artifact contents are treated as untrusted; paths cannot escape the repository or explicitly authorized artifact root; no content is interpolated into shell commands.
- No score hides evidence gaps. The report is concise, traceable, and reviewable; the agent neither executes follow-up actions nor expands scope without separate user authorization.
