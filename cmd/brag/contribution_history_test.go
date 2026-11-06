package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContributionPackageIdentitySeparatesSameDefinition(t *testing.T) {
	src := []byte("package p\nimport \"testing\"\nfunc TestSame(t *testing.T){}\n")
	a := contributionTests([]snapshotEntry{{path: "go.mod", data: []byte("module example.test/a\ngo 1.22\n")}, {path: "p_test.go", data: src}})
	b := contributionTests([]snapshotEntry{{path: "go.mod", data: []byte("module example.test/b\ngo 1.22\n")}, {path: "p_test.go", data: src}})
	if len(a) != 1 || len(b) != 1 || a[0].Package == b[0].Package || a[0].ID == b[0].ID {
		t.Fatalf("package identities conflated: %+v %+v", a, b)
	}
}

func TestContributionDefinitionTransitions(t *testing.T) {
	base := map[string]contributionDefinition{"a_test.go/test/TestKeep": {Key: "a_test.go/test/TestKeep", Hash: "same"}, "a_test.go/test/TestModify": {Key: "a_test.go/test/TestModify", Hash: "old"}, "a_test.go/test/TestRemove": {Key: "a_test.go/test/TestRemove", Hash: "gone"}}
	current := map[string]contributionDefinition{"a_test.go/test/TestKeep": {Key: "a_test.go/test/TestKeep", Hash: "same"}, "a_test.go/test/TestModify": {Key: "a_test.go/test/TestModify", Hash: "new"}, "a_test.go/test/TestNew": {Key: "a_test.go/test/TestNew", Hash: "add"}}
	got := map[string]string{}
	for _, change := range compareContributionDefinitions(base, current) {
		got[pathBase(change.Key)] = change.Status
	}
	for name, want := range map[string]string{"TestKeep": "existing_unchanged", "TestModify": "modified", "TestRemove": "removed", "TestNew": "introduced"} {
		if got[name] != want {
			t.Errorf("%s=%s want %s", name, got[name], want)
		}
	}
}

func TestContributionHistoricalOverlayAndAuthorCredits(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Dev")
	gitFixture(t, root, "config", "user.email", "dev@example.test")
	writeFixture := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("go.mod", "module example.test/p\ngo 1.22\n")
	baseProd := "package p\nfunc Keep() bool { return true }\nfunc Gone() bool { return true }\n"
	baseTest := "package p\nimport \"testing\"\nfunc TestKeep(t *testing.T) { if !Keep(){t.Fatal()} }\nfunc TestModify(t *testing.T) { t.Log(\"baseline\") }\nfunc TestRemove(t *testing.T) { if !Gone(){t.Fatal()} }\n"
	writeFixture("app.go", baseProd)
	writeFixture("app_test.go", baseTest)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-m", "baseline")
	mergeBase := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	currentProd := "package p\nfunc Keep() bool { return true }\n"
	currentTest := "package p\nimport \"testing\"\nfunc TestKeep(t *testing.T) { if !Keep(){t.Fatal()} }\nfunc TestModify(t *testing.T) { t.Log(\"current\") }\nfunc TestNew(t *testing.T) {}\n"
	writeFixture("app.go", currentProd)
	writeFixture("app_test.go", currentTest)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "-c", "user.name=Other", "-c", "user.email=other@example.test", "commit", "-m", "update", "-m", "Co-authored-by: Ada <ada-old@example.test>")
	head := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	selectedSource := []snapshotEntry{{path: "app.go", mode: "100644", data: []byte(currentProd)}, {path: "go.mod", mode: "100644", data: []byte("module example.test/p\ngo 1.22\n")}}
	overlay, err := overlayBaselineTests(root, mergeBase, selectedSource)
	if err != nil {
		t.Fatal(err)
	}
	if mutationSourceIdentity(overlay, false) != mutationSourceIdentity(selectedSource, false) {
		t.Fatal("baseline overlay changed selected production source identity")
	}
	if mutationSourceIdentity(overlay, true) == mutationSourceIdentity([]snapshotEntry{{path: "app_test.go", mode: "100644", data: []byte(currentTest)}}, true) {
		t.Fatal("baseline tests not overlaid")
	}
	baseSites, _, _, _, err := mutationSites(overlay, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceSites, _, _, _, err := mutationSites(selectedSource, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !equalMutationIDs(baseSites, sourceSites) {
		t.Fatal("mutation IDs differ between populations")
	}
	commits := strings.Fields(string(gitFixture(t, root, "rev-list", "HEAD")))
	aliases := maps{aliases: map[string]string{"ada-old@example.test": "ada@example.test"}}
	selected := identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}
	credits, events, partial, reason := resolveContributionHistory(root, commits, selected, aliases)
	if partial {
		t.Fatalf("history partial: %s", reason)
	}
	statuses := map[string]map[string]bool{}
	for _, event := range events {
		if statuses[pathBase(event.Key)] == nil {
			statuses[pathBase(event.Key)] = map[string]bool{}
		}
		statuses[pathBase(event.Key)][event.Status] = true
	}
	for name, want := range map[string]string{"TestModify": "modified", "TestNew": "introduced", "TestRemove": "removed"} {
		if !statuses[name][want] {
			t.Errorf("history %s missing %s: %v", name, want, statuses[name])
		}
	}
	if !credits["selected"]["example.test/p\x00app_test.go/test/TestNew\x00"+hashContributionDecl(t, currentTest, "TestNew")] {
		t.Fatal("coauthor alias not credited")
	}
	writeFixture("app_test.go", baseTest)
	headVersions, err := trackedHeadContributionVersions(context.Background(), root, head)
	if err != nil {
		t.Fatal(err)
	}
	baselineDefs := contributionDefinitions([]snapshotEntry{{path: "app_test.go", mode: "100644", data: []byte(baseTest)}, {path: "go.mod", mode: "100644", data: []byte("module example.test/p\ngo 1.22\n")}})
	oldModify := baselineDefs["example.test/p\x00app_test.go/test/TestModify"]
	if headVersions[oldModify.Key+"\x00"+oldModify.Hash] {
		t.Fatal("uncommitted revert matched the HEAD definition")
	}
	uncommitted, err := countUncommittedContributionTests(context.Background(), root, head)
	if err != nil || uncommitted == 0 {
		t.Fatalf("uncommitted count=%d err=%v", uncommitted, err)
	}
}

func TestContributionHistoryMergeTopologySkipsResolutionDefinitions(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init", "-b", "main")
	gitFixture(t, root, "config", "user.name", "Fixture")
	gitFixture(t, root, "config", "user.email", "fixture@example.test")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/p\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "sample_test.go"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := "package p\nimport \"testing\"\nfunc TestBase(t *testing.T) {}\n"
	write(base)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "base")
	baseCommit := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	gitFixture(t, root, "checkout", "-qb", "topic")
	topic := base + "func TestTopic(t *testing.T) {}\n"
	write(topic)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "topic definition")
	topicCommit := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	gitFixture(t, root, "checkout", "main")
	// A non-test change makes the first-parent side of the eventual merge distinct.
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte("package p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "main work")
	gitFixture(t, root, "merge", "--no-ff", "--no-commit", "topic")
	resolution := topic + "func TestMergeResolution(t *testing.T) {}\n"
	write(resolution)
	gitFixture(t, root, "add", "sample_test.go")
	gitFixture(t, root, "commit", "-qm", "merge with resolution-only definition")
	commits := strings.Fields(string(gitFixture(t, root, "rev-list", "--reverse", baseCommit+"..HEAD")))
	selected := identityInfo{Name: "Fixture", Email: "fixture@example.test", Available: true}
	credits, events, partial, reason := resolveContributionHistory(root, commits, selected, maps{})
	if !partial || !strings.Contains(reason, "merge commits excluded") {
		t.Fatalf("merge history partial=%v reason=%q", partial, reason)
	}
	topicDefs, err := definitionsForFile("sample_test.go", []byte(topic))
	if err != nil {
		t.Fatal(err)
	}
	resolutionDefs, err := definitionsForFile("sample_test.go", []byte(resolution))
	if err != nil {
		t.Fatal(err)
	}
	key := func(name string) string { return "example.test/p\x00sample_test.go/test/" + name }
	foundTopic, foundResolution := false, false
	for _, event := range events {
		if strings.HasSuffix(event.Key, "/TestTopic") && event.Status == "introduced" && event.Commit == topicCommit {
			foundTopic = true
		}
		if strings.HasSuffix(event.Key, "/TestMergeResolution") {
			foundResolution = true
		}
	}
	if !foundTopic || foundResolution {
		t.Fatalf("topic attributed=%v merge resolution attributed=%v events=%+v", foundTopic, foundResolution, events)
	}
	if !credits["all_developers"][key("TestTopic")+"\x00"+topicDefs["sample_test.go/test/TestTopic"].BodyHash] {
		t.Fatal("non-merge test definition was not credited")
	}
	if credits["all_developers"][key("TestMergeResolution")+"\x00"+resolutionDefs["sample_test.go/test/TestMergeResolution"].BodyHash] {
		t.Fatal("merge-resolution-only definition was credited")
	}
}

func TestContributionGroupsRequireCompleteSameIDDenominator(t *testing.T) {
	group := []contributionTest{{ID: "g1"}, {ID: "g2"}}
	base := []contributionTest{{ID: "b1"}}
	ids := []string{"m1", "m2"}
	groupPairs := []contributionPair{{TestID: "g1", MutationID: "m1", Status: "detected"}, {TestID: "g2", MutationID: "m1", Status: "not_detected"}, {TestID: "g1", MutationID: "m2", Status: "not_detected"}, {TestID: "g2", MutationID: "m2", Status: "not_detected"}}
	basePairs := []contributionPair{{TestID: "b1", MutationID: "m1", Status: "not_detected"}, {TestID: "b1", MutationID: "m2", Status: "not_detected"}}
	got := compareContributionGroup("dev", group, base, groupPairs, basePairs, ids, "test definition")
	if got.AdditionalDetected != 1 || got.CompleteMutationDenominator != 2 || got.Status != "measured" {
		t.Fatalf("group=%+v", got)
	}
	got = compareContributionGroup("dev", group, base, groupPairs[:3], basePairs, ids, "test definition")
	if got.AdditionalDetected != 1 || got.CompleteMutationDenominator != 1 || got.IncompleteMutations != 1 || got.Status != "partial" {
		t.Fatalf("incomplete group=%+v", got)
	}
}

func TestBaselineListedUnreadableTestBlobRejectsOverlay(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Fixture")
	gitFixture(t, root, "config", "user.email", "fixture@example.test")
	if err := os.Symlink("target", filepath.Join(root, "broken_test.go")); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "add", "broken_test.go")
	gitFixture(t, root, "commit", "-qm", "symlink test")
	source := []snapshotEntry{{path: "go.mod", mode: "100644", data: []byte("module example.test/p\ngo 1.22\n")}}
	if _, err := overlayBaselineTests(root, strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD"))), source); err == nil {
		t.Fatal("listed unreadable test blob silently omitted")
	}
}

func TestBaselineIncompatibilityIsNotZeroDetections(t *testing.T) {
	entries := []snapshotEntry{{path: "go.mod", mode: "100644", data: []byte("module example.test/p\ngo 1.22\n")}, {path: "app.go", mode: "100644", data: []byte("package p\n")}, {path: "legacy_test.go", mode: "100644", data: []byte("package p\nimport \"testing\"\nfunc TestLegacy(t *testing.T){ MissingSupport() }\n")}}
	tests := contributionTests(entries)
	if len(tests) != 1 {
		t.Fatalf("tests=%d", len(tests))
	}
	d, err := materializeMutationCopy(entries)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	status, _, _, _ := executeContributionTest(context.Background(), d, tests[0], 15*time.Second, false)
	if status != "inconclusive" {
		t.Fatalf("historical incompatibility=%s", status)
	}
}

func TestHistoricalModuleCacheAggregateByteBudget(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Budget")
	gitFixture(t, root, "config", "user.email", "budget@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/budget\ngo 1.22\n")
	write("one.go", "package budget\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "first")
	first := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	write("two.go", "package budget\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "second")
	second := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	write("three.go", "package budget\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "third")
	third := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))

	cache := &historicalModuleCache{root: root}
	if _, err := cache.index(first); err != nil {
		t.Fatal(err)
	}
	used := cache.bytesUsed
	cache.byteLimit = used + 1
	if _, err := cache.index(first); err != nil {
		t.Fatal(err)
	}
	if cache.bytesUsed != used {
		t.Fatalf("cached revision consumed budget twice: before=%d after=%d", used, cache.bytesUsed)
	}
	if _, err := historicalDefinitionKey(cache, second, "two_test.go", "test/TestTwo"); err == nil {
		t.Fatal("module index succeeded after exhausting the remaining byte allowance")
	}
	if cache.bytesUsed != cache.byteLimit {
		t.Fatalf("failed bounded listing did not consume remaining allowance: used=%d limit=%d", cache.bytesUsed, cache.byteLimit)
	}
	if _, err := historicalDefinitionKey(cache, third, "three_test.go", "test/TestThree"); err == nil {
		t.Fatal("another revision reused allowance after a bounded listing failure")
	}
}

func TestHistoricalAttributionSkipsNestedModuleTests(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Nested")
	gitFixture(t, root, "config", "user.email", "nested@example.test")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":           "module example.test/root\ngo 1.22\n",
		"nested/go.mod":    "module example.test/nested\ngo 1.22\n",
		"nested/n_test.go": "package nested\nimport \"testing\"\nfunc TestNested(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "nested test")
	commits := strings.Fields(string(gitFixture(t, root, "rev-list", "HEAD")))
	selected := identityInfo{Name: "Nested", Email: "nested@example.test", Available: true}
	credits, changes, partial, _ := resolveContributionHistory(root, commits, selected, maps{})
	if !partial {
		t.Fatal("nested module history must be reported partial")
	}
	if len(credits["selected"]) != 0 || len(credits["all_developers"]) != 0 || len(changes) != 0 {
		t.Fatalf("nested test received root-module attribution: credits=%v changes=%v", credits, changes)
	}
}

func TestContributionCLIUsesSameManifestAndEvidenceForGroups(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Base")
	gitFixture(t, root, "config", "user.email", "base@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/e2e\ngo 1.22\n")
	write("app.go", "package e2e\nfunc Predicate() bool { return true }\n")
	baseTests := "package e2e\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\n"
	write("app_test.go", baseTests)
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "base")
	base := strings.TrimSpace(string(gitFixture(t, root, "rev-parse", "HEAD")))
	currentTests := "package e2e\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestDetect(t *testing.T) { if !Predicate(){t.Fatal(\"mutant\")} }\n"
	write("app_test.go", currentTests)
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Other", "-c", "user.email=other@example.test", "commit", "-m", "add detector", "-m", "Co-authored-by: Ada <ada-old@example.test>")
	artifacts := privateTempDir(t)
	output, err := run(t, root, "--base", base, "--selected", "Ada=ada@example.test", "--alias", "ada-old@example.test=ada@example.test", "--mutation-tests", "--test-contributions", "--mutation-limit", "1", "--dynamic-artifacts", artifacts, "--dynamic-timeout", "20s", "--dynamic-total-timeout", "2m", "--contribution-timeout", "20s", "--contribution-total-timeout", "3m")
	if err != nil {
		t.Fatalf("brag contribution command: %v output=%s", err, output)
	}
	var report struct {
		SchemaVersion int `json:"schema_version"`
		Contributions struct {
			MatrixArtifact string              `json:"matrix_artifact"`
			MatrixSHA256   string              `json:"matrix_sha256"`
			Groups         []contributionGroup `json:"groups"`
		} `json:"test_contributions"`
	}
	if err = json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 5 {
		t.Fatalf("schema=%d", report.SchemaVersion)
	}
	matrixPath := filepath.Join(artifacts, filepath.Base(report.Contributions.MatrixArtifact))
	matrixBytes, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(matrixBytes)
	if hex.EncodeToString(digest[:]) != report.Contributions.MatrixSHA256 {
		t.Fatal("matrix digest mismatch")
	}
	var matrix struct {
		Tests         []contributionTest    `json:"tests"`
		BaselineTests []contributionTest    `json:"baseline_tests"`
		Pairs         []contributionPair    `json:"pairs"`
		Controls      []contributionControl `json:"controls"`
	}
	if err = json.Unmarshal(matrixBytes, &matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Pairs) != 3 {
		t.Fatalf("pairs=%d want two current and one baseline: %+v", len(matrix.Pairs), matrix.Pairs)
	}
	ids := map[string]bool{}
	names := map[string]string{}
	for _, test := range matrix.Tests {
		names[test.ID] = test.Name
	}
	for _, test := range matrix.BaselineTests {
		names[test.ID] = test.Name
	}
	currentDetected, baselinePass := false, false
	for _, pair := range matrix.Pairs {
		ids[pair.MutationID] = true
		if pair.EvidenceArtifact == "" || pair.EvidenceSHA256 == "" {
			t.Fatalf("pair evidence missing: %+v", pair)
		}
		if pair.Population == "current" && names[pair.TestID] == "TestDetect" && pair.Status == "detected" {
			currentDetected = true
		}
		if pair.Population == "baseline" && names[pair.TestID] == "TestOld" && pair.Status == "not_detected" {
			baselinePass = true
		}
		path := filepath.Join(artifacts, filepath.Base(pair.EvidenceArtifact))
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != pair.EvidenceSHA256 {
			t.Fatal("pair evidence digest mismatch")
		}
	}
	if len(ids) != 1 || !currentDetected || !baselinePass {
		t.Fatalf("IDs=%v current=%v baseline=%v pairs=%+v", ids, currentDetected, baselinePass, matrix.Pairs)
	}
	for _, control := range matrix.Controls {
		if control.EvidenceArtifact == "" || control.EvidenceSHA256 == "" {
			t.Fatalf("control evidence missing: %+v", control)
		}
		b, e := os.ReadFile(filepath.Join(artifacts, filepath.Base(control.EvidenceArtifact)))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != control.EvidenceSHA256 {
			t.Fatal("control evidence digest mismatch")
		}
	}
	selectedDelta := false
	for _, group := range report.Contributions.Groups {
		if group.Name == "selected_developer_vs_baseline" && group.AdditionalDetected == 1 && group.CompleteMutationDenominator == 1 {
			selectedDelta = true
		}
	}
	if !selectedDelta {
		t.Fatalf("selected-developer group delta missing: %+v", report.Contributions.Groups)
	}
}

func gitFixture(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}
func pathBase(s string) string { return filepath.Base(s) }
func hashContributionDecl(t *testing.T, src, name string) string {
	t.Helper()
	defs, err := definitionsForFile("app_test.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return defs["app_test.go/test/"+name].BodyHash
}
