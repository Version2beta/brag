package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	binary = filepath.Join(os.TempDir(), "brag-tests")
	c := exec.Command("go", "build", "-o", binary, ".")
	c.Dir = wd
	if b, e := c.CombinedOutput(); e != nil {
		panic(string(b))
	}
	code := m.Run()
	os.Remove(binary)
	os.Exit(code)
}
func TestDynamicTestsAreExplicitlyOptIn(t *testing.T) {
	d, base := mkRepo(t)
	os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/dynamic\n\ngo 1.22\n"), 0600)
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\nfunc Work() int { return 1 }\n"), 0600)
	marker := filepath.Join(t.TempDir(), "fixture-test-executed")
	testSource := fmt.Sprintf("package app\nimport (\"os\"; \"testing\")\nfunc TestPass(t *testing.T) { if Work()!=1 { t.Fatal(\"bad\") }; if err:=os.WriteFile(%q, []byte(\"ran\"), 0600); err!=nil { t.Fatal(err) } }\n", marker)
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(testSource), 0600)
	gitCmd(t, d, "add", "go.mod", "app.go", "app_test.go")
	gitCmd(t, d, "commit", "-qm", "add test")
	static, err := run(t, d, "all")
	if err != nil || strings.Contains(static, `"dynamic_tests"`) {
		t.Fatalf("default report unexpectedly contains dynamic evidence: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ordinary static report executed the analyzed test fixture")
	}
	artifacts := privateTempDir(t)
	out, err := run(t, d, "all", "--dynamic-tests", "--dynamic-artifacts", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("explicit dynamic execution did not run the test fixture: %v", err)
	}
	var got struct {
		SchemaVersion int                    `json:"schema_version"`
		Dynamic       *dynamicEvidence       `json:"dynamic_tests"`
		Unavailable   map[string]unavailable `json:"unavailable"`
	}
	if err = json.Unmarshal([]byte(out), &got); err != nil || got.SchemaVersion != 3 || got.Dynamic == nil || len(got.Dynamic.Runs) != 1 || got.Dynamic.Runs[0].Snapshot != "current_tracked_worktree" || got.Dynamic.Runs[0].Coverage.Status != "measured" || got.Dynamic.Runs[0].Coverage.Total == 0 || got.Dynamic.Runs[0].PackageCount == 0 || got.Dynamic.Runs[0].DetailsArtifact == "" {
		t.Fatalf("dynamic report missing: %v %s", err, out)
	}
	if got.Unavailable["reliability"].Status != "unavailable" || !strings.Contains(got.Unavailable["reliability"].Reason, "observed instability") {
		t.Fatalf("legacy reliability status contradicts dynamic evidence: %+v", got.Unavailable["reliability"])
	}
	var detail dynamicRunDetailsArtifact
	detailBytes, err := os.ReadFile(got.Dynamic.Runs[0].DetailsArtifact)
	if err != nil || json.Unmarshal(detailBytes, &detail) != nil || len(detail.Tests) == 0 || len(detail.Packages) == 0 || detail.Packages[0].CoverageStatus != "measured" {
		t.Fatalf("structured dynamic details missing: %v %+v", err, detail)
	}
	var reportDoc struct {
		Dynamic struct {
			Runs []map[string]json.RawMessage `json:"runs"`
		} `json:"dynamic_tests"`
	}
	if err := json.Unmarshal([]byte(out), &reportDoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := reportDoc.Dynamic.Runs[0]["tests"]; ok {
		t.Fatal("per-test detail leaked into the concise report")
	}
	if _, ok := reportDoc.Dynamic.Runs[0]["packages"]; ok {
		t.Fatal("per-package detail leaked into the concise report")
	}
	branchArtifacts := privateTempDir(t)
	branch, err := run(t, d, "--base", base, "--dynamic-tests", "--dynamic-artifacts", branchArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	var br struct {
		Dynamic *dynamicEvidence `json:"dynamic_tests"`
	}
	if err = json.Unmarshal([]byte(branch), &br); err != nil || br.Dynamic == nil || len(br.Dynamic.Runs) != 3 || br.Dynamic.Runs[0].Snapshot != "baseline" || br.Dynamic.Runs[1].Snapshot != "HEAD" || br.Dynamic.Runs[2].Snapshot != "current_tracked_worktree" || len(br.Dynamic.CoverageComparisons) == 0 || br.Dynamic.CoverageComparisons[0].CompatibleDenominator {
		t.Fatalf("branch snapshots missing: %v %+v", err, br.Dynamic)
	}
}

func TestDynamicSnapshotUsesGitBlobsNotArchiveFiltersAndDeduplicates(t *testing.T) {
	d, base := mkRepo(t)
	os.WriteFile(filepath.Join(d, ".gitattributes"), []byte("secret.go export-ignore\n"), 0600)
	os.WriteFile(filepath.Join(d, "secret.go"), []byte("package app\n"), 0600)
	gitCmd(t, d, "add", ".gitattributes", "secret.go")
	gitCmd(t, d, "commit", "-qm", "tracked source")
	sp := snapshotSpec{label: "HEAD", rev: gitCmd(t, d, "rev-parse", "HEAD")}
	dir, id, err := materializeDynamicSnapshot(d, sp)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer os.RemoveAll(dir)
	if _, err = os.Stat(filepath.Join(dir, "secret.go")); err != nil {
		t.Fatalf("Git blob omitted by archive attribute: %v", err)
	}
	again, same, err := materializeDynamicSnapshot(d, sp)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(again)
	if id != same {
		t.Fatal("identical snapshots have different identities")
	}
	os.WriteFile(filepath.Join(d, "secret.go"), []byte("package app\nfunc Changed() {}\n"), 0700)
	os.WriteFile(filepath.Join(d, "untracked.go"), []byte("package app\n"), 0600)
	overlay, overlayID, err := materializeDynamicSnapshot(d, snapshotSpec{label: "current_tracked_worktree", rev: sp.rev, overlay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(overlay)
	if _, err = os.Stat(filepath.Join(overlay, "untracked.go")); !os.IsNotExist(err) {
		t.Fatal("untracked file entered dynamic snapshot")
	}
	if data, err := os.ReadFile(filepath.Join(overlay, "secret.go")); err != nil || !strings.Contains(string(data), "Changed") {
		t.Fatal("tracked overlay content missing")
	}
	if overlayID == id {
		t.Fatal("tracked overlay change did not alter source identity")
	}
	if base == "" {
		t.Fatal("fixture baseline missing")
	}
}

func TestDynamicEventsAndCoverageDenominators(t *testing.T) {
	r := dynamicRun{Tests: []dynamicTest{}, TestCounts: map[string]int{"run": 0, "pass": 0, "fail": 0, "skip": 0}, Packages: []dynamicPackage{}}
	parseGoEvents([]byte(`{"Action":"run","Package":"example.test/p","Test":"TestA"}
{"Action":"pass","Package":"example.test/p","Test":"TestA","Elapsed":0.01}
{"Action":"skip","Package":"example.test/p","Test":"TestB"}
{"Action":"pass","Package":"example.test/p","Elapsed":0.02,"Coverage":"50.0%"}
`), &r)
	if r.TestCounts["run"] != 1 || r.TestCounts["pass"] != 1 || r.TestCounts["skip"] != 1 || len(r.Packages) != 1 {
		t.Fatalf("event parsing: %+v", r)
	}
	parseCoverage([]byte("mode: set\nexample.test/p/a.go:1.1,1.5 2 1\nexample.test/p/a.go:2.1,2.5 3 0\n"), &r)
	if r.Coverage.Status != "measured" || r.Coverage.Covered != 2 || r.Coverage.Total != 5 || r.Coverage.Percent == nil || *r.Coverage.Percent != 40 {
		t.Fatalf("coverage denominator: %+v", r.Coverage)
	}
}

func TestDynamicTimeoutAndOutputCap(t *testing.T) {
	d, _ := mkRepo(t)
	src := `package app
import ("testing"; "time")
func TestWait(t *testing.T) { time.Sleep(5*time.Second) }
`
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(src), 0600)
	gitCmd(t, d, "add", "app_test.go")
	dir, identity, err := materializeDynamicSnapshot(d, snapshotSpec{label: "timeout", overlay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	r := executeDynamic(context.Background(), dir, privateTempDir(t), "timeout", identity, 100*time.Millisecond, 0, &artifactBudget{})
	if r.Status != "timeout" || r.Classification != "run_timeout" {
		t.Fatalf("per-run timeout not reported: %+v", r)
	}
	totalCtx, cancelTotal := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	totalTimeout := executeDynamic(totalCtx, dir, privateTempDir(t), "total-timeout", identity, 10*time.Second, 0, &artifactBudget{})
	cancelTotal()
	if totalTimeout.Status != "timeout" || totalTimeout.Classification != "total_timeout" || totalTimeout.Coverage.Status != "unavailable" {
		t.Fatalf("total timeout without coverage artifact not reported: %+v", totalTimeout)
	}
	exact := &cappedOutput{limit: 4}
	_, _ = exact.Write([]byte("1234"))
	if exact.truncated || exact.String() != "1234" {
		t.Fatal("exact-boundary write incorrectly truncated")
	}
	out := &cappedOutput{limit: 4}
	_, _ = out.Write([]byte("0123456789"))
	if !out.truncated || out.Len() != 4 {
		t.Fatal("output limit did not report truncation")
	}
	artifactDir := privateTempDir(t)
	perFileBudget := &artifactBudget{}
	atLimit := make([]byte, dynamicArtifactBytes)
	artifactPath := writeDynamicArtifact(artifactDir, perFileBudget, "limit-", ".out", atLimit)
	if artifactPath == "" {
		t.Fatal("artifact at the per-file limit was rejected")
	}
	if info, err := os.Stat(artifactPath); err != nil || info.Size() != dynamicArtifactBytes || info.Mode().Perm() != 0600 {
		t.Fatalf("artifact bound or permissions incorrect: info=%v err=%v", info, err)
	}
	if p := writeDynamicArtifact(artifactDir, perFileBudget, "over-", ".out", make([]byte, dynamicArtifactBytes+1)); p != "" {
		t.Fatal("artifact above the per-file limit was accepted")
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("oversized artifact created a file: entries=%d err=%v", len(entries), err)
	}
	budget := &artifactBudget{used: dynamicArtifactTotalBytes - 1}
	if p := writeDynamicArtifact(privateTempDir(t), budget, "cap-", ".out", []byte("xx")); p != "" {
		t.Fatal("artifact budget was not enforced")
	}
	if budget.used != dynamicArtifactTotalBytes-1 {
		t.Fatal("failed artifact write consumed budget")
	}
}

func TestDynamicAllUnbornHeadIsUnavailable(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q")
	out, err := run(t, d, "all", "--dynamic-tests", "--dynamic-artifacts", privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Dynamic *dynamicEvidence `json:"dynamic_tests"`
	}
	if json.Unmarshal([]byte(out), &r) != nil || r.Dynamic == nil || r.Dynamic.Status != "unavailable" {
		t.Fatalf("unborn all should be unavailable: %+v", r.Dynamic)
	}
}

func TestDynamicFailSkipCompileAndDependencyOutcomes(t *testing.T) {
	d, _ := mkRepo(t)
	artifacts := privateTempDir(t)
	src := `package app
import "testing"
func TestPass(t *testing.T) {}
func TestFail(t *testing.T) { t.Fatal("expected") }
func TestSkip(t *testing.T) { t.Skip("expected") }
`
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(src), 0600)
	gitCmd(t, d, "add", "app_test.go")
	r := runFixtureDynamic(t, d, artifacts)
	if r.Classification != "test_failure" || r.TestCounts["pass"] < 1 || r.TestCounts["fail"] != 1 || r.TestCounts["skip"] != 1 || r.Coverage.Status != "measured_zero" {
		t.Fatalf("test outcomes/coverage: %+v", r)
	}
	var details dynamicRunDetailsArtifact
	detailBytes, err := os.ReadFile(r.DetailsArtifact)
	if err != nil || json.Unmarshal(detailBytes, &details) != nil || details.CoverageUnitIdentities == nil {
		t.Fatalf("zero-coverage detail artifact must use an empty identity array: %v %+v", err, details)
	}
	os.WriteFile(filepath.Join(d, "app.go"), []byte(`package app
func Broken( {
`), 0600)
	gitCmd(t, d, "add", "app.go")
	r = runFixtureDynamic(t, d, artifacts)
	if r.Classification != "compile_error" {
		t.Fatalf("compile classification: %+v", r)
	}
	os.WriteFile(filepath.Join(d, "go.mod"), []byte(`module example.test/missing

go 1.22

require example.invalid/notcached v1.0.0
`), 0600)
	os.WriteFile(filepath.Join(d, "app.go"), []byte(`package app
import _ "example.invalid/notcached"
`), 0600)
	gitCmd(t, d, "add", "go.mod", "app.go")
	r = runFixtureDynamic(t, d, artifacts)
	if r.Classification != "dependency_error" {
		t.Fatalf("dependency classification: %+v", r)
	}
	os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/setup\n\ngo 1.22\n"), 0600)
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\nfunc Work() {}\n"), 0600)
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte("package other\nimport \"testing\"\nfunc TestSetup(t *testing.T) {}\n"), 0600)
	gitCmd(t, d, "add", "go.mod", "app.go", "app_test.go")
	r = runFixtureDynamic(t, d, artifacts)
	if r.Classification != "setup_error" {
		t.Fatalf("setup failure classification: %+v", r)
	}
}
func privateTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp(t.TempDir(), "artifacts-")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func runFixtureDynamic(t *testing.T, root, artifacts string) dynamicRun {
	t.Helper()
	dir, id, err := materializeDynamicSnapshot(root, snapshotSpec{label: "fixture", overlay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	return executeDynamic(context.Background(), dir, artifacts, "fixture", id, 30*time.Second, 0, &artifactBudget{})
}

func TestBranchPackageCoverageComparisonRequiresExactUnits(t *testing.T) {
	d, _ := mkRepo(t)
	os.WriteFile(filepath.Join(d, "go.mod"), []byte(`module example.test/cov

go 1.22
`), 0600)
	os.WriteFile(filepath.Join(d, "app.go"), []byte(`package app
func F(x int) int { if x>0 { return 1 }; return 0 }
`), 0600)
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import "testing"
func TestF(t *testing.T) {}
`), 0600)
	gitCmd(t, d, "add", "go.mod", "app.go", "app_test.go")
	gitCmd(t, d, "commit", "-qm", "coverage baseline")
	base := gitCmd(t, d, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import "testing"
func TestF(t *testing.T) { if F(1)!=1 { t.Fatal("bad") } }
`), 0600)
	gitCmd(t, d, "add", "app_test.go")
	gitCmd(t, d, "commit", "-qm", "cover branches")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	artifacts := privateTempDir(t)
	r := runDynamicTests(context.Background(), "branch", head, &baseInfo{MergeBase: base}, d, 1, 30*time.Second, 2*time.Minute, artifacts)
	if len(r.Runs) != 3 || len(r.CoverageComparisons) < 1 {
		t.Fatalf("branch coverage snapshots incomplete: %+v", r)
	}
	var found *dynamicPackageCoverageComparison
	for i := range r.CoverageComparisons[0].Packages {
		if r.CoverageComparisons[0].Packages[i].Package == "example.test/cov" {
			found = &r.CoverageComparisons[0].Packages[i]
		}
	}
	if found == nil || found.Status != "compared" || found.FromCovered != 0 || found.FromTotal != 3 || found.ToCovered != 2 || found.ToTotal != 3 || r.CoverageComparisons[0].FromRunOrder == r.CoverageComparisons[0].ToRunOrder {
		t.Fatalf("package coverage comparison incorrect: %+v", r.CoverageComparisons[0])
	}
	var packageComparison dynamicPackageComparisonArtifact
	comparisonBytes, err := os.ReadFile(r.CoverageComparisons[0].PackageComparisonsArtifact)
	if err != nil || json.Unmarshal(comparisonBytes, &packageComparison) != nil || packageComparison.Kind != "brag-dynamic-package-comparison" || len(packageComparison.Packages) != 1 || packageComparison.Packages[0].FromTotal != 3 || packageComparison.Packages[0].ToTotal != 3 {
		t.Fatalf("structured package comparison artifact missing: %v %+v", err, packageComparison)
	}
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import "testing"
func TestF(t *testing.T) { if F(1)!=1 || F(-1)!=0 { t.Fatal("bad") } }
`), 0600)
	r = runDynamicTests(context.Background(), "branch", head, &baseInfo{MergeBase: base}, d, 1, 30*time.Second, 2*time.Minute, privateTempDir(t))
	if len(r.CoverageComparisons) != 3 || r.CoverageComparisons[2].Packages[0].Status != "compared" || r.CoverageComparisons[2].Packages[0].FromCovered != 2 || r.CoverageComparisons[2].Packages[0].FromTotal != 3 || r.CoverageComparisons[2].Packages[0].ToCovered != 3 || r.CoverageComparisons[2].Packages[0].ToTotal != 3 {
		t.Fatalf("complete branch coverage not compared against partial coverage: %+v", r.CoverageComparisons)
	}
	os.WriteFile(filepath.Join(d, "app.go"), []byte(`package app
func F(x int) int { if x>0 { return 1 }; return 0 }
func G() int { return 7 }
`), 0600)
	gitCmd(t, d, "add", "app.go")
	r = runDynamicTests(context.Background(), "branch", head, &baseInfo{MergeBase: base}, d, 1, 30*time.Second, 2*time.Minute, privateTempDir(t))
	if len(r.CoverageComparisons) < 2 || r.CoverageComparisons[len(r.CoverageComparisons)-1].Packages[0].Status != "incompatible" {
		t.Fatalf("changed coverable units treated as compatible: %+v", r.CoverageComparisons)
	}
}

func TestPackageCoverageComparisonPreservesAddedAndRemovedDenominators(t *testing.T) {
	fromPercent, toPercent := 25.0, 50.0
	a := dynamicRun{Packages: []dynamicPackage{{Package: "example.test/removed", Covered: 1, Total: 4, CoveragePercent: &fromPercent, CoverageStatus: "measured"}}}
	b := dynamicRun{Packages: []dynamicPackage{{Package: "example.test/added", Covered: 2, Total: 4, CoveragePercent: &toPercent, CoverageStatus: "measured"}}}
	got := comparePackageCoverage(a, b)
	if len(got) != 2 || got[0].Package != "example.test/added" || got[0].Status != "added" || got[0].ToCovered != 2 || got[0].ToTotal != 4 || got[0].ToPercent == nil || *got[0].ToPercent != 50 || got[1].Package != "example.test/removed" || got[1].Status != "removed" || got[1].FromCovered != 1 || got[1].FromTotal != 4 || got[1].FromPercent == nil || *got[1].FromPercent != 25 {
		t.Fatalf("added/removed package denominators were lost: %+v", got)
	}
}

func TestRepeatedPerTestOutcomeInstability(t *testing.T) {
	d, _ := mkRepo(t)
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import("os";"testing")
func TestToggle(t *testing.T){if _,e:=os.Stat("toggle");e==nil{t.Fatal("second run")};if e:=os.WriteFile("toggle",[]byte("x"),0600);e!=nil{t.Fatal(e)}}
`), 0600)
	gitCmd(t, d, "add", "app_test.go")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	unstable := runDynamicTests(context.Background(), "all", head, nil, d, 2, 30*time.Second, 2*time.Minute, privateTempDir(t))
	if len(unstable.Runs) != 2 || runOutcomeFingerprint(unstable.Runs[0]) == runOutcomeFingerprint(unstable.Runs[1]) || unstable.Runs[0].Instability == "" {
		t.Fatalf("per-test inconsistent outcomes not distinguished: %+v", unstable.Runs)
	}
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import "testing"
func TestStable(t *testing.T){}
`), 0600)
	gitCmd(t, d, "add", "app_test.go")
	os.WriteFile(filepath.Join(d, "app_test.go"), []byte(`package app
import "testing"
func TestStable(t *testing.T){}
`), 0600)
	gitCmd(t, d, "add", "app_test.go")
	stable := runDynamicTests(context.Background(), "all", head, nil, d, 2, 30*time.Second, 2*time.Minute, privateTempDir(t))
	if len(stable.Runs) != 2 || runOutcomeFingerprint(stable.Runs[0]) != runOutcomeFingerprint(stable.Runs[1]) || stable.Runs[0].Instability != "" {
		t.Fatalf("stable outcomes incorrectly reported unstable: %+v", stable.Runs)
	}
}

func TestDynamicTotalTimeoutAndCallerCancellation(t *testing.T) {
	d, _ := mkRepo(t)
	artifacts := privateTempDir(t)
	head := gitCmd(t, d, "rev-parse", "HEAD")
	e := runDynamicTests(context.Background(), "all", head, nil, d, 1, 500*time.Millisecond, time.Millisecond, artifacts)
	if e.Status != "partial" || e.TimeoutMillis != 500 || e.TotalTimeoutMillis != 1 {
		t.Fatalf("timeouts not recorded exactly: %+v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := runDynamicTests(ctx, "all", head, nil, d, 1, time.Second, time.Second, artifacts)
	if cancelled.Status != "cancelled" {
		t.Fatalf("caller cancellation not distinguished: %+v", cancelled)
	}
	if _, _, err := materializeDynamicSnapshotContext(ctx, d, snapshotSpec{label: "cancel", rev: head}); err == nil {
		t.Fatal("cancelled snapshot materialized")
	}
	if _, err := run(t, d, "all", "--dynamic-tests", "--dynamic-timeout", "500us", "--dynamic-artifacts", privateTempDir(t)); err == nil {
		t.Fatal("sub-millisecond timeout should be rejected")
	}
}

func TestDynamicArtifactPathMustBeOutsideRepository(t *testing.T) {
	d, _ := mkRepo(t)
	head := gitCmd(t, d, "rev-parse", "HEAD")
	out := runDynamicTests(context.Background(), "all", head, nil, d, 1, time.Second, 2*time.Second, d)
	if out.Status != "unavailable" {
		t.Fatalf("unsafe artifact path accepted: %+v", out)
	}
	outside := t.TempDir()
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	_ = runDynamicTests(context.Background(), "all", head, nil, d, 1, time.Second, 2*time.Second, outside)
	after, err := os.Stat(outside)
	if err != nil || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatal("caller-owned artifact permissions were modified")
	}
	target := filepath.Join(d, "generated-artifacts")
	if err := os.Symlink(d, filepath.Join(outside, "repo-link")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(outside, "repo-link", "generated-artifacts")
	out = runDynamicTests(context.Background(), "all", head, nil, d, 1, time.Second, 2*time.Second, alias)
	if out.Status != "unavailable" {
		t.Fatalf("symlink artifact alias accepted: %+v", out)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("artifact path was created inside repo before safety rejection")
	}
}

func TestGitEnvironmentDisablesOptionalLocksAndLazyFetch(t *testing.T) {
	env := gitEnvironment([]string{"PATH=/bin", "GIT_OPTIONAL_LOCKS=1", "GIT_NO_LAZY_FETCH=0", "OTHER=value"})
	seen := map[string]int{}
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_OPTIONAL_LOCKS=") || strings.HasPrefix(entry, "GIT_NO_LAZY_FETCH=") {
			seen[entry]++
		}
	}
	if seen["GIT_OPTIONAL_LOCKS=0"] != 1 || seen["GIT_NO_LAZY_FETCH=1"] != 1 || len(seen) != 2 {
		t.Fatalf("Git environment does not force read-only offline behavior: %v", env)
	}
}

func env(dir string) []string {
	return append(os.Environ(), "HOME="+filepath.Join(dir, "home"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env(dir)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v\n%s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command(binary, args...)
	c.Dir = dir
	c.Env = env(dir)
	b, e := c.CombinedOutput()
	return string(b), e
}
func mkRepo(t *testing.T) (string, string) {
	t.Helper()
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Ada")
	gitCmd(t, d, "config", "user.email", "ada@example.test")
	os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/fixture\n\ngo 1.22\n"), 0600)
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\n"), 0600)
	os.WriteFile(filepath.Join(d, "rename.txt"), []byte("rename me\n"), 0600)
	gitCmd(t, d, "add", "go.mod", "app.go", "rename.txt")
	gitCmd(t, d, "commit", "-qm", "base")
	return d, gitCmd(t, d, "rev-parse", "HEAD")
}
func decode(t *testing.T, s string) report {
	t.Helper()
	var r report
	if e := json.Unmarshal([]byte(s), &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestReachableScopeIdentityOverlayDeterminism(t *testing.T) {
	d, base := mkRepo(t)
	gitCmd(t, d, "switch", "-qc", "feature")
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\nfunc A() {}\n"), 0600)
	gitCmd(t, d, "commit", "-qam", "change\n\nCo-authored-by: Grace <grace@example.test>")
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\nfunc A() { }\n"), 0600)
	os.WriteFile(filepath.Join(d, "new.go"), []byte("package app\n"), 0600)
	gitCmd(t, d, "add", "new.go")
	os.WriteFile(filepath.Join(d, "new.go"), []byte("package app\nfunc B() {}\n"), 0600)
	os.WriteFile(filepath.Join(d, "deleted.txt"), []byte("old\n"), 0600)
	gitCmd(t, d, "add", "deleted.txt")
	gitCmd(t, d, "commit", "-qm", "track delete")
	gitCmd(t, d, "rm", "deleted.txt")
	gitCmd(t, d, "mv", "rename.txt", "renamed.txt")
	os.WriteFile(filepath.Join(d, "secret"), []byte("sensitive-no-read"), 0600)
	sub := filepath.Join(d, "sub")
	os.Mkdir(sub, 0700)
	one, e := run(t, sub, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil {
		t.Fatal(e)
	}
	two, e := run(t, sub, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil {
		t.Fatal(e)
	}
	if one != two || strings.Contains(one, "sensitive-no-read") {
		t.Fatal("non-deterministic output or untracked contents leaked")
	}
	r := decode(t, one)
	if r.SchemaVersion != reportSchemaVersion || r.EngineeringEvidence.Version != "brag-go-ast-v2" || r.EngineeringEvidence.Complexity.Status != "measured" || len(r.EngineeringEvidence.Complexity.Snapshots) == 0 || r.EngineeringEvidence.Complexity.Snapshots[len(r.EngineeringEvidence.Complexity.Snapshots)-1].Functions == 0 {
		t.Fatalf("Go declaration evidence missing: %+v", r.EngineeringEvidence)
	}
	if r.EngineeringEvidence.Security.Status != "measured" || r.EngineeringEvidence.Architecture.Status != "measured" || r.EngineeringEvidence.TestInventory.Status != "measured" {
		t.Fatal("implemented Phase 2 snapshot categories must report measured status")
	}
	if r.Scope != "branch" || len(r.History.IncludedCommitIDs) != 2 || r.Base.MergeBase != base {
		t.Fatalf("scope/boundary: %+v", r.History)
	}
	if r.SelectedIdentity.Email != "ada@example.test" {
		t.Fatalf("selected=%+v", r.SelectedContributor)
	}
	if r.NetSourceChange == nil || r.NetSourceChange.Status != "available" || r.NetSourceChange.Baseline != "pinned merge-base "+base {
		t.Fatalf("net source snapshot: %+v", r.NetSourceChange)
	}
	if r.SelectedContributor.AuthorCommitRatio.Numerator != 2 || r.SelectedContributor.AuthorCommitRatio.Denominator != 2 || r.RepositoryMeasures.CommitCount != 2 {
		t.Fatalf("developer/repository commit ratio=%+v repository=%+v", r.SelectedContributor.AuthorCommitRatio, r.RepositoryMeasures)
	}
	if r.WorkingTree.TrackedPathsChanged != 4 || !r.WorkingTree.UntrackedFilesPresent {
		t.Fatalf("overlay=%+v", r.WorkingTree)
	}
	if len(r.Contributors) != 2 || r.Contributors[0].CoauthorCommits+r.Contributors[1].CoauthorCommits != 1 {
		t.Fatalf("attribution=%+v", r.Contributors)
	}
}
func TestUpstreamAndExplicitBaseAndEmptyScopes(t *testing.T) {
	d, base := mkRepo(t)
	gitCmd(t, d, "switch", "-qc", "feature")
	os.WriteFile(filepath.Join(d, "x"), []byte("x\n"), 0600)
	gitCmd(t, d, "add", "x")
	gitCmd(t, d, "commit", "-qm", "feature")
	gitCmd(t, d, "branch", "--set-upstream-to=main", "feature")
	out, e := run(t, d)
	if e != nil {
		t.Fatal(e)
	}
	r := decode(t, out)
	if r.Base.Provenance != "configured upstream" || r.Base.MergeBase != base || len(r.History.IncludedCommitIDs) != 1 {
		t.Fatalf("upstream: %+v", r)
	}
	empty, e := run(t, d, "--base", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	if len(decode(t, empty).History.IncludedCommitIDs) != 0 {
		t.Fatal("expected empty branch scope")
	}
	all, e := run(t, d, "all")
	if e != nil {
		t.Fatal(e)
	}
	allReport := decode(t, all)
	if allReport.ToolVersion != "v0.1.0-rc.1" {
		t.Fatalf("tool_version = %q, want release v0.1.0-rc.1", allReport.ToolVersion)
	}
	if len(allReport.History.IncludedCommitIDs) != 2 {
		t.Fatal("all scope not reachable HEAD history")
	}
	for _, key := range []string{"complexity", "tests_added", "all_tests_added", "documentation", "generated_documentation", "architecture", "security"} {
		if _, conflict := allReport.Unavailable[key]; conflict {
			t.Errorf("legacy unavailable map conflicts with engineering_evidence.%s", key)
		}
	}
	if allReport.EngineeringEvidence.Architecture.Status != "measured" || allReport.EngineeringEvidence.Security.Status != "measured" || allReport.EngineeringEvidence.Duplication.Status != "unavailable" {
		t.Fatal("static evidence categories or explicit duplication status missing")
	}
	if allReport.Unavailable["net_source_change"].Status != "unavailable" || allReport.Unavailable["net_source_change"].Reason != "no selected snapshot baseline for all scope" {
		t.Fatal("all net source baseline must be unavailable")
	}
	for _, category := range []string{"pull_requests", "code_reviews", "test_coverage", "test_effectiveness", "reliability", "maintainability", "duplication"} {
		if allReport.Unavailable[category].Status != "unavailable" || allReport.Unavailable[category].Reason == "" {
			t.Errorf("missing unavailable evidence for %s", category)
		}
	}
	d2 := t.TempDir()
	gitCmd(t, d2, "init", "-q")
	unborn, ue := run(t, d2, "all")
	if ue != nil || len(decode(t, unborn).History.IncludedCommitIDs) != 0 {
		t.Fatalf("unborn empty all scope: %v", ue)
	}
	unbornReport := decode(t, unborn)
	if unbornReport.EngineeringEvidence.TestInventory.Status != "unavailable" || unbornReport.EngineeringEvidence.Documentation.Status != "unavailable" || unbornReport.EngineeringEvidence.TestInventory.SelectedTestsAddedRatio.Status != "unavailable" {
		t.Fatal("empty repository evidence must have explicit unavailable statuses")
	}
	d2, _ = mkRepo(t)
	if _, e = run(t, d2); e == nil {
		t.Fatal("missing upstream/base should fail")
	}
	emptyAll, e := run(t, d2, "all")
	if e != nil || len(decode(t, emptyAll).History.IncludedCommitIDs) != 1 {
		t.Fatalf("empty repository all scope: %v", e)
	}
}
func TestUnknownSelectedIdentityDoesNotAttributeHistoryOrWorktree(t *testing.T) {
	d, _ := mkRepo(t)
	if err := os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\nfunc Unattributed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "untracked.go"), []byte("package app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, d, "all", "--selected", "Unknown=unknown@example.test")
	if err != nil {
		t.Fatal(err)
	}
	r := decode(t, out)
	if !r.SelectedIdentity.Available || r.SelectedIdentity.Email != "unknown@example.test" || r.SelectedContributor == nil {
		t.Fatalf("explicit unknown identity was not preserved: %+v", r.SelectedIdentity)
	}
	if r.RepositoryMeasures.CommitCount != 1 || r.SelectedContributor.AuthorCommits != 0 || r.SelectedContributor.CoauthorCommits != 0 || r.SelectedContributor.AuthorCommitRatio.Numerator != 0 || r.SelectedContributor.AuthorCommitRatio.Denominator != 1 {
		t.Fatalf("unknown identity received fabricated history: selected=%+v repository=%+v", r.SelectedContributor, r.RepositoryMeasures)
	}
	for _, contributor := range r.Contributors {
		if strings.EqualFold(contributor.Email, "unknown@example.test") {
			t.Fatalf("unknown identity was inserted into history contributors: %+v", contributor)
		}
	}
	if r.SelectedContributor.CumulativeChurn != (Counts{}) || r.WorkingTree.Counts.LinesAdded == 0 || !r.WorkingTree.UntrackedFilesPresent {
		t.Fatalf("uncommitted changes were attributed or hidden: selected=%+v overlay=%+v", r.SelectedContributor, r.WorkingTree)
	}
}

func TestEmailCanonicalizationAndStateIdentity(t *testing.T) {
	d, base := mkRepo(t)
	gitCmd(t, d, "-c", "user.name=Zed", "-c", "user.email=ada@example.test", "commit", "--allow-empty", "-qm", "second name")
	out, e := run(t, d, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil {
		t.Fatal(e)
	}
	r := decode(t, out)
	if len(r.Contributors) != 1 || r.SelectedContributor.AuthorCommits != 1 {
		t.Fatalf("email identity did not collapse: %+v selected=%+v", r.Contributors, r.SelectedContributor)
	}
	first := r.WorkingTreeStateID
	again, e := run(t, d, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil || decode(t, again).WorkingTreeStateID != first {
		t.Fatal("unchanged worktree identity changed")
	}
	os.WriteFile(filepath.Join(d, "app.go"), []byte("package app\\nfunc X() {}\\n"), 0600)
	changed, e := run(t, d, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil || decode(t, changed).WorkingTreeStateID == first {
		t.Fatal("tracked change did not change state identity")
	}
}
func TestIgnoredFilesAreExcludedFromUntrackedObservation(t *testing.T) {
	d, _ := mkRepo(t)
	os.WriteFile(filepath.Join(d, ".gitignore"), []byte("ignored-secret\n"), 0600)
	gitCmd(t, d, "add", ".gitignore")
	gitCmd(t, d, "commit", "-qm", "ignore local file")
	os.WriteFile(filepath.Join(d, "ignored-secret"), []byte("must-not-be-read"), 0600)
	out, err := run(t, d, "all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-be-read") || decode(t, out).WorkingTree.UntrackedFilesPresent {
		t.Fatal("ignored files should not be read or reported as untracked")
	}
}

func TestUntrackedDefaultConfigIsNotReadAndAllExcludesUnreachable(t *testing.T) {
	d, _ := mkRepo(t)
	gitCmd(t, d, "switch", "-qc", "abandoned")
	os.WriteFile(filepath.Join(d, "only-abandoned"), []byte("x\\n"), 0600)
	gitCmd(t, d, "add", "only-abandoned")
	gitCmd(t, d, "commit", "-qm", "abandoned")
	gitCmd(t, d, "switch", "--detach", "HEAD~1")
	os.WriteFile(filepath.Join(d, ".brag.json"), []byte("PRIVATE invalid config marker"), 0600)
	out, e := run(t, d, "all")
	if e != nil || strings.Contains(out, "PRIVATE") {
		t.Fatalf("untracked default config read/disclosed: %v %s", e, out)
	}
	r := decode(t, out)
	if len(r.History.IncludedCommitIDs) != 1 || !r.WorkingTree.UntrackedFilesPresent {
		t.Fatalf("all scope/untracked: %+v", r.History)
	}
}
func TestMergeIdentityConfigAliasAndMissingIdentity(t *testing.T) {
	d, base := mkRepo(t)
	gitCmd(t, d, "switch", "-qc", "feature")
	os.WriteFile(filepath.Join(d, "f"), []byte("feature\n"), 0600)
	gitCmd(t, d, "add", "f")
	gitCmd(t, d, "commit", "-qm", "feature")
	gitCmd(t, d, "switch", "-qc", "side", base)
	os.WriteFile(filepath.Join(d, "s"), []byte("side\n"), 0600)
	gitCmd(t, d, "add", "s")
	gitCmd(t, d, "commit", "-qm", "side")
	gitCmd(t, d, "switch", "feature")
	gitCmd(t, d, "merge", "--no-ff", "side", "-m", "merge")
	gitCmd(t, d, "config", "user.name", "Canonical")
	gitCmd(t, d, "config", "user.email", "canonical@example.test")
	cfg := `{"version":1,"identities":[{"name":"Canonical","email":"canonical@example.test","available":true}],"aliases":{"ada@example.test":"canonical@example.test"}}`
	os.WriteFile(filepath.Join(d, ".brag.json"), []byte(cfg), 0600)
	gitCmd(t, d, "add", ".brag.json")
	out, e := run(t, d, "--base", base, "--selected", "Ada=ada@example.test")
	if e != nil {
		t.Fatal(e)
	}
	r := decode(t, out)
	if len(r.History.IncludedCommitIDs) != 3 {
		t.Fatalf("merge history ids: %d", len(r.History.IncludedCommitIDs))
	}
	if !strings.Contains(r.Measures.CommitMethod, "may omit merge changes") {
		t.Fatalf("merge diff limitation not disclosed: %s", r.Measures.CommitMethod)
	}
	if r.SelectedIdentity.Email != "canonical@example.test" {
		t.Fatalf("alias not applied: %+v", r.SelectedIdentity)
	}
	if r.Attribution.AuthorCommits != 3 || r.Attribution.CoauthorCommits != 0 {
		t.Fatalf("commit attribution totals: %+v", r.Attribution)
	}
	d2, _ := mkRepo(t)
	gitCmd(t, d2, "config", "--unset", "user.name")
	gitCmd(t, d2, "config", "--unset", "user.email")
	out, e = run(t, d2, "all")
	if e != nil {
		t.Fatal(e)
	}
	m := decode(t, out)
	if m.SelectedIdentity.Available || m.SelectedContributor != nil {
		t.Fatal("missing identity should be unavailable")
	}
	if m.Unavailable["selected_contributor_metrics"].Status != "unavailable" {
		t.Fatal("missing selected metrics status")
	}
}
