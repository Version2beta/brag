package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoricalTestAttributionMergeIsPartial(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Dev")
	gitCmd(t, d, "config", "user.email", "dev@example.test")
	write := func(name string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(d, name), []byte("package p\nimport \"testing\"\nfunc Test"+strings.ToUpper(name[:1])+"(t *testing.T) {}\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("base_test.go")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "base")
	root := gitCmd(t, d, "rev-parse", "HEAD")
	gitCmd(t, d, "switch", "-qc", "side")
	write("side_test.go")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "side test")
	side := gitCmd(t, d, "rev-parse", "HEAD")
	gitCmd(t, d, "switch", "main")
	write("main_test.go")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "main test")
	mainCommit := gitCmd(t, d, "rev-parse", "HEAD")
	gitCmd(t, d, "merge", "--no-ff", "side", "-m", "merge tests")
	merge := gitCmd(t, d, "rev-parse", "HEAD")
	e := buildTestEvidence(d, nil, []string{root, side, mainCommit, merge}, identityInfo{Name: "Dev", Email: "dev@example.test", Available: true}, maps{exact: map[string]identityInfo{}, aliases: map[string]string{}})
	if e.HistoricalAttribution.Status != "partial" || e.HistoricalAttribution.Count != 3 || e.SelectedTestsAddedRatio.Status != "partial" || e.AllTestsAddedRatio.Denominator != 4 {
		t.Fatalf("merge attribution must disclose exclusion: %+v", e)
	}
}

func TestHistoricalTestAttributionReportsInjectedCommitCapAndScopedRatio(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q")
	gitCmd(t, d, "config", "user.name", "Dev")
	gitCmd(t, d, "config", "user.email", "dev@example.test")
	gitCmd(t, d, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(d, "app_test.go"), []byte("package p\nimport \"testing\"\nfunc TestPeer(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "peer test")
	if err := os.WriteFile(filepath.Join(d, "app_test.go"), []byte("package p\nimport \"testing\"\nfunc TestPeer(t *testing.T) {}\nfunc TestDev(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", "app_test.go")
	gitCmd(t, d, "commit", "-qm", "selected test")
	if err := os.WriteFile(filepath.Join(d, "history.txt"), []byte("third commit"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", "history.txt")
	gitCmd(t, d, "commit", "-qm", "history only")
	commits := strings.Fields(gitCmd(t, d, "rev-list", "--reverse", "HEAD"))
	selected := identityInfo{Name: "Dev", Email: "dev@example.test", Available: true}
	e := applyHistoricalTestAttributionWithLimit(testEvidence{HistoricalAttribution: evidenceMetric{}, SelectedTestsAddedRatio: attributedRatio{}, AllTestsAddedRatio: attributedRatio{}}, d, commits, selected, maps{exact: map[string]identityInfo{}, aliases: map[string]string{}}, 2)
	if e.HistoricalAttribution.Status != "partial" || !strings.Contains(e.HistoricalAttribution.Units, "2 of 3 included commits attempted") {
		t.Fatalf("analysis completeness missing: %+v", e.HistoricalAttribution)
	}
	if e.SelectedTestsAddedRatio.Numerator == nil || *e.SelectedTestsAddedRatio.Numerator != 1 || e.SelectedTestsAddedRatio.Denominator != 2 || e.SelectedTestsAddedRatio.Status != "partial" {
		t.Fatalf("selected-test ratio must include only selected-author additions: %+v", e.SelectedTestsAddedRatio)
	}
	if e.AllTestsAddedRatio.Numerator == nil || *e.AllTestsAddedRatio.Numerator != 2 || e.AllTestsAddedRatio.Denominator != 2 || e.AllTestsAddedRatio.Status != "partial" {
		t.Fatalf("all-test ratio must use analyzed commits and all test additions: %+v", e.AllTestsAddedRatio)
	}
	if len(e.HistoricalAttribution.Warnings) == 0 || !strings.Contains(e.HistoricalAttribution.Warnings[0], "omitted 1") {
		t.Fatalf("omitted commit count missing: %+v", e.HistoricalAttribution)
	}
}
