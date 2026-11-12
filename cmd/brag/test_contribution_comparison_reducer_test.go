package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func validComparisonControl(population, testID, status string) contributionControl {
	return contributionControl{Population: population, TestID: testID, Status: status, EvidenceArtifact: "control.log", EvidenceSHA256: strings.Repeat("a", 64)}
}

func validComparisonPair(population, testID, mutationID, status string) contributionPair {
	return contributionPair{Population: population, TestID: testID, MutationID: mutationID, Status: status, EvidenceArtifact: "pair.log", EvidenceSHA256: strings.Repeat("b", 64)}
}

func appendCompleteTestEvidence(matrix *comparisonEvidence, population, testID string, statuses map[string]string) {
	matrix.Controls = append(matrix.Controls, validComparisonControl(population, testID, "not_detected"))
	mutationIDs := make([]string, 0, len(statuses))
	for mutationID := range statuses {
		mutationIDs = append(mutationIDs, mutationID)
	}
	sort.Strings(mutationIDs)
	for _, mutationID := range mutationIDs {
		matrix.Pairs = append(matrix.Pairs, validComparisonPair(population, testID, mutationID, statuses[mutationID]))
	}
}

func TestReduceComparisonCohortRequiresCompletePersistedEvidence(t *testing.T) {
	tests := []contributionTest{{ID: "a"}}
	ids := []string{"m"}
	matrix := comparisonEvidence{MutationIDs: []string{"m"}, Controls: []contributionControl{validComparisonControl("selected_only", "a", "not_detected")}, Pairs: []contributionPair{validComparisonPair("selected_only", "a", "m", "detected")}}
	complete, detected := reduceComparisonCohort(matrix, "selected_only", tests, ids)
	if !complete["m"] || !detected["m"] {
		t.Fatal("persisted terminal evidence should be complete and detected")
	}
	matrix.Controls = append(matrix.Controls, matrix.Controls[0])
	complete, _ = reduceComparisonCohort(matrix, "selected_only", tests, ids)
	if complete["m"] {
		t.Fatal("duplicate controls must make mutation incomplete")
	}
}

func TestReduceComparisonCohortRejectsMissingAndInconclusiveEvidence(t *testing.T) {
	tests := []contributionTest{{ID: "a"}, {ID: "b"}}
	ids := []string{"m"}
	matrix := comparisonEvidence{
		MutationIDs: []string{"m"},
		Controls:    []contributionControl{validComparisonControl("other_only", "a", "not_detected")},
		Pairs:       []contributionPair{validComparisonPair("other_only", "a", "m", "inconclusive")},
	}
	complete, _ := reduceComparisonCohort(matrix, "other_only", tests, ids)
	if complete["m"] {
		t.Fatal("missing control/pair and inconclusive pair must be incomplete")
	}

	matrix.Pairs = append(matrix.Pairs, validComparisonPair("other_only", "a", "m", "not_detected"))
	complete, _ = reduceComparisonCohort(matrix, "other_only", tests[:1], ids)
	if complete["m"] {
		t.Fatal("duplicate pair outcomes must make mutation incomplete")
	}

	matrix.MutationIDs = []string{"different"}
	complete, _ = reduceComparisonCohort(matrix, "other_only", tests[:1], ids)
	if complete["m"] {
		t.Fatal("mismatched matrix mutation IDs must fail closed")
	}
}

func TestReduceComparisonCohortEmptyTestsIncomplete(t *testing.T) {
	complete, _ := reduceComparisonCohort(comparisonEvidence{MutationIDs: []string{"m"}}, "selected_only", nil, []string{"m"})
	if complete["m"] {
		t.Fatal("empty cohort cannot claim complete mutations")
	}
	cohort := summarizeComparisonCohort(comparisonEvidence{MutationIDs: []string{"m"}}, comparisonCohort{Name: "selected_only"}, nil, []string{"m"}, false)
	if cohort.Status != "unavailable" || cohort.CompleteMutationDenominator != 0 || cohort.IncompleteMutations != 0 {
		t.Fatalf("empty cohort manufactured a denominator: %+v", cohort)
	}
}

func TestPairedComparisonPartitionsCompleteMutationIDs(t *testing.T) {
	ids := []string{"both", "neither", "other", "selected"}
	selectedTests := []contributionTest{{ID: "selected-test"}}
	otherTests := []contributionTest{{ID: "other-test"}}
	matrix := comparisonEvidence{MutationIDs: append([]string(nil), ids...)}
	appendCompleteTestEvidence(&matrix, "selected_only", "selected-test", map[string]string{
		"both": "detected", "selected": "detected", "other": "not_detected", "neither": "not_detected",
	})
	appendCompleteTestEvidence(&matrix, "other_only", "other-test", map[string]string{
		"both": "detected", "selected": "not_detected", "other": "detected", "neither": "not_detected",
	})

	paired := reducePairedComparison(matrix, ids, ids, selectedTests, otherTests)
	if paired.Status != "measured" || paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 4 {
		t.Fatalf("paired denominator/status=%+v", paired)
	}
	if !reflect.DeepEqual(paired.BothDetected, []string{"both"}) || !reflect.DeepEqual(paired.SelectedOnlyDetected, []string{"selected"}) || !reflect.DeepEqual(paired.OtherOnlyDetected, []string{"other"}) || !reflect.DeepEqual(paired.NeitherDetected, []string{"neither"}) {
		t.Fatalf("paired detection partition=%+v", paired)
	}
	if len(paired.PairedMutationIDs) != 4 || len(paired.OmittedMutationIDs) != 0 {
		t.Fatalf("complete mutation IDs not preserved: %+v", paired)
	}
}

func TestPairedComparisonOmitsDifferentIncompleteOutcomesAcrossCohorts(t *testing.T) {
	ids := []string{"selected-incomplete", "other-incomplete"}
	selectedTests := []contributionTest{{ID: "s"}}
	otherTests := []contributionTest{{ID: "o"}}
	matrix := comparisonEvidence{MutationIDs: append([]string(nil), ids...)}
	appendCompleteTestEvidence(&matrix, "selected_only", "s", map[string]string{"selected-incomplete": "unstable", "other-incomplete": "not_detected"})
	appendCompleteTestEvidence(&matrix, "other_only", "o", map[string]string{"selected-incomplete": "detected", "other-incomplete": "not_executed"})
	paired := reducePairedComparison(matrix, ids, ids, selectedTests, otherTests)
	if paired.Status != "partial" || paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 0 {
		t.Fatalf("incomplete paired outcomes/status=%+v", paired)
	}
	if !reflect.DeepEqual(paired.OmittedMutationIDs, ids) || !reflect.DeepEqual(paired.OmissionReasons[ids[0]], []string{"selected_only_incomplete"}) || !reflect.DeepEqual(paired.OmissionReasons[ids[1]], []string{"other_only_incomplete"}) {
		t.Fatalf("incomplete reasons/IDs=%+v", paired)
	}
	if len(paired.PairedMutationIDs)+len(paired.SelectedOnlyDetected)+len(paired.OtherOnlyDetected)+len(paired.BothDetected)+len(paired.NeitherDetected) != 0 {
		t.Fatalf("inconclusive outcomes were treated as survivors: %+v", paired)
	}
}

func TestPairedComparisonRequiresPassingControlsAndValidArtifacts(t *testing.T) {
	ids := []string{"m"}
	tests := []contributionTest{{ID: "s"}}
	other := []contributionTest{{ID: "o"}}
	for _, tc := range []struct {
		name     string
		controls []contributionControl
	}{
		{name: "missing selected control", controls: []contributionControl{validComparisonControl("other_only", "o", "not_detected")}},
		{name: "failing other control", controls: []contributionControl{validComparisonControl("selected_only", "s", "not_detected"), validComparisonControl("other_only", "o", "detected")}},
		{name: "missing artifact", controls: []contributionControl{{Population: "selected_only", TestID: "s", Status: "not_detected", EvidenceSHA256: strings.Repeat("a", 64)}, validComparisonControl("other_only", "o", "not_detected")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := comparisonEvidence{MutationIDs: []string{"m"}, Controls: tc.controls, Pairs: []contributionPair{validComparisonPair("selected_only", "s", "m", "detected"), validComparisonPair("other_only", "o", "m", "not_detected")}}
			paired := reducePairedComparison(matrix, ids, ids, tests, other)
			if paired.Status != "partial" || paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 0 || len(paired.PairedMutationIDs) != 0 {
				t.Fatalf("invalid control/evidence entered paired denominator: %+v", paired)
			}
		})
	}
}

func TestPairedComparisonEmptyCohortHasUnavailableDenominator(t *testing.T) {
	ids := []string{"m"}
	paired := reducePairedComparison(comparisonEvidence{MutationIDs: ids}, ids, ids, nil, []contributionTest{{ID: "other"}})
	if paired.Status != "unavailable" || paired.CompleteMutationDenominator != nil || !reflect.DeepEqual(paired.OmittedMutationIDs, ids) || !reflect.DeepEqual(paired.OmissionReasons["m"], []string{"cohort_unavailable"}) {
		t.Fatalf("empty cohort produced a denominator: %+v", paired)
	}
}

func TestPairedComparisonExcludesSharedAndUnattributedTests(t *testing.T) {
	selected := identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}
	tests := []contributionTest{
		{ID: "s", Package: "m/p", File: "p_test.go", Name: "TestSelected", DeclarationSHA256: "s"},
		{ID: "o", Package: "m/p", File: "p_test.go", Name: "TestOther", DeclarationSHA256: "o"},
		{ID: "x", Package: "m/p", File: "p_test.go", Name: "TestShared", DeclarationSHA256: "x"},
		{ID: "u", Package: "m/p", File: "p_test.go", Name: "TestUnknown", DeclarationSHA256: "u"},
	}
	changes := []contributionDefinitionChange{
		{Key: "m/p\x00p_test.go/test/TestSelected", NewHash: "s", Credits: []string{"ada@example.test"}},
		{Key: "m/p\x00p_test.go/test/TestOther", NewHash: "o", Credits: []string{"peer@example.test"}},
		{Key: "m/p\x00p_test.go/test/TestShared", NewHash: "x", Credits: []string{"ada@example.test", "peer@example.test"}},
	}
	groups, shared, unknown := classifyComparisonTests(tests, changes, selected, maps{aliases: map[string]string{}})
	if shared != 1 || unknown != 1 || len(groups["selected_only"]) != 1 || len(groups["other_only"]) != 1 {
		t.Fatalf("cohort exclusion=%v shared=%d unknown=%d", groups, shared, unknown)
	}
	ids := []string{"m"}
	matrix := comparisonEvidence{MutationIDs: ids}
	appendCompleteTestEvidence(&matrix, "selected_only", "s", map[string]string{"m": "detected"})
	appendCompleteTestEvidence(&matrix, "other_only", "o", map[string]string{"m": "not_detected"})
	paired := reducePairedComparison(matrix, ids, ids, groups["selected_only"], groups["other_only"])
	if paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 1 || len(paired.PairedMutationIDs) != 1 {
		t.Fatalf("shared/unattributed tests entered paired denominator: %+v", paired)
	}
}

func TestPairedComparisonListsSelectionOmissionsAndWithholdsOnPersistenceFailure(t *testing.T) {
	eligible, selectedIDs := []string{"m1", "m2"}, []string{"m1"}
	selectedTests, otherTests := []contributionTest{{ID: "s"}}, []contributionTest{{ID: "o"}}
	matrix := comparisonEvidence{MutationIDs: selectedIDs}
	appendCompleteTestEvidence(&matrix, "selected_only", "s", map[string]string{"m1": "detected"})
	appendCompleteTestEvidence(&matrix, "other_only", "o", map[string]string{"m1": "not_detected"})
	paired := reducePairedComparison(matrix, eligible, selectedIDs, selectedTests, otherTests)
	if paired.Status != "partial" || paired.CompleteMutationDenominator == nil || *paired.CompleteMutationDenominator != 1 || !reflect.DeepEqual(paired.OmittedMutationIDs, []string{"m2"}) || !reflect.DeepEqual(paired.OmissionReasons["m2"], []string{"comparison_pair_limit"}) {
		t.Fatalf("selection omission not disclosed: %+v", paired)
	}
	cohorts := []comparisonCohort{{Name: "selected_only", CompleteMutationDenominator: 1, DetectedMutations: 1}, {Name: "other_only", CompleteMutationDenominator: 1}}
	groups := map[string][]contributionTest{"selected_only": selectedTests, "other_only": otherTests}
	paired, cohorts = withholdComparisonAggregates(paired, cohorts, groups, selectedIDs)
	if paired.Status != "inconclusive" || paired.CompleteMutationDenominator != nil || len(paired.PairedMutationIDs) != 0 || len(paired.SelectedOnlyDetected)+len(paired.OtherOnlyDetected)+len(paired.BothDetected)+len(paired.NeitherDetected) != 0 {
		t.Fatalf("matrix persistence failure retained paired aggregates: %+v", paired)
	}
	if cohorts[0].CompleteMutationDenominator != 0 || cohorts[0].DetectedMutations != 0 || cohorts[0].Status != "inconclusive" {
		t.Fatalf("matrix persistence failure retained cohort aggregates: %+v", cohorts[0])
	}
}

func TestComparisonSchedulesAreDeterministicAndAlternateCohortPriority(t *testing.T) {
	groups := map[string][]contributionTest{
		"selected_only": {{ID: "s1"}, {ID: "s2"}, {ID: "s3"}},
		"other_only":    {{ID: "o1"}, {ID: "o2"}, {ID: "o3"}},
	}
	controls := comparisonControlRounds(groups)
	if !reflect.DeepEqual(controls, comparisonControlRounds(groups)) || len(controls) != 3 {
		t.Fatalf("control schedule is not reproducible: %+v", controls)
	}
	if controls[0][0].Population != "selected_only" || controls[0][1].Population != "other_only" || !controls[0][0].Reserve || controls[1][0].Population != "other_only" || controls[1][1].Population != "selected_only" || !controls[1][0].Reserve {
		t.Fatalf("control turns do not interleave and alternate: %+v", controls)
	}
	ids := []string{"m1", "m2"}
	selectedPairs := []contributionPair{{TestID: "s1", MutationID: "m1"}, {TestID: "s2", MutationID: "m2"}}
	otherPairs := []contributionPair{{TestID: "o1", MutationID: "m1"}, {TestID: "o2", MutationID: "m2"}}
	pairs := comparisonPairRounds(ids, selectedPairs, otherPairs)
	if !reflect.DeepEqual(pairs, comparisonPairRounds(ids, selectedPairs, otherPairs)) || len(pairs) != 2 {
		t.Fatalf("pair schedule is not reproducible: %+v", pairs)
	}
	if pairs[0][0].Population != "selected_only" || pairs[0][1].Population != "other_only" || pairs[1][0].Population != "other_only" || pairs[1][1].Population != "selected_only" {
		t.Fatalf("pair schedule does not interleave matched mutation IDs: %+v", pairs)
	}
}

type fakeScheduledOutcome struct {
	population string
	status     string
}

func fakeDeadlineControlOutcomes(rounds [][]comparisonControlTask, total, taskCost time.Duration) []fakeScheduledOutcome {
	remaining := total
	outcomes := []fakeScheduledOutcome{}
	for _, round := range rounds {
		for _, task := range round {
			if remaining <= 0 {
				outcomes = append(outcomes, fakeScheduledOutcome{task.Population, "not_executed"})
				continue
			}
			budget := comparisonFairBudget(remaining, task.Reserve)
			used := taskCost
			status := "not_detected"
			if used > budget {
				used = budget
				status = "inconclusive"
			}
			remaining -= used
			outcomes = append(outcomes, fakeScheduledOutcome{task.Population, status})
		}
	}
	return outcomes
}

func fakeDeadlinePairOutcomes(rounds [][]comparisonPairTask, total, taskCost time.Duration) []fakeScheduledOutcome {
	remaining := total
	outcomes := []fakeScheduledOutcome{}
	for _, round := range rounds {
		for _, task := range round {
			if remaining <= 0 {
				outcomes = append(outcomes, fakeScheduledOutcome{task.Population, "not_executed"})
				continue
			}
			budget := comparisonFairBudget(remaining, task.Reserve)
			used := taskCost
			status := "detected"
			if used > budget {
				used = budget
				status = "inconclusive"
			}
			remaining -= used
			outcomes = append(outcomes, fakeScheduledOutcome{task.Population, status})
		}
	}
	return outcomes
}

func assertDeadlineFairness(t *testing.T, outcomes []fakeScheduledOutcome) {
	t.Helper()
	if len(outcomes) < 4 || outcomes[0].population == outcomes[1].population || outcomes[0].status != "inconclusive" || outcomes[1].status != "inconclusive" {
		t.Fatalf("deadline did not give both cohorts a bounded opportunity: %+v", outcomes)
	}
	if outcomes[2].status != "not_executed" || outcomes[3].status != "not_executed" {
		t.Fatalf("remaining work was not recorded as unexecuted: %+v", outcomes)
	}
}

func TestComparisonBudgetExpiryDuringControlsAndPairsLeavesIncompleteRows(t *testing.T) {
	groups := map[string][]contributionTest{
		"selected_only": {{ID: "s1"}, {ID: "s2"}},
		"other_only":    {{ID: "o1"}, {ID: "o2"}},
	}
	controls := fakeDeadlineControlOutcomes(comparisonControlRounds(groups), 6*time.Second, 4*time.Second)
	assertDeadlineFairness(t, controls)

	pairs := comparisonPairRounds([]string{"m1", "m2"}, []contributionPair{{TestID: "s1", MutationID: "m1"}, {TestID: "s2", MutationID: "m2"}}, []contributionPair{{TestID: "o1", MutationID: "m1"}, {TestID: "o2", MutationID: "m2"}})
	mutations := fakeDeadlinePairOutcomes(pairs, 6*time.Second, 4*time.Second)
	assertDeadlineFairness(t, mutations)
}

func TestWithholdComparisonAggregatesAfterMatrixPersistenceFailure(t *testing.T) {
	artifactDir := t.TempDir()
	artifactBudget := &artifactBudget{used: dynamicArtifactTotalBytes}
	if path := writeDynamicArtifact(artifactDir, artifactBudget, "matrix-", ".json", []byte(`{"kind":"matrix"}`)); path != "" {
		t.Fatalf("exhausted artifact budget persisted a matrix: %s", path)
	}
	paired := pairedComparison{
		Status: "measured", EligibleMutationIDs: []string{"m"}, SelectedMutationIDs: []string{"m"},
		PairedMutationIDs: []string{"m"}, CompleteMutationDenominator: intPointer(1),
		SelectedOnlyDetected: []string{"m"}, OmittedMutationIDs: []string{}, OmissionReasons: map[string][]string{},
	}
	cohorts := []comparisonCohort{{Name: "selected_only", SelectedTests: 1}, {Name: "other_only", SelectedTests: 1}}
	groups := map[string][]contributionTest{"selected_only": {{ID: "s"}}, "other_only": {{ID: "o"}}}
	paired, cohorts = withholdComparisonAggregates(paired, cohorts, groups, []string{"m"})
	if paired.Status != "inconclusive" || paired.CompleteMutationDenominator != nil || len(paired.SelectedOnlyDetected) != 0 || len(paired.OmittedMutationIDs) != 1 {
		t.Fatalf("paired aggregate survived matrix persistence failure: %+v", paired)
	}
	for _, cohort := range cohorts {
		if cohort.CompleteMutationDenominator != 0 || cohort.DetectedMutations != 0 || cohort.Status != "inconclusive" {
			t.Fatalf("cohort aggregate survived matrix persistence failure: %+v", cohort)
		}
	}
}

func intPointer(v int) *int { return &v }
