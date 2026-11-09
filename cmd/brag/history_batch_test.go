package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitBatchReaderBoundsAndBinaryContents(t *testing.T) {
	dir, _ := mkRepo(t)
	binary := []byte{0, 1, 2, '\n', 0xff}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "blob.bin")
	gitCmd(t, dir, "commit", "-qm", "binary object")
	oid := gitCmd(t, dir, "rev-parse", "HEAD:blob.bin")
	reader, err := newGitBatchReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	kind, size, err := reader.Info(oid)
	if err != nil || kind != "blob" || size != len(binary) {
		t.Fatalf("object info = (%q, %d, %v)", kind, size, err)
	}
	if _, content, _, err := reader.Read(oid, -1); err == nil || content != nil {
		t.Fatalf("negative content limit was accepted: %v", err)
	}
	kind, content, gotSize, err := reader.Read(oid, size-1)
	if err != nil || kind != "blob" || gotSize != size || content != nil {
		t.Fatalf("oversized read = (%q, %v, %d, %v)", kind, content, gotSize, err)
	}
	kind, content, gotSize, err = reader.Read(oid, size)
	if err != nil || kind != "blob" || gotSize != size || !bytes.Equal(content, binary) {
		t.Fatalf("binary read = (%q, %v, %d, %v)", kind, content, gotSize, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGitBatchReaderMissingObjectDoesNotBreakProtocol(t *testing.T) {
	dir, _ := mkRepo(t)
	reader, err := newGitBatchReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.Info(strings.Repeat("0", 40)); err == nil {
		t.Fatal("missing object was reported as available")
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close after missing object: %v", err)
	}
	if _, _, err := reader.Info("valid-after-close"); err == nil {
		t.Fatal("closed reader accepted another request")
	}
}

func TestReadHistoryCommitObjectHonorsRemainingBudget(t *testing.T) {
	dir, head := mkRepo(t)
	reader, err := newGitBatchReaderForCommit(dir, head)
	if err != nil {
		t.Fatal(err)
	}
	object, size, err := readHistoryCommitObject(reader, head, 0)
	if err == nil || object != nil || size <= 0 {
		t.Fatalf("commit read exceeded remaining budget: object=%d size=%d err=%v", len(object), size, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close after rejected commit read: %v", err)
	}
}

func TestCollectHistoryMatchesPerCommitGitResults(t *testing.T) {
	dir, _ := mkRepo(t)
	gitCmd(t, dir, "switch", "-qc", "feature")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nfunc A() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "commit", "-qam", "feature change\n\nCo-authored-by: Grace <grace@example.test>\nCo-authored-by: grace <GRACE@example.test>")
	gitCmd(t, dir, "switch", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package app\nfunc Main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "main.go")
	gitCmd(t, dir, "-c", "user.name=Grace", "-c", "user.email=grace@example.test", "commit", "-qm", "main change")
	gitCmd(t, dir, "merge", "--no-ff", "-qm", "merge feature", "feature")
	gitCmd(t, dir, "mv", "rename.txt", "renamed.txt")
	gitCmd(t, dir, "commit", "-qm", "rename")
	if err := os.WriteFile(filepath.Join(dir, "odd\nname.go"), []byte("package app\nfunc Odd() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0, 1, 2, 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "--all")
	gitCmd(t, dir, "commit", "-qm", "unusual paths and binary")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	commits := strings.Split(gitCmd(t, dir, "rev-list", "--reverse", head), "\n")
	identities := maps{exact: map[string]identityInfo{}, aliases: map[string]string{}}
	got, err := collectHistory(dir, head, commits, identities)
	if err != nil {
		t.Fatal(err)
	}
	want := referenceHistoryTotals(t, dir, commits, identities)
	assertHistoryTotalsEqual(t, got, want)
}

func TestCollectHistoryFallsBackWhenBatchCommandUnavailable(t *testing.T) {
	dir, _ := mkRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nfunc Work() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "commit", "-qam", "add work")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	commits := strings.Split(gitCmd(t, dir, "rev-list", "--reverse", head), "\n")
	identities := maps{exact: map[string]identityInfo{}, aliases: map[string]string{}}
	want := referenceHistoryTotals(t, dir, commits, identities)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	wrapper := "#!/bin/sh\nif [ \"$3\" = cat-file ] && [ \"$4\" = --batch-command ]; then\n  echo 'unsupported batch command' >&2\n  exit 129\nfi\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := collectHistory(dir, head, commits, identities)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryTotalsEqual(t, got, want)
}

func assertHistoryTotalsEqual(t *testing.T, got, want historyTotals) {
	t.Helper()
	if got.churn != want.churn || got.authored != want.authored || got.coauthored != want.coauthored || len(got.people) != len(want.people) {
		t.Fatalf("history totals differ: got=%+v want=%+v", got, want)
	}
	for key, expected := range want.people {
		actual := got.people[key]
		if actual == nil || actual.Name != expected.Name || actual.Email != expected.Email || actual.AuthorCommits != expected.AuthorCommits || actual.CoauthorCommits != expected.CoauthorCommits || actual.LinesAdded != expected.LinesAdded || actual.LinesDeleted != expected.LinesDeleted || actual.SourceLinesAdded != expected.SourceLinesAdded || actual.SourceLinesDeleted != expected.SourceLinesDeleted {
			t.Fatalf("person %q differs: got=%+v want=%+v", key, actual, expected)
		}
	}
}

func referenceHistoryTotals(t *testing.T, root string, commits []string, identities maps) historyTotals {
	t.Helper()
	out := historyTotals{people: map[string]*Person{}}
	for _, commit := range commits {
		meta, err := runGit(root, "show", "-s", "--format=%an%x00%ae%x00%B", commit)
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.SplitN(string(meta), "\x00", 3)
		if len(fields) < 3 {
			continue
		}
		author := resolve(identityInfo{Name: fields[0], Email: fields[1], Available: true}, identities)
		person := getPerson(out.people, author)
		person.AuthorCommits++
		out.authored++
		seen := map[string]bool{}
		for _, coauthor := range parseCoauthors(fields[2]) {
			coauthor = resolve(coauthor, identities)
			key := idKey(coauthor)
			if seen[key] {
				continue
			}
			seen[key] = true
			getPerson(out.people, coauthor).CoauthorCommits++
			out.coauthored++
		}
		diff, err := runGit(root, "diff-tree", "--no-ext-diff", "--no-textconv", "--root", "--no-commit-id", "--numstat", "-r", "-M", commit)
		if err != nil {
			t.Fatal(err)
		}
		change := sum(diff)
		out.churn = add(out.churn, change)
		person.LinesAdded += change.LinesAdded
		person.LinesDeleted += change.LinesDeleted
		person.SourceLinesAdded += change.SourceLinesAdded
		person.SourceLinesDeleted += change.SourceLinesDeleted
	}
	return out
}

func TestCollectHistoryEmptyList(t *testing.T) {
	got, err := collectHistory(t.TempDir(), "", nil, maps{exact: map[string]identityInfo{}, aliases: map[string]string{}})
	if err != nil || len(got.people) != 0 || got.churn != (Counts{}) {
		t.Fatalf("empty history = %+v, %v", got, err)
	}
}

func TestHistoricalBatchAttributionPreservesAuthorsAndByteCaps(t *testing.T) {
	dir, _ := mkRepo(t)
	writeTest := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "app_test.go"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeTest("package app\nimport \"testing\"\nfunc TestOne(t *testing.T) { t.Helper() }\n")
	gitCmd(t, dir, "add", "app_test.go")
	gitCmd(t, dir, "commit", "-qm", "first test")
	writeTest("package app\nimport \"testing\"\nfunc TestOne(t *testing.T) { t.Helper(); t.Log(\"updated\") }\nfunc TestTwo(t *testing.T) { t.Helper() }\n")
	gitCmd(t, dir, "add", "app_test.go")
	gitCmd(t, dir, "-c", "user.name=Grace", "-c", "user.email=grace@example.test", "commit", "-qm", "second test\n\nCo-authored-by: Ada <ada@example.test>")
	writeTest("package app\nimport \"testing\"\nfunc TestOne(t *testing.T) { t.Helper(); t.Log(\"modified body\") }\nfunc TestTwo(t *testing.T) { t.Helper() }\n")
	gitCmd(t, dir, "add", "app_test.go")
	gitCmd(t, dir, "-c", "user.name=Grace", "-c", "user.email=grace@example.test", "commit", "-qm", "modify test body")
	commits := strings.Split(gitCmd(t, dir, "rev-list", "--reverse", "HEAD"), "\n")
	e := testEvidence{Status: "measured", HistoricalAttribution: evidenceMetric{}, SelectedTestsAddedRatio: attributedRatio{}, AllTestsAddedRatio: attributedRatio{}}
	e = applyHistoricalTestAttribution(e, dir, commits, identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}, maps{exact: map[string]identityInfo{}, aliases: map[string]string{}})
	if e.HistoricalAttribution.Status != "measured" || e.HistoricalAttribution.Count != 2 || e.SelectedTestsAddedRatio.Status != "measured" || e.SelectedTestsAddedRatio.Numerator == nil || *e.SelectedTestsAddedRatio.Numerator != 2 || e.SelectedTestsAddedRatio.Denominator != 2 || e.AllTestsAddedRatio.Denominator != len(commits) {
		t.Fatalf("historical attribution changed: %+v", e)
	}
}
