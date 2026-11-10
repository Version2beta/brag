package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllScopeComparisonUsesSameCurrentMutationIDs(t *testing.T) {
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
		Comparison    testContributionComparison `json:"test_contribution_comparison"`
	}
	if err = json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 6 {
		t.Fatalf("schema=%d", report.SchemaVersion)
	}
	if report.Comparison.SourceIdentity == "" || len(report.Comparison.MutationIDs) != 1 || report.Comparison.MatrixArtifact == "" || len(report.Comparison.MatrixSHA256) != 64 {
		t.Fatalf("comparison identities/mutations: %+v", report.Comparison)
	}
	if len(report.Comparison.Cohorts) != 2 || report.Comparison.Cohorts[0].SelectedTests != 1 || report.Comparison.Cohorts[1].SelectedTests != 1 {
		t.Fatalf("cohorts: %+v", report.Comparison.Cohorts)
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
	if matrix.SourceIdentity != report.Comparison.SourceIdentity || len(matrix.MutationIDs) != len(report.Comparison.MutationIDs) || len(matrix.Pairs) != 2 || len(matrix.Controls) != 2 {
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
