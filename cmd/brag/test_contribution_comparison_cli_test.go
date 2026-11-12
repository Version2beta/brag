package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllScopeComparisonDoesNotCreditUncommittedRestoredTestDefinition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cohort CLI mutation and selector execution in short mode")
	}
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Ada")
	gitFixture(t, root, "config", "user.email", "ada@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/cohorts\ngo 1.22\n")
	write("app.go", "package cohorts\nfunc Predicate() bool { return true }\n")
	declarationA := "func TestFoo(t *testing.T) { if !Predicate() { t.Fatal(\"changed\") } }"
	declarationB := "func TestFoo(t *testing.T) { if Predicate() { t.Fatal(\"changed\") } }"
	write("app_test.go", "package cohorts\nimport \"testing\"\n"+declarationA+"\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "Ada declares TestFoo A")
	testPeer := "func TestPeer(t *testing.T) {}"
	testShared := "func TestShared(t *testing.T) {}"
	write("app_test.go", "package cohorts\nimport \"testing\"\n"+declarationB+"\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "Peer modifies TestFoo to B")
	write("app_test.go", "package cohorts\nimport \"testing\"\n"+declarationB+"\n"+testPeer+"\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "Peer adds TestPeer")
	write("app_test.go", "package cohorts\nimport \"testing\"\n"+declarationB+"\n"+testPeer+"\n"+testShared+"\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-m", "Peer and Ada add TestShared", "-m", "Co-authored-by: Ada <ada@example.test>")
	write("app_test.go", "package cohorts\nimport \"testing\"\n"+declarationA+"\n"+testPeer+"\n"+testShared+"\n")

	artifacts := privateTempDir(t)
	output, err := run(t, root, "all", "--selected", "Ada=ada@example.test", "--mutation-tests", "--test-contributions", "--mutation-limit", "1", "--dynamic-artifacts", artifacts, "--dynamic-timeout", "20s", "--dynamic-total-timeout", "2m", "--contribution-timeout", "20s", "--contribution-total-timeout", "3m")
	if err != nil {
		t.Fatalf("brag all comparison: %v output=%s", err, output)
	}
	var report struct {
		Comparison testContributionComparison `json:"test_contribution_comparison"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	c := report.Comparison
	if len(c.Cohorts) != 2 || c.Cohorts[0].EligibleTests != 0 || c.Cohorts[1].EligibleTests != 1 || c.Cohorts[1].SelectedTests != 1 {
		t.Fatalf("uncommitted A inherited historical attribution or committed Peer test lost its attribution: %+v", c)
	}
	if c.SharedTests == nil || *c.SharedTests != 1 || c.UnattributedTests == nil || *c.UnattributedTests != 1 || c.IndeterminateTests != 0 || c.EligibleTests+*c.SharedTests+*c.UnattributedTests != 3 {
		t.Fatalf("eligible/shared/unattributed/indeterminate counts do not account for all current tests: %+v", c)
	}
}

func TestAllScopeComparisonIgnoresUnrelatedNestedModuleHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cohort CLI mutation and selector execution in short mode")
	}
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Ada")
	gitFixture(t, root, "config", "user.email", "ada@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/root\ngo 1.22\n")
	write("app.go", "package root\nfunc Predicate() bool { return true }\n")
	write("app_test.go", "package root\nimport \"testing\"\nfunc TestRoot(t *testing.T) { if !Predicate() { t.Fatal(\"changed\") } }\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "Ada adds root test")
	write("nested/go.mod", "module example.test/nested\ngo 1.22\n")
	write("nested/nested_test.go", "package nested\nimport \"testing\"\nfunc TestNested(t *testing.T) {}\n")
	gitFixture(t, root, "add", "nested")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "Peer adds nested-module test")

	artifacts := privateTempDir(t)
	output, err := run(t, root, "all", "--selected", "Ada=ada@example.test", "--mutation-tests", "--test-contributions", "--mutation-limit", "1", "--dynamic-artifacts", artifacts, "--dynamic-timeout", "20s", "--dynamic-total-timeout", "2m", "--contribution-timeout", "20s", "--contribution-total-timeout", "3m")
	if err != nil {
		t.Fatalf("brag all comparison: %v output=%s", err, output)
	}
	var report struct {
		Comparison testContributionComparison `json:"test_contribution_comparison"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	c := report.Comparison
	if c.HistoryAttributionStatus != "measured" || c.EligibleTests != 1 || c.IndeterminateTests != 0 || c.SharedTests == nil || *c.SharedTests != 0 || c.UnattributedTests == nil || *c.UnattributedTests != 0 {
		t.Fatalf("unrelated nested-module history tainted root candidate attribution: %+v", c)
	}
	if len(c.Cohorts) != 2 || c.Cohorts[0].EligibleTests != 1 || c.Cohorts[0].SelectedTests != 1 || c.Cohorts[1].EligibleTests != 0 {
		t.Fatalf("root candidate cohort mismatch: %+v", c.Cohorts)
	}
}

func TestAllScopeComparisonMarksMergeAffectedCandidateIndeterminate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cohort CLI mutation execution in short mode")
	}
	root := t.TempDir()
	gitFixture(t, root, "init", "-b", "main")
	gitFixture(t, root, "config", "user.name", "Ada")
	gitFixture(t, root, "config", "user.email", "ada@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/merge-candidate\ngo 1.22\n")
	write("app.go", "package mergecandidate\nfunc Predicate() bool { return true }\n")
	baseTest := "package mergecandidate\nimport \"testing\"\nfunc TestRoot(t *testing.T) { if !Predicate() { t.Fatal(\"changed\") } }\n"
	write("app_test.go", baseTest)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "root baseline")
	gitFixture(t, root, "checkout", "-qb", "topic")
	write("app_test.go", baseTest+"func TestMerged(t *testing.T) {}\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "topic test")
	gitFixture(t, root, "checkout", "main")
	write("other.go", "package mergecandidate\n")
	gitFixture(t, root, "add", "other.go")
	gitFixture(t, root, "commit", "-qm", "main work")
	gitFixture(t, root, "merge", "--no-ff", "-m", "merge candidate test", "topic")
	artifacts := privateTempDir(t)
	output, err := run(t, root, "all", "--selected", "Ada=ada@example.test", "--mutation-tests", "--test-contributions", "--mutation-limit", "1", "--dynamic-artifacts", artifacts, "--dynamic-timeout", "20s", "--dynamic-total-timeout", "2m", "--contribution-timeout", "20s", "--contribution-total-timeout", "3m")
	if err != nil {
		t.Fatalf("brag all comparison: %v output=%s", err, output)
	}
	var report struct {
		Comparison testContributionComparison `json:"test_contribution_comparison"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	c := report.Comparison
	if c.HistoryAttributionStatus != "partial" || c.Status != "partial" || c.EligibleTests != 0 || c.IndeterminateTests != 2 || c.SharedTests != nil || c.UnattributedTests != nil {
		t.Fatalf("candidate-affecting merge did not withhold attribution: %+v", c)
	}
	if len(c.Cohorts) != 2 || c.Cohorts[0].EligibleTests != 0 || c.Cohorts[1].EligibleTests != 0 {
		t.Fatalf("merge populated a current cohort: %+v", c.Cohorts)
	}
}

func TestAllScopeComparisonUsesSameCurrentMutationIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cohort CLI mutation and selector execution in short mode")
	}
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Ada")
	gitFixture(t, root, "config", "user.email", "ada@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/cohorts\ngo 1.22\n")
	write("app.go", "package cohorts\nfunc Predicate() bool { return true }\n")
	write("app_test.go", "package cohorts\nimport \"testing\"\nfunc TestSelected(t *testing.T) { if !Predicate() { t.Fatal(\"changed\") } }\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "selected author test")
	write("app_test.go", "package cohorts\nimport \"testing\"\nfunc TestSelected(t *testing.T) { if !Predicate() { t.Fatal(\"changed\") } }\nfunc TestPeer(t *testing.T) {}\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "peer author test")
	artifacts := privateTempDir(t)
	output, err := run(t, root, "all", "--selected", "Ada=ada@example.test", "--mutation-tests", "--test-contributions", "--mutation-limit", "1", "--dynamic-artifacts", artifacts, "--dynamic-timeout", "20s", "--dynamic-total-timeout", "2m", "--contribution-timeout", "20s", "--contribution-total-timeout", "3m")
	if err != nil {
		t.Fatalf("brag all comparison: %v output=%s", err, output)
	}
	var report struct {
		SchemaVersion int                        `json:"schema_version"`
		MutationTests mutationReport             `json:"mutation_tests"`
		Comparison    testContributionComparison `json:"test_contribution_comparison"`
	}
	if err = json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 7 {
		t.Fatalf("schema=%d", report.SchemaVersion)
	}
	if report.Comparison.SourceIdentity == "" || len(report.Comparison.MutationIDs) != 1 || report.Comparison.MatrixArtifact == "" || len(report.Comparison.MatrixSHA256) != 64 {
		t.Fatalf("comparison identities/mutations: %+v", report.Comparison)
	}
	if report.Comparison.Paired.Status == "unavailable" || report.Comparison.Paired.CompleteMutationDenominator == nil || *report.Comparison.Paired.CompleteMutationDenominator != 1 || len(report.Comparison.Paired.PairedMutationIDs) != 1 {
		t.Fatalf("paired comparison: %+v", report.Comparison.Paired)
	}
	if len(report.Comparison.Cohorts) != 2 || report.Comparison.Cohorts[0].EligibleTests != 1 || report.Comparison.Cohorts[0].SelectedTests != 1 || report.Comparison.Cohorts[1].EligibleTests != 1 || report.Comparison.Cohorts[1].SelectedTests != 1 {
		t.Fatalf("cohorts: %+v", report.Comparison.Cohorts)
	}
	if report.Comparison.UnattributedTests == nil || report.Comparison.SharedTests == nil || report.Comparison.EligibleTests+*report.Comparison.SharedTests+*report.Comparison.UnattributedTests != 2 {
		t.Fatalf("complete attribution count invariant failed: %+v", report.Comparison)
	}
	paired := report.Comparison.Paired
	if paired.Status != "measured" || paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 1 || paired.CompleteMutationDenominatorUnit == "" || len(paired.EligibleMutationIDs) != 1 || len(paired.SelectedMutationIDs) != 1 || len(paired.PairedMutationIDs) != 1 || paired.PairedMutationIDs[0] != report.Comparison.MutationIDs[0] || len(paired.OmittedMutationIDs) != 0 || len(paired.SelectedOnlyDetected) != 1 || paired.SelectedOnlyDetected[0] != report.Comparison.MutationIDs[0] || len(paired.OtherOnlyDetected)+len(paired.BothDetected)+len(paired.NeitherDetected) != 0 {
		t.Fatalf("paired complete-case result: %+v", paired)
	}
	for _, cohort := range report.Comparison.Cohorts {
		if cohort.SelectedPairs != 1 || cohort.OmittedPairs != cohort.SelectedTests*len(report.Comparison.MutationIDs)-cohort.SelectedPairs {
			t.Fatalf("cohort pair denominator accounting: %+v", cohort)
		}
	}
	matrixBytes, err := os.ReadFile(filepath.Join(artifacts, filepath.Base(report.Comparison.MatrixArtifact)))
	if err != nil || hashBytes(matrixBytes) != report.Comparison.MatrixSHA256 {
		t.Fatalf("matrix evidence/hash: %v", err)
	}
	var matrix comparisonEvidence
	if err := json.Unmarshal(matrixBytes, &matrix); err != nil {
		t.Fatal(err)
	}
	if matrix.SourceIdentity != report.Comparison.SourceIdentity || len(report.MutationTests.Snapshots) != 1 || matrix.TestIdentity != report.MutationTests.Snapshots[0].TestIdentity || len(matrix.MutationIDs) != len(report.Comparison.MutationIDs) || len(matrix.Pairs) != 2 || len(matrix.Controls) != 2 {
		t.Fatalf("matrix provenance/outcomes missing: %+v", matrix)
	}
	for _, pair := range matrix.Pairs {
		if pair.EvidenceArtifact == "" || pair.EvidenceSHA256 == "" {
			t.Fatalf("pair evidence missing: %+v", pair)
		}
	}
	for _, control := range matrix.Controls {
		if control.EvidenceArtifact == "" || control.EvidenceSHA256 == "" {
			t.Fatalf("control evidence missing: %+v", control)
		}
	}
}
