package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestContributionTestsUsePackageFileAndDeclarationHash(t *testing.T) {
	entries := []snapshotEntry{{path: "go.mod", data: []byte("module example.test/project\ngo 1.22\n")}, {path: "pkg/a_test.go", data: []byte("package pkg\nimport \"testing\"\nfunc TestSame(t *testing.T) { t.Run(\"nested\", func(t *testing.T){}) }\n")}, {path: "pkg/b_test.go", data: []byte("package pkg\nimport \"testing\"\nfunc TestSame(t *testing.T) {}\n")}}
	got := contributionTests(entries)
	if len(got) != 2 {
		t.Fatalf("tests=%d", len(got))
	}
	if got[0].ID == got[1].ID || got[0].DeclarationSHA256 == got[1].DeclarationSHA256 {
		t.Fatal("distinct declarations conflated")
	}
	for _, test := range got {
		if test.Package != "example.test/project/pkg" {
			t.Fatalf("package=%s", test.Package)
		}
	}
}

func TestContributionMatrixBoundsAndStableDenominators(t *testing.T) {
	tests := []contributionTest{{ID: "a"}, {ID: "b"}}
	matrix, omitted := contributionMatrix(tests, []string{"m1", "m2"}, 3)
	if len(matrix) != 3 || omitted != 1 {
		t.Fatalf("matrix=%d omitted=%d", len(matrix), omitted)
	}
	if matrix[0].TestID != "a" || matrix[0].MutationID != "m1" || matrix[2].TestID != "b" {
		t.Fatalf("ordering: %+v", matrix)
	}
	complete := []contributionPair{{TestID: "a", MutationID: "m", Status: "detected"}, {TestID: "b", MutationID: "m", Status: "not_detected"}}
	if !confirmedUniqueMutation(tests, complete, "m") {
		t.Fatal("complete one-detector matrix not unique")
	}
	if confirmedUniqueMutation(tests, complete[:1], "m") {
		t.Fatal("incomplete matrix established uniqueness")
	}
	if got := stableContributionStatus([]string{"detected", "not_detected"}, 2); got != "unstable" {
		t.Fatalf("repeat status=%s", got)
	}
	if got := stableContributionStatus([]string{"detected"}, 2); got != "inconclusive" {
		t.Fatalf("incomplete repeat status=%s", got)
	}
}

func TestContributionCalculationsFailClosedAndDeriveSets(t *testing.T) {
	tests := []contributionTest{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	pairs := []contributionPair{
		{Population: "current", TestID: "a", MutationID: "unique", Status: "detected"},
		{Population: "current", TestID: "b", MutationID: "unique", Status: "not_detected"},
		{Population: "current", TestID: "c", MutationID: "unique", Status: "not_detected"},
		{Population: "current", TestID: "a", MutationID: "overlap", Status: "detected"},
		{Population: "current", TestID: "b", MutationID: "overlap", Status: "detected"},
		{Population: "current", TestID: "c", MutationID: "overlap", Status: "not_detected"},
		{Population: "current", TestID: "a", MutationID: "missing", Status: "detected"},
		{Population: "current", TestID: "b", MutationID: "missing", Status: "inconclusive"},
		{Population: "current", TestID: "c", MutationID: "inconclusive", Status: "detected"},
		{Population: "current", TestID: "a", MutationID: "suite-only", Status: "not_detected"},
		{Population: "current", TestID: "b", MutationID: "suite-only", Status: "not_detected"},
		{Population: "current", TestID: "c", MutationID: "suite-only", Status: "not_detected"},
		{Population: "baseline", TestID: "a", MutationID: "ignored", Status: "detected"},
	}
	ids, counts, testCounts, unique := summarizeContributionDetections(tests, pairs, []string{"unique", "overlap", "missing", "inconclusive", "suite-only"})
	wantCounts := map[string]int{"unique": 1, "overlap": 2, "missing": 1, "inconclusive": 1}
	wantTestCounts := map[string]int{"a": 3, "b": 1, "c": 1}
	if strings.Join(ids, ",") != "unique,overlap,missing,inconclusive" || !reflect.DeepEqual(counts, wantCounts) || !reflect.DeepEqual(testCounts, wantTestCounts) || strings.Join(unique, ",") != "unique" {
		t.Fatalf("detections ids=%v counts=%v testCounts=%v unique=%v", ids, counts, testCounts, unique)
	}
	mutationIDs := []string{"unique", "overlap", "missing", "inconclusive", "suite-only"}
	marginals, overlaps := contributionSetDerivedStats(tests, pairs, mutationIDs)
	idsAgain, countsAgain, testCountsAgain, uniqueAgain := summarizeContributionDetections(tests, pairs, mutationIDs)
	marginalsAgain, overlapsAgain := contributionSetDerivedStats(tests, pairs, mutationIDs)
	if !reflect.DeepEqual([]any{ids, counts, testCounts, unique, marginals, overlaps}, []any{idsAgain, countsAgain, testCountsAgain, uniqueAgain, marginalsAgain, overlapsAgain}) {
		t.Fatal("same ordered calculation inputs produced different results")
	}
	if marginals["a"] != 1 || len(marginals) != 1 || strings.Join(overlaps, ",") != "overlap:a,b" {
		t.Fatalf("marginals=%v overlaps=%v", marginals, overlaps)
	}
	if confirmedUniqueMutation(tests, pairs[:2], "unique") {
		t.Fatal("missing pair established uniqueness")
	}
}

func TestContributionSuiteInteractionClassifications(t *testing.T) {
	tests := []contributionTest{{ID: "t1", Package: "p", Name: "TestOne"}, {ID: "t2", Package: "p", Name: "TestTwo"}}
	controls := map[string]string{"t1": "not_detected", "t2": "not_detected"}
	pairs := []contributionPair{{Population: "current", TestID: "t1", MutationID: "isolated", Status: "detected"}}
	suite := mutationOutcomeArtifact{Control: mutationExecution{Tests: []dynamicTest{{Package: "p", Name: "TestOne", Outcome: "fail"}}}, Mutants: []mutationOutcome{{ID: "suite-only", Status: "killed"}, {ID: "isolated", Status: "survived"}}}
	got := contributionSuiteInteractions(tests, controls, pairs, suite)
	want := []string{"t1:suite_control_differs_from_isolated_control", "suite-only:suite_killed_no_isolated_detector", "isolated:isolated_detector_suite_survived"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("interactions=%v want %v", got, want)
	}
}

func TestContributionSelectorDetectsOnlySelectedTestFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping isolated selector test-suite execution in short mode")
	}
	dir := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/p\ngo 1.22\n")
	write("app.go", "package p\nfunc A() bool{return true}\nfunc B() bool{return true}\n")
	write("app_test.go", "package p\nimport \"testing\"\nfunc TestOne(t *testing.T){t.Run(\"child\",func(t *testing.T){if !A(){t.Fatal(\"A\")}})}\nfunc TestTwo(t *testing.T){if !A(){t.Fatal(\"A\")}}\nfunc TestUnique(t *testing.T){if !B(){t.Fatal(\"B\")}}\nfunc TestNone(t *testing.T){}\nfunc TestSkip(t *testing.T){t.Skip(\"skip\")}\n")
	entries, _, err := snapshotEntriesForMutation(dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := contributionTests(entries)
	byName := map[string]contributionTest{}
	for _, test := range tests {
		byName[test.Name] = test
	}
	ctx := context.Background()
	for _, name := range []string{"TestOne", "TestTwo", "TestUnique", "TestNone", "TestSkip"} {
		copyDir, e := materializeMutationCopy(entries)
		if e != nil {
			t.Fatal(e)
		}
		status, _, _, _ := executeContributionTest(ctx, copyDir, byName[name], 20*time.Second, false)
		os.RemoveAll(copyDir)
		wantControl := "not_detected"
		if name == "TestSkip" {
			wantControl = "inconclusive"
		}
		if status != wantControl {
			t.Fatalf("control %s=%s want %s", name, status, wantControl)
		}
	}
	noMatch := byName["TestNone"]
	noMatch.Name = "TestMissing"
	copyDir, e := materializeMutationCopy(entries)
	if e != nil {
		t.Fatal(e)
	}
	noMatchStatus, _, _, _ := executeContributionTest(ctx, copyDir, noMatch, 20*time.Second, false)
	os.RemoveAll(copyDir)
	if noMatchStatus != "inconclusive" {
		t.Fatalf("no-match status=%s", noMatchStatus)
	}
	for _, name := range []string{"TestOne", "TestTwo", "TestUnique", "TestNone"} {
		copyDir, e := materializeMutationCopy(entries)
		if e != nil {
			t.Fatal(e)
		}
		source, _ := os.ReadFile(filepath.Join(copyDir, "app.go"))
		offset := strings.Index(string(source), "true")
		if name == "TestUnique" {
			offset = strings.LastIndex(string(source), "true")
		}
		site := mutationSite{Path: "app.go", Offset: offset, Before: "true", After: "false"}
		if e = applyMutation(copyDir, site); e != nil {
			t.Fatal(e)
		}
		status, _, _, _ := executeContributionTest(ctx, copyDir, byName[name], 20*time.Second, true)
		os.RemoveAll(copyDir)
		want := "detected"
		if name == "TestNone" {
			want = "not_detected"
		}
		if status != want {
			t.Fatalf("%s mutation status=%s want %s", name, status, want)
		}
	}
}
