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
