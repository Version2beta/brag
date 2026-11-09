# BRAG v0.1.0-rc.1 — release candidate

**Status:** Owner-approved source candidate for local commit and annotated tag `v0.1.0-rc.1`. Remote setup, pushing, and publication are outside this authorization. BRAG is licensed under Apache-2.0; see [LICENSE](../LICENSE).

BRAG produces an evidence-backed summary of bounded Git history and static repository measurements. It keeps selected-contributor activity separate from repository-wide evidence and reports unavailable or partial measurements instead of filling gaps by inference. It does not rank contributors or establish individual impact, quality, security, or productivity.

## Candidate highlights

- Batched Git history processing preserves ordered commit attribution and per-commit churn semantics while bounding metadata and blob reads.
- The Pi extension accepts a validated schema-v2 report and compacts history IDs to a boundary, count, and ordered endpoints before sending its evidence projection.
- `BRAG_SELECTED` provides an explicit identity override; unavailable identity remains repository-only rather than inferred from history.
- The dynamic-test timeout correction preserves timeout classifications when coverage output is missing.
- The Pi summary instructions require the selected author's exact commit fraction and percentage from one ratio, and qualify Go-only evidence as such.

## Candidate contents and exclusions

`RELEASE-CANDIDATE-MANIFEST.sha256` is the sorted source-package allowlist and checksum record. It covers every intended source, schema, test, license, and public documentation file except the checksum manifest itself. This candidate has 40 manifest-listed files plus the manifest (41 files total). To assemble a source copy, include every listed path plus the manifest; omit Git metadata and anything not listed. Verify listed files from the package root with:

```sh
shasum -a 256 -c RELEASE-CANDIDATE-MANIFEST.sha256
```

The allowlist includes the Go CLI and tests, canonical and bundled schemas, Pi extension and tests, installation/usage documentation, roadmap, this candidate note, and the official Apache License 2.0 text in the root `LICENSE` file. It excludes the internal operator readiness record, private reports/projections, temporary logs, local configuration, binaries (including the ignored root `brag`), test executables, coverage files, and other generated artifacts. The package review found no third-party dependencies or bundled third-party code and no existing NOTICE material requiring a separate `NOTICE` file. Apache-2.0 applies to BRAG; it does not change any third-party component's license.

## Capabilities and boundaries

- The default CLI reads Git history and tracked snapshots without running code from the analyzed repository, downloading its modules, or invoking scanners or generators. `brag --base REF` (or a configured upstream) measures branch scope; `brag all` measures commits reachable from `HEAD` and has no net-change baseline.
- Author-commit share is the selected identity's authored commits divided by all included repository commits. The JSON ratio carries numerator, denominator, and value. Coauthor credits are separate and can overlap; they are not added to that denominator. An unavailable identity stays unavailable, and the CLI does not select a contributor by ranking.
- Complexity, test inventory/test additions, architecture, and source-security indicators are Go-only. Non-Go source counts are disclosed as unsupported for those measurements. Documentation evidence covers supported tracked document files and Go comments; comment or file presence is not documentation quality.
- Pull requests/reviews, test execution/effectiveness, coverage, reliability, maintainability, duplication, and other unsupported categories remain unavailable as documented. Static security indicators are not vulnerability findings or assurance.
- The Pi extension accepts schema v2 and submits a bounded evidence projection to Pi's configured model provider. A selected-identity summary is instructed to show the exact author-commit numerator/denominator and percentage from the same ratio value. The extension does not run dynamic modes or inspect repository files itself. Provider requests can carry contributor identities, commit IDs, paths, and bounded source excerpts; apply the provider's data-handling policy.
- Dynamic, mutation, and test-contribution modes are explicit opt-ins. They execute Go tests with the current user's privileges and are not a sandbox. Time, output, and artifact limits do not prevent test code from using host APIs or attempting network access. The Pi extension's 30-second CLI timeout does not guarantee cleanup of Git descendants.

The CLI reports `tool_version: v0.1.0-rc.1` and default `schema_version: 2`; opt-in modes use schemas v3–v5. The Pi extension is shipped in the same source candidate and uses schema compatibility; it has no separate release-version mechanism. `go.mod` declares Go 1.22. The candidate was exercised with Go 1.27.2 on Darwin/arm64; Linux/amd64 and Windows/amd64 test binaries were cross-compiled but not run. This is not a broader runtime support claim.

## Install and use

From the source root with Go 1.22 or newer:

```sh
go install ./cmd/brag
# or build a local binary:
go build -trimpath -o ./brag ./cmd/brag
```

Use `brag --base REF` for an explicit branch comparison, or `brag all` for history reachable from `HEAD`. The branch command needs `--base REF` or configured upstream metadata. Use `--selected NAME=EMAIL` when developer-specific measures are wanted; without an available identity, the result is repository-only. See [README.md](../README.md) for identity/configuration, metric interpretation, Pi setup, and opt-in mode safety details.

For Pi, copy `.pi/extensions/brag.js` and its sibling `.pi/extensions/brag-v2.schema.json` together to a trusted global extension directory (for example, `~/.pi/agent/extensions/`). Configure `BRAG_BIN` and `BRAG_GIT` as absolute trusted executable paths in the extension host environment. Optionally configure `BRAG_SELECTED=NAME=EMAIL`; the extension passes it to the existing CLI option for either command. Do not load extension code from an analyzed repository.

## Local validation

The Go suite and vet can be run without provider access or tests in analyzed repositories:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./...
node --check .pi/extensions/brag.js
node --check tests/pi-extension/brag.test.js
```

The Pi harness uses a stub API and makes no provider request. Its integration cases need `BRAG_BIN` (the built CLI), `BRAG_GIT` (an absolute Git executable path), `BRAG_FIXTURE_REPO`, and `BRAG_TEST_BASE` naming a local branch in a disposable fixture repository. Set `BRAG_FIXTURE_CWD` if the fixture working directory is not its root. Without those values, integration cases are skipped. Use a scratch Git fixture, not a private report or an analyzed production repository.

A full local harness example, run from the source root, is:

```sh
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
go build -trimpath -o "$scratch/brag" ./cmd/brag
fixture="$scratch/repo"
mkdir -p "$fixture"
git -C "$fixture" init -q
git -C "$fixture" config user.name 'BRAG Fixture'
git -C "$fixture" config user.email 'brag-fixture@example.test'
printf 'module fixture.local/brag\n\ngo 1.22\n' > "$fixture/go.mod"
printf 'package app\nfunc Work() int { return 1 }\n' > "$fixture/app.go"
printf 'package app\nimport "testing"\nfunc TestWork(t *testing.T) { if Work() != 1 { t.Fatal("unexpected result") } }\n' > "$fixture/app_test.go"
git -C "$fixture" add go.mod app.go app_test.go
git -C "$fixture" commit -qm baseline
git -C "$fixture" branch fixture-base
printf '\n// tracked fixture change\n' >> "$fixture/app.go"
git -C "$fixture" add app.go
git -C "$fixture" commit -qm current
BRAG_BIN="$scratch/brag" BRAG_GIT="$(command -v git)" BRAG_FIXTURE_REPO="$fixture" BRAG_FIXTURE_CWD="$fixture" BRAG_TEST_BASE=fixture-base node --test tests/pi-extension/brag.test.js
```

The harness only creates synthetic local commits and exercises the extension API stub; it does not contact Pi or a provider. The manifest and validation results establish this candidate's stated source contents and tested host behavior only, not universal correctness, scale, security, productivity, or cross-platform runtime behavior.
