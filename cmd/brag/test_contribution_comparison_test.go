package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComparisonAttributionCountsBecomeIndeterminateWithoutCompleteIdentityHistory(t *testing.T) {
	shared, unattributed, indeterminate := comparisonAttributionCounts(true, false, 2, 3, 10)
	if shared != nil || unattributed != nil || indeterminate != 10 {
		t.Fatalf("partial history counts shared=%v unattributed=%v indeterminate=%d", shared, unattributed, indeterminate)
	}

	shared, unattributed, indeterminate = comparisonAttributionCounts(true, true, 2, 3, 10)
	if shared == nil || *shared != 2 || unattributed == nil || *unattributed != 3 || indeterminate != 0 {
		t.Fatalf("complete history counts shared=%v unattributed=%v indeterminate=%d", shared, unattributed, indeterminate)
	}

	shared, unattributed, indeterminate = comparisonAttributionCounts(false, true, 2, 3, 10)
	if shared != nil || unattributed != nil || indeterminate != 10 {
		t.Fatalf("missing identity counts shared=%v unattributed=%v indeterminate=%d", shared, unattributed, indeterminate)
	}
}

func TestClassifyComparisonTestsKeepsSharedAndUnknownOutOfPeerCohort(t *testing.T) {
	selected := identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}
	tests := []contributionTest{
		{ID: "a", Package: "m/p", File: "p_test.go", Name: "TestAda", DeclarationSHA256: "a"},
		{ID: "b", Package: "m/p", File: "p_test.go", Name: "TestPeer", DeclarationSHA256: "b"},
		{ID: "c", Package: "m/p", File: "p_test.go", Name: "TestShared", DeclarationSHA256: "c"},
		{ID: "d", Package: "m/p", File: "p_test.go", Name: "TestUnknown", DeclarationSHA256: "d"},
	}
	changes := []contributionDefinitionChange{
		{Key: "m/p\x00p_test.go/test/TestAda", NewHash: "a", Credits: []string{"ada@example.test"}},
		{Key: "m/p\x00p_test.go/test/TestPeer", NewHash: "b", Credits: []string{"peer@example.test"}},
		{Key: "m/p\x00p_test.go/test/TestShared", NewHash: "c", Credits: []string{"ada@example.test", "peer@example.test"}},
	}
	groups, shared, unknown := classifyComparisonTests(tests, changes, selected, maps{aliases: map[string]string{}})
	if len(groups["selected_only"]) != 1 || groups["selected_only"][0].ID != "a" {
		t.Fatalf("selected cohort=%v", groups["selected_only"])
	}
	if len(groups["other_only"]) != 1 || groups["other_only"][0].ID != "b" {
		t.Fatalf("other cohort=%v", groups["other_only"])
	}
	if shared != 1 || unknown != 1 {
		t.Fatalf("shared=%d unknown=%d", shared, unknown)
	}
}

func TestComparisonSelectionAndPairDenominatorAreDeterministicAndFair(t *testing.T) {
	groups := map[string][]contributionTest{"selected_only": {}, "other_only": {}}
	for i := 0; i < 8; i++ {
		groups["selected_only"] = append(groups["selected_only"], contributionTest{ID: string(rune('a' + i)), Package: "p1"})
		groups["other_only"] = append(groups["other_only"], contributionTest{ID: string(rune('A' + i)), Package: "p2"})
	}
	first := selectComparisonTests(groups, 7)
	second := selectComparisonTests(groups, 7)
	if len(first["selected_only"])+len(first["other_only"]) != 7 {
		t.Fatal("selection exceeded or failed limit")
	}
	if len(first["selected_only"]) == 0 || len(first["other_only"]) == 0 || len(first["selected_only"]) != 4 || len(first["other_only"]) != 3 {
		t.Fatalf("selection was not fair across cohorts: %#v", first)
	}
	for i := range first {
		for j := range first[i] {
			if first[i][j].ID != second[i][j].ID {
				t.Fatal("selection is not deterministic")
			}
		}
	}
	ids := []string{"mutation-a", "mutation-b", "mutation-c"}
	selectedPairs, _ := comparisonPairs(first["selected_only"], ids, 100)
	otherPairs, _ := comparisonPairs(first["other_only"], ids, 100)
	for _, cohortPairs := range [][]contributionPair{selectedPairs, otherPairs} {
		seen := map[string]bool{}
		for _, pair := range cohortPairs {
			seen[pair.MutationID] = true
		}
		for _, id := range ids {
			if !seen[id] {
				t.Fatalf("cohort missing shared mutation denominator %q", id)
			}
		}
	}
	capped, omitted := comparisonPairs(first["selected_only"], ids, 2)
	if len(capped) != 2 || omitted != len(first["selected_only"])*len(ids)-2 {
		t.Fatalf("cap=%d omitted=%d", len(capped), omitted)
	}
}

func TestComparisonSelectionAlternatesCohortsWithSkewedPackageCounts(t *testing.T) {
	for _, manyCohort := range []string{"selected_only", "other_only"} {
		t.Run(manyCohort, func(t *testing.T) {
			groups := map[string][]contributionTest{"selected_only": {}, "other_only": {}}
			many := groups[manyCohort]
			for p := 0; p < 12; p++ {
				for n := 0; n < 3; n++ {
					many = append(many, contributionTest{ID: fmt.Sprintf("%02d-%02d", p, n), Package: fmt.Sprintf("pkg-%02d", p)})
				}
			}
			groups[manyCohort] = many
			fewCohort := "selected_only"
			if manyCohort == fewCohort {
				fewCohort = "other_only"
			}
			for n := 0; n < 12; n++ {
				groups[fewCohort] = append(groups[fewCohort], contributionTest{ID: fmt.Sprintf("single-%02d", n), Package: "single"})
			}
			chosen := selectComparisonTests(groups, 9)
			manyCount, fewCount := len(chosen[manyCohort]), len(chosen[fewCohort])
			if manyCount+fewCount != 9 || manyCount-fewCount > 1 || fewCount-manyCount > 1 {
				t.Fatalf("skewed selection not cohort-fair: many=%d few=%d", manyCount, fewCount)
			}
			for i := 0; i < len(chosen[manyCohort]); i++ {
				wantPackage := fmt.Sprintf("pkg-%02d", i)
				if chosen[manyCohort][i].Package != wantPackage || chosen[manyCohort][i].ID != wantPackage[4:]+"-00" {
					t.Fatalf("package/test ordering at %d: %+v", i, chosen[manyCohort][i])
				}
			}
		})
	}
}

func TestLocalGitComparisonAttributionUsesResolvedHistoryOnly(t *testing.T) {
	root := t.TempDir()
	gitFixture(t, root, "init")
	gitFixture(t, root, "config", "user.name", "Ada")
	gitFixture(t, root, "config", "user.email", "ada-old@example.test")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "app_test.go"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module p\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	write("package p\nimport \"testing\"\nfunc TestAda(t *testing.T) {}\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "commit", "-qm", "selected test")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "--allow-empty", "-qm", "peer baseline")
	write("package p\nimport \"testing\"\nfunc TestAda(t *testing.T) {}\nfunc TestPeer(t *testing.T) {}\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-qm", "peer adds test")
	write("package p\nimport \"testing\"\nfunc TestAda(t *testing.T) {}\nfunc TestPeer(t *testing.T) {}\nfunc TestShared(t *testing.T) {}\nfunc TestUnknown(t *testing.T) {}\n")
	gitFixture(t, root, "add", "app_test.go")
	gitFixture(t, root, "-c", "user.name=Peer", "-c", "user.email=peer@example.test", "commit", "-m", "peer adds shared tests", "-m", "Co-authored-by: Ada <ada@example.test>")
	commits := strings.Fields(string(gitFixture(t, root, "rev-list", "--reverse", "HEAD")))
	cm := maps{aliases: map[string]string{"ada-old@example.test": "ada@example.test"}}
	selected := resolve(identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}, cm)
	_, events, partial, _ := resolveContributionHistory(root, commits, selected, cm)
	if partial {
		t.Fatal("synthetic history unexpectedly partial")
	}
	tests := []contributionTest{
		{ID: "ada", Package: "p", File: "app_test.go", Name: "TestAda", DeclarationSHA256: hashContributionDecl(t, `package p
import "testing"
func TestAda(t *testing.T) {}`, "TestAda")},
		{ID: "peer", Package: "p", File: "app_test.go", Name: "TestPeer", DeclarationSHA256: hashContributionDecl(t, `package p
import "testing"
func TestPeer(t *testing.T) {}`, "TestPeer")},
		{ID: "shared", Package: "p", File: "app_test.go", Name: "TestShared", DeclarationSHA256: hashContributionDecl(t, `package p
import "testing"
func TestShared(t *testing.T) {}`, "TestShared")},
		{ID: "unknown", Package: "p", File: "app_test.go", Name: "TestUnknown", DeclarationSHA256: "unrecorded"},
	}
	groups, shared, unknown := classifyComparisonTests(tests, events, selected, cm)
	if len(groups["selected_only"]) != 1 || groups["selected_only"][0].Name != "TestAda" {
		t.Fatalf("selected=%v", groups["selected_only"])
	}
	if len(groups["other_only"]) != 1 || groups["other_only"][0].Name != "TestPeer" {
		t.Fatalf("other=%v", groups["other_only"])
	}
	if shared != 1 || unknown != 1 {
		t.Fatalf("shared=%d unknown=%d", shared, unknown)
	}
}

func TestComparisonApplyFailureRecordsInconclusivePairAndInvalidatesMutation(t *testing.T) {
	matrix := comparisonEvidence{Pairs: []contributionPair{}}
	validByMutation := map[string]bool{"mutation": true}
	pair := contributionPair{Population: "selected_only", TestID: "test", MutationID: "mutation", Status: "not_executed"}

	recordInconclusiveComparisonPair(&matrix, validByMutation, pair, "mutation_application_failed")
	if len(matrix.Pairs) != 1 || matrix.Pairs[0].MutationID != "mutation" || matrix.Pairs[0].Status != "inconclusive" || matrix.Pairs[0].Classification != "mutation_application_failed" {
		t.Fatalf("failure pair missing from evidence matrix: %+v", matrix.Pairs)
	}
	if validByMutation["mutation"] {
		t.Fatal("mutation with an application failure must be excluded from the complete denominator")
	}
}
