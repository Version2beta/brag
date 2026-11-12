package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const comparisonTestLimit = 200
const comparisonPairLimit = 1000

type comparisonEvidence struct {
	SourceIdentity string                `json:"source_identity"`
	TestIdentity   string                `json:"test_identity"`
	MutationIDs    []string              `json:"mutation_ids"`
	Controls       []contributionControl `json:"controls"`
	Pairs          []contributionPair    `json:"pairs"`
}

type comparisonCohort struct {
	Name                            string `json:"name"`
	EligibleTests                   int    `json:"eligible_tests"`
	SelectedTests                   int    `json:"selected_tests"`
	OmittedTests                    int    `json:"omitted_tests"`
	SelectedPairs                   int    `json:"selected_pairs"`
	OmittedPairs                    int    `json:"omitted_pairs"`
	CompleteMutationDenominator     int    `json:"complete_mutation_denominator"`
	CompleteMutationDenominatorUnit string `json:"complete_mutation_denominator_unit"`
	DetectedMutations               int    `json:"detected_mutations"`
	IncompleteMutations             int    `json:"incomplete_mutations"`
	Status                          string `json:"status"`
}

type pairedComparison struct {
	Status                          string              `json:"status"`
	CompleteMutationDenominatorUnit string              `json:"complete_mutation_denominator_unit"`
	EligibleMutationIDs             []string            `json:"eligible_mutation_ids"`
	SelectedMutationIDs             []string            `json:"selected_mutation_ids"`
	PairedMutationIDs               []string            `json:"paired_mutation_ids"`
	OmittedMutationIDs              []string            `json:"omitted_mutation_ids"`
	OmissionReasons                 map[string][]string `json:"omission_reasons"`
	CompleteMutationDenominator     *int                `json:"complete_mutation_denominator"`
	SelectedOnlyDetected            []string            `json:"selected_only_detected"`
	OtherOnlyDetected               []string            `json:"other_only_detected"`
	BothDetected                    []string            `json:"both_detected"`
	NeitherDetected                 []string            `json:"neither_detected"`
}

type testContributionComparison struct {
	Status                   string             `json:"status"`
	SourceIdentity           string             `json:"source_identity,omitempty"`
	MutationIDs              []string           `json:"mutation_ids"`
	EligibleMutations        int                `json:"eligible_mutations"`
	OmittedMutations         int                `json:"omitted_mutations"`
	EligibleTests            int                `json:"eligible_tests"`
	SelectedTests            int                `json:"selected_tests"`
	OmittedTests             int                `json:"omitted_tests"`
	SharedTests              *int               `json:"shared_tests"`
	UnattributedTests        *int               `json:"unattributed_tests"`
	IndeterminateTests       int                `json:"indeterminate_tests"`
	HistoryAttributionStatus string             `json:"history_attribution_status"`
	MatrixArtifact           string             `json:"matrix_artifact,omitempty"`
	MatrixSHA256             string             `json:"matrix_sha256,omitempty"`
	Cohorts                  []comparisonCohort `json:"cohorts"`
	Paired                   pairedComparison   `json:"paired_comparison"`
	Limitations              []string           `json:"limitations"`
}

// classifyComparisonTests uses only recorded definition-change credits. Definitions
// without resolvable in-scope history remain unattributed, never peer tests.
func comparisonAttributionCounts(identityAvailable, historyComplete bool, shared, unattributed, currentTests int) (*int, *int, int) {
	if !identityAvailable || !historyComplete {
		return nil, nil, currentTests
	}
	return &shared, &unattributed, 0
}

func classifyComparisonTests(tests []contributionTest, changes []contributionDefinitionChange, selected identityInfo, identities maps) (map[string][]contributionTest, int, int) {
	selectedEmail := ""
	if selected.Available {
		selectedEmail = resolve(selected, identities).Email
	}
	// Resolver events are chronological. Only the latest state transition for a
	// definition key can establish current provenance; earlier recurring hashes
	// must not union their author credits into the current cohort.
	latest := map[string]contributionDefinitionChange{}
	for _, change := range changes {
		latest[change.Key] = change
	}
	credits := map[string]map[string]bool{}
	for _, change := range latest {
		if change.NewHash == "" || change.Status == "removed" {
			continue
		}
		key := change.Key + "\x00" + change.NewHash
		if credits[key] == nil {
			credits[key] = map[string]bool{}
		}
		for _, email := range change.Credits {
			credits[key][resolve(identityInfo{Name: email, Email: email, Available: true}, identities).Email] = true
		}
	}
	groups := map[string][]contributionTest{"selected_only": {}, "other_only": {}}
	shared, unknown := 0, 0
	for _, test := range tests {
		key := test.Package + "\x00" + test.File + "/test/" + test.Name + "\x00" + test.DeclarationSHA256
		people, found := credits[key]
		if !found || len(people) == 0 || selectedEmail == "" {
			unknown++
			continue
		}
		hasSelected := people[selectedEmail]
		hasOther := false
		for email := range people {
			if email != selectedEmail {
				hasOther = true
			}
		}
		switch {
		case hasSelected && hasOther:
			shared++
		case hasSelected:
			groups["selected_only"] = append(groups["selected_only"], test)
		case hasOther:
			groups["other_only"] = append(groups["other_only"], test)
		default:
			unknown++
		}
	}
	return groups, shared, unknown
}

// selectComparisonTests alternates cohorts and round-robins their sorted packages.
func selectComparisonTests(groups map[string][]contributionTest, limit int) map[string][]contributionTest {
	out := map[string][]contributionTest{"selected_only": {}, "other_only": {}}
	if limit <= 0 {
		return out
	}
	cohorts := []string{"selected_only", "other_only"}
	byCohortPackage := map[string]map[string][]contributionTest{}
	packages := map[string][]string{}
	positions := map[string]map[string]int{}
	packagePositions := map[string]int{}
	for _, cohort := range cohorts {
		byCohortPackage[cohort] = map[string][]contributionTest{}
		positions[cohort] = map[string]int{}
		for _, test := range groups[cohort] {
			byCohortPackage[cohort][test.Package] = append(byCohortPackage[cohort][test.Package], test)
		}
		for pkg, tests := range byCohortPackage[cohort] {
			packages[cohort] = append(packages[cohort], pkg)
			sort.Slice(tests, func(i, j int) bool { return tests[i].ID < tests[j].ID })
		}
		sort.Strings(packages[cohort])
	}
	for len(out[cohorts[0]])+len(out[cohorts[1]]) < limit {
		added := false
		for _, cohort := range cohorts {
			if len(out[cohorts[0]])+len(out[cohorts[1]]) >= limit {
				break
			}
			pkgs := packages[cohort]
			for checked := 0; checked < len(pkgs); checked++ {
				idx := packagePositions[cohort] % len(pkgs)
				pkg := pkgs[idx]
				packagePositions[cohort] = (idx + 1) % len(pkgs)
				if positions[cohort][pkg] < len(byCohortPackage[cohort][pkg]) {
					out[cohort] = append(out[cohort], byCohortPackage[cohort][pkg][positions[cohort][pkg]])
					positions[cohort][pkg]++
					added = true
					break
				}
			}
			if len(out[cohorts[0]])+len(out[cohorts[1]]) >= limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return out
}

func comparisonPairs(tests []contributionTest, ids []string, limit int) ([]contributionPair, int) {
	byPackage := map[string][]contributionTest{}
	packages := []string{}
	for _, test := range tests {
		if _, ok := byPackage[test.Package]; !ok {
			packages = append(packages, test.Package)
		}
		byPackage[test.Package] = append(byPackage[test.Package], test)
	}
	sort.Strings(packages)
	pairs := []contributionPair{}
	for _, mutationID := range ids {
		for round := 0; ; round++ {
			added := false
			for _, pkg := range packages {
				if round < len(byPackage[pkg]) && len(pairs) < limit {
					test := byPackage[pkg][round]
					pairs = append(pairs, contributionPair{Population: "cohort", TestID: test.ID, MutationID: mutationID, Status: "not_executed"})
					added = true
				}
			}
			if !added || len(pairs) >= limit {
				break
			}
		}
		if len(pairs) >= limit {
			break
		}
	}
	return pairs, len(tests)*len(ids) - len(pairs)
}

// reduceComparisonCohort validates persisted evidence independently for one cohort.
func validComparisonEvidence(artifact, digest string) bool {
	if artifact == "" || filepath.Clean(artifact) != artifact || artifact == "." || artifact == ".." || len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func reduceComparisonCohort(matrix comparisonEvidence, population string, tests []contributionTest, ids []string) (map[string]bool, map[string]bool) {
	complete, detected := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		complete[id] = true
	}
	matrixMatchesIDs := len(matrix.MutationIDs) == len(ids)
	if matrixMatchesIDs {
		for i, id := range ids {
			if matrix.MutationIDs[i] != id {
				matrixMatchesIDs = false
				break
			}
		}
	}
	if !matrixMatchesIDs {
		for _, id := range ids {
			complete[id] = false
		}
	}
	controls := map[string]int{}
	controlOK := map[string]bool{}
	knownTestIDs := map[string]bool{}
	for _, t := range tests {
		knownTestIDs[t.ID] = true
	}
	for _, c := range matrix.Controls {
		if c.Population == population {
			if !knownTestIDs[c.TestID] {
				for _, id := range ids {
					complete[id] = false
				}
				continue
			}
			controls[c.TestID]++
			controlOK[c.TestID] = c.Status == "not_detected" && validComparisonEvidence(c.EvidenceArtifact, c.EvidenceSHA256)
		}
	}
	for _, t := range tests {
		if controls[t.ID] != 1 || !controlOK[t.ID] {
			for _, id := range ids {
				complete[id] = false
			}
		}
	}
	pairs := map[string]int{}
	knownTests := map[string]bool{}
	knownMutations := map[string]bool{}
	for _, t := range tests {
		knownTests[t.ID] = true
	}
	for _, id := range ids {
		knownMutations[id] = true
	}
	for _, p := range matrix.Pairs {
		if p.Population != population {
			continue
		}
		if !knownTests[p.TestID] || !knownMutations[p.MutationID] {
			for _, id := range ids {
				complete[id] = false
			}
			continue
		}
		key := p.TestID + "\x00" + p.MutationID
		pairs[key]++
		if p.Status != "detected" && p.Status != "not_detected" || !validComparisonEvidence(p.EvidenceArtifact, p.EvidenceSHA256) {
			complete[p.MutationID] = false
		}
		if p.Status == "detected" {
			detected[p.MutationID] = true
		}
	}
	for _, t := range tests {
		for _, id := range ids {
			key := t.ID + "\x00" + id
			if pairs[key] != 1 {
				complete[id] = false
			}
		}
	}
	for _, id := range ids {
		if len(tests) == 0 {
			complete[id] = false
		}
	}
	return complete, detected
}

func summarizeComparisonCohort(matrix comparisonEvidence, cohort comparisonCohort, tests []contributionTest, ids []string, historyPartial bool) comparisonCohort {
	cohort.CompleteMutationDenominator = 0
	cohort.DetectedMutations = 0
	cohort.IncompleteMutations = 0
	if len(tests) == 0 {
		cohort.Status = "unavailable"
		return cohort
	}
	complete, detected := reduceComparisonCohort(matrix, cohort.Name, tests, ids)
	for _, id := range ids {
		if complete[id] {
			cohort.CompleteMutationDenominator++
			if detected[id] {
				cohort.DetectedMutations++
			}
		} else {
			cohort.IncompleteMutations++
		}
	}
	if cohort.IncompleteMutations > 0 || cohort.OmittedTests > 0 || cohort.OmittedPairs > 0 || historyPartial {
		cohort.Status = "partial"
	} else {
		cohort.Status = "measured"
	}
	return cohort
}

func reducePairedComparison(matrix comparisonEvidence, eligibleIDs, selectedIDs []string, selectedTests, otherTests []contributionTest) pairedComparison {
	result := pairedComparison{
		Status:                          "unavailable",
		CompleteMutationDenominatorUnit: "mutation IDs with complete outcomes for every selected test and passing control in both cohorts",
		EligibleMutationIDs:             append([]string(nil), eligibleIDs...),
		SelectedMutationIDs:             append([]string(nil), selectedIDs...),
		PairedMutationIDs:               []string{},
		OmittedMutationIDs:              []string{},
		OmissionReasons:                 map[string][]string{},
		SelectedOnlyDetected:            []string{},
		OtherOnlyDetected:               []string{},
		BothDetected:                    []string{},
		NeitherDetected:                 []string{},
	}
	selectedSet := map[string]bool{}
	for _, id := range selectedIDs {
		selectedSet[id] = true
	}
	for _, id := range eligibleIDs {
		reasons := []string{}
		if !selectedSet[id] {
			reasons = append(reasons, "comparison_pair_limit")
		}
		if len(selectedTests) == 0 || len(otherTests) == 0 {
			reasons = append(reasons, "cohort_unavailable")
		}
		if len(reasons) > 0 {
			result.OmittedMutationIDs = append(result.OmittedMutationIDs, id)
			result.OmissionReasons[id] = reasons
		}
	}
	if len(eligibleIDs) == 0 || len(selectedIDs) == 0 || len(selectedTests) == 0 || len(otherTests) == 0 {
		return result
	}

	selectedComplete, selectedDetected := reduceComparisonCohort(matrix, "selected_only", selectedTests, selectedIDs)
	otherComplete, otherDetected := reduceComparisonCohort(matrix, "other_only", otherTests, selectedIDs)
	denominator := 0
	result.CompleteMutationDenominator = &denominator
	result.Status = "partial"
	for _, id := range selectedIDs {
		if selectedComplete[id] && otherComplete[id] {
			*result.CompleteMutationDenominator++
			result.PairedMutationIDs = append(result.PairedMutationIDs, id)
			a, b := selectedDetected[id], otherDetected[id]
			switch {
			case a && b:
				result.BothDetected = append(result.BothDetected, id)
			case a:
				result.SelectedOnlyDetected = append(result.SelectedOnlyDetected, id)
			case b:
				result.OtherOnlyDetected = append(result.OtherOnlyDetected, id)
			default:
				result.NeitherDetected = append(result.NeitherDetected, id)
			}
			continue
		}
		reasons := []string{}
		if !selectedComplete[id] {
			reasons = append(reasons, "selected_only_incomplete")
		}
		if !otherComplete[id] {
			reasons = append(reasons, "other_only_incomplete")
		}
		result.OmittedMutationIDs = append(result.OmittedMutationIDs, id)
		result.OmissionReasons[id] = reasons
	}
	if *result.CompleteMutationDenominator == len(eligibleIDs) && len(result.OmittedMutationIDs) == 0 {
		result.Status = "measured"
	}
	return result
}

func withholdPairedComparison(paired pairedComparison) pairedComparison {
	paired.Status = "inconclusive"
	paired.CompleteMutationDenominator = nil
	paired.PairedMutationIDs = []string{}
	paired.OmittedMutationIDs = append([]string(nil), paired.EligibleMutationIDs...)
	paired.OmissionReasons = map[string][]string{}
	for _, id := range paired.OmittedMutationIDs {
		paired.OmissionReasons[id] = []string{"matrix_persistence_failed"}
	}
	paired.SelectedOnlyDetected = []string{}
	paired.OtherOnlyDetected = []string{}
	paired.BothDetected = []string{}
	paired.NeitherDetected = []string{}
	return paired
}

func withholdComparisonAggregates(paired pairedComparison, cohorts []comparisonCohort, selectedGroups map[string][]contributionTest, ids []string) (pairedComparison, []comparisonCohort) {
	paired = withholdPairedComparison(paired)
	for i, name := range []string{"selected_only", "other_only"} {
		cohorts[i].CompleteMutationDenominator = 0
		cohorts[i].DetectedMutations = 0
		if len(selectedGroups[name]) == 0 {
			cohorts[i].IncompleteMutations = 0
			cohorts[i].Status = "unavailable"
			continue
		}
		cohorts[i].IncompleteMutations = len(ids)
		cohorts[i].Status = "inconclusive"
	}
	return paired, cohorts
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func manifestIDs(manifest mutationManifest) []string {
	ids := make([]string, 0, len(manifest.Sites))
	for _, site := range manifest.Sites {
		ids = append(ids, site.ID)
	}
	sort.Strings(ids)
	return ids
}

type comparisonControlTask struct {
	Population string
	Test       contributionTest
	Reserve    bool
}

type comparisonPairTask struct {
	Population string
	Pair       contributionPair
	Reserve    bool
}

func comparisonControlRounds(groups map[string][]contributionTest) [][]comparisonControlTask {
	cohorts := []string{"selected_only", "other_only"}
	maxTests := max(len(groups[cohorts[0]]), len(groups[cohorts[1]]))
	rounds := make([][]comparisonControlTask, 0, maxTests)
	for i := 0; i < maxTests; i++ {
		first := i % 2
		round := []comparisonControlTask{}
		for step := 0; step < 2; step++ {
			ci := (first + step) % 2
			if i < len(groups[cohorts[ci]]) {
				round = append(round, comparisonControlTask{Population: cohorts[ci], Test: groups[cohorts[ci]][i]})
			}
		}
		if len(round) == 2 {
			round[0].Reserve = true
		}
		rounds = append(rounds, round)
	}
	return rounds
}

func comparisonPairRounds(ids []string, selected, other []contributionPair) [][]comparisonPairTask {
	byCohort := map[string]map[string][]contributionPair{
		"selected_only": {},
		"other_only":    {},
	}
	for _, pair := range selected {
		byCohort["selected_only"][pair.MutationID] = append(byCohort["selected_only"][pair.MutationID], pair)
	}
	for _, pair := range other {
		byCohort["other_only"][pair.MutationID] = append(byCohort["other_only"][pair.MutationID], pair)
	}
	rounds := [][]comparisonPairTask{}
	for mutationIndex, id := range ids {
		a, b := byCohort["selected_only"][id], byCohort["other_only"][id]
		for testIndex := 0; testIndex < max(len(a), len(b)); testIndex++ {
			first := (mutationIndex + testIndex) % 2
			round := []comparisonPairTask{}
			for step := 0; step < 2; step++ {
				ci := (first + step) % 2
				population := []string{"selected_only", "other_only"}[ci]
				pairs := a
				if ci == 1 {
					pairs = b
				}
				if testIndex < len(pairs) {
					round = append(round, comparisonPairTask{Population: population, Pair: pairs[testIndex]})
				}
			}
			if len(round) == 2 {
				round[0].Reserve = true
			}
			rounds = append(rounds, round)
		}
	}
	return rounds
}

// comparisonFairBudget reserves time for the counterpart task in a paired round.
func comparisonFairBudget(remaining time.Duration, reserveCounterpart bool) time.Duration {
	if remaining <= 0 {
		return 0
	}
	if reserveCounterpart {
		return remaining / 2
	}
	return remaining
}

func comparisonTaskContext(parent context.Context, reserveCounterpart bool) (context.Context, context.CancelFunc, bool) {
	if !reserveCounterpart {
		return parent, func() {}, false
	}
	deadline, ok := parent.Deadline()
	if !ok {
		return parent, func() {}, false
	}
	budget := comparisonFairBudget(time.Until(deadline), true)
	task, cancel := context.WithTimeout(parent, budget)
	return task, cancel, true
}

func comparisonInterruption(parent, task context.Context, reserved bool) string {
	if parent.Err() == context.Canceled {
		return "caller_cancellation"
	}
	if parent.Err() != nil {
		return "total_contribution_budget"
	}
	if reserved && task.Err() != nil {
		return "fair_share_budget_exhausted"
	}
	if task.Err() != nil {
		return "evaluation_timeout"
	}
	return "not_executed"
}

func executeComparisonControl(ctx context.Context, task comparisonControlTask, entries []snapshotEntry, repeats int, per time.Duration, artifactDir string, budget *artifactBudget) contributionControl {
	taskCtx, cancel, reserved := comparisonTaskContext(ctx, task.Reserve)
	if cancel != nil {
		defer cancel()
	}
	control := contributionControl{Population: task.Population, TestID: task.Test.ID, Status: "not_executed", Classification: "total_contribution_budget"}
	outcomes, classifications := []string{}, []string{}
	var evidence []byte
	for repeat := 0; repeat < repeats && taskCtx.Err() == nil; repeat++ {
		dir, err := materializeMutationCopy(entries)
		if err != nil {
			outcomes = append(outcomes, "inconclusive")
			classifications = append(classifications, "control_setup_error")
			continue
		}
		if taskCtx.Err() != nil {
			_ = os.RemoveAll(dir)
			break
		}
		status, classification, _, output := executeContributionTest(taskCtx, dir, task.Test, per, false)
		_ = os.RemoveAll(dir)
		outcomes = append(outcomes, status)
		classifications = append(classifications, classification)
		evidence = append(evidence, output...)
	}
	if len(outcomes) == 0 {
		control.Classification = comparisonInterruption(ctx, taskCtx, reserved)
	} else {
		control.Status = stableContributionStatus(outcomes, repeats)
		switch {
		case control.Status == "unstable":
			control.Classification = "repeats_differ"
		case len(outcomes) < repeats && taskCtx.Err() != nil:
			control.Classification = comparisonInterruption(ctx, taskCtx, reserved)
		case len(classifications) > 0:
			control.Classification = classifications[len(classifications)-1]
		default:
			control.Classification = "comparison_selector_control"
		}
	}
	if len(evidence) > 0 {
		control.EvidenceArtifact, control.EvidenceSHA256 = persistMutationLog(artifactDir, budget, "comparison-control-", evidence)
		if control.EvidenceArtifact == "" {
			control.Status = "inconclusive"
			control.Classification = "control_evidence_artifact_unavailable"
		}
	} else if control.Status == "not_detected" || control.Status == "detected" {
		control.Status = "inconclusive"
		control.Classification = "control_evidence_missing"
	}
	return control
}

func executeComparisonPair(ctx context.Context, task comparisonPairTask, test contributionTest, site mutationSite, control contributionControl, entries []snapshotEntry, repeats int, per time.Duration, artifactDir string, budget *artifactBudget) contributionPair {
	pair := task.Pair
	pair.Population = task.Population
	if control.Status != "not_detected" {
		if ctx.Err() != nil {
			pair.Status = "not_executed"
			pair.Classification = comparisonInterruption(ctx, ctx, false)
		} else if control.Status == "not_executed" {
			pair.Status = "not_executed"
			pair.Classification = control.Classification
		} else {
			pair.Status = "inconclusive"
			pair.Classification = "control_not_valid"
			if control.Classification != "" {
				pair.Classification += ":" + control.Classification
			}
		}
		return pair
	}
	taskCtx, cancel, reserved := comparisonTaskContext(ctx, task.Reserve)
	if cancel != nil {
		defer cancel()
	}
	outcomes, classifications := []string{}, []string{}
	var evidence []byte
	for repeat := 0; repeat < repeats && taskCtx.Err() == nil; repeat++ {
		dir, err := materializeMutationCopy(entries)
		if err != nil {
			outcomes = append(outcomes, "inconclusive")
			classifications = append(classifications, "snapshot_setup_error")
			continue
		}
		if err = applyMutation(dir, site); err != nil {
			_ = os.RemoveAll(dir)
			outcomes = append(outcomes, "inconclusive")
			classifications = append(classifications, "mutation_application_failed")
			continue
		}
		if taskCtx.Err() != nil {
			_ = os.RemoveAll(dir)
			break
		}
		status, classification, elapsed, output := executeContributionTest(taskCtx, dir, test, per, true)
		_ = os.RemoveAll(dir)
		pair.ElapsedMillis += elapsed
		outcomes = append(outcomes, status)
		classifications = append(classifications, classification)
		evidence = append(evidence, output...)
	}
	if len(outcomes) == 0 {
		pair.Status = "not_executed"
		pair.Classification = comparisonInterruption(ctx, taskCtx, reserved)
	} else {
		pair.Status = stableContributionStatus(outcomes, repeats)
		switch {
		case pair.Status == "unstable":
			pair.Classification = "repeats_differ"
		case len(outcomes) < repeats && taskCtx.Err() != nil:
			pair.Classification = comparisonInterruption(ctx, taskCtx, reserved)
		case len(classifications) > 0:
			pair.Classification = classifications[len(classifications)-1]
		default:
			pair.Classification = "comparison_pair_outcome"
		}
	}
	if len(evidence) > 0 {
		pair.EvidenceArtifact, pair.EvidenceSHA256 = persistMutationLog(artifactDir, budget, "comparison-pair-", evidence)
		if pair.EvidenceArtifact == "" {
			pair.Status = "inconclusive"
			pair.Classification = "pair_evidence_artifact_unavailable"
		}
	} else if pair.Status == "detected" || pair.Status == "not_detected" {
		pair.Status = "inconclusive"
		pair.Classification = "pair_evidence_missing"
	}
	return pair
}

func recordInconclusiveComparisonPair(matrix *comparisonEvidence, validByMutation map[string]bool, pair contributionPair, classification string) {
	pair.Status = "inconclusive"
	pair.Classification = classification
	matrix.Pairs = append(matrix.Pairs, pair)
	validByMutation[pair.MutationID] = false
}

func collectTestContributionComparison(parent context.Context, root, head, artifactPath string, snapshots []string, mutations *mutationReport, per, total time.Duration, repeats int, commits []string, selected identityInfo, identities maps) *testContributionComparison {
	r := &testContributionComparison{Status: "unavailable", MutationIDs: []string{}, Cohorts: []comparisonCohort{}, Paired: pairedComparison{Status: "unavailable", CompleteMutationDenominatorUnit: "mutation IDs complete in both cohorts", EligibleMutationIDs: []string{}, SelectedMutationIDs: []string{}, PairedMutationIDs: []string{}, OmittedMutationIDs: []string{}, OmissionReasons: map[string][]string{}, SelectedOnlyDetected: []string{}, OtherOnlyDetected: []string{}, BothDetected: []string{}, NeitherDetected: []string{}}, Limitations: []string{"experimental bounded evidence comparison; not a quality score or causal claim", "tests without safely resolved definition-change history are excluded from both cohorts", "package initialization and TestMain execute for each isolated selector", "test package relevance to mutation sites is not matched; deterministic bounded test selection and lowest-ID mutation selection are not representative or stratified by operator/package, nor equal measures of responsibility or difficulty"}}
	if mutations == nil || len(mutations.Snapshots) != 1 {
		r.Limitations = append(r.Limitations, "exactly one mutation snapshot is required")
		return r
	}
	ms := mutations.Snapshots[0]
	if ms.ManifestArtifact == "" || ms.ManifestSHA256 == "" || mutations.artifactBudget == nil {
		r.Limitations = append(r.Limitations, "mutation manifest or artifact budget unavailable")
		return r
	}
	abs, err := canonicalCandidate(root, artifactPath)
	if err != nil {
		return r
	}
	b, err := os.ReadFile(filepath.Join(abs, filepath.Base(ms.ManifestArtifact)))
	if err != nil || hashBytes(b) != ms.ManifestSHA256 {
		r.Limitations = append(r.Limitations, "mutation manifest hash verification failed")
		return r
	}
	var manifest mutationManifest
	if json.Unmarshal(b, &manifest) != nil || manifest.SourceIdentity != ms.SourceIdentity || manifest.Snapshot != "current_tracked_worktree" || manifest.TestIdentity != ms.TestIdentity {
		r.Limitations = append(r.Limitations, "mutation manifest identity mismatch")
		return r
	}
	manifestMutationIDs := manifestIDs(manifest)
	r.EligibleMutations = len(manifestMutationIDs)
	r.Paired.EligibleMutationIDs = append([]string(nil), manifestMutationIDs...)
	sp := snapshotSpec{"current_tracked_worktree", head, true}
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	dir, _, err := materializeDynamicSnapshotContext(ctx, root, sp)
	if err != nil {
		r.Limitations = append(r.Limitations, "current snapshot unavailable")
		return r
	}
	entries, _, err := snapshotEntriesForMutation(dir)
	os.RemoveAll(dir)
	if err != nil || mutationSourceIdentity(entries, false) != manifest.SourceIdentity || mutationSourceIdentity(entries, true) != manifest.TestIdentity {
		r.Limitations = append(r.Limitations, "current source identity does not match mutation manifest")
		return r
	}
	r.SourceIdentity = manifest.SourceIdentity
	all := contributionTests(entries)
	r.EligibleTests = len(all)
	headVersions, verErr := trackedHeadContributionVersions(ctx, root, head)
	if verErr != nil {
		r.HistoryAttributionStatus = "unavailable"
		r.EligibleTests = 0
		r.IndeterminateTests = len(all)
		r.OmittedMutations = r.EligibleMutations
		r.Paired.OmittedMutationIDs = append([]string(nil), manifestMutationIDs...)
		for _, id := range manifestMutationIDs {
			r.Paired.OmissionReasons[id] = []string{"head_definition_matching_unavailable"}
		}
		r.Limitations = append(r.Limitations, "HEAD definition matching unavailable; attribution not evaluated")
		return r
	}
	matching := make([]contributionTest, 0, len(all))
	unmatched := 0
	for _, test := range all {
		key := test.Package + "\x00" + test.File + "/test/" + test.Name + "\x00" + test.DeclarationSHA256
		if headVersions[key] {
			matching = append(matching, test)
		} else {
			unmatched++
		}
	}
	_, events, partial, reason := resolveContributionHistory(root, commits, selected, identities, contributionCandidatePaths(matching))
	r.HistoryAttributionStatus = "measured"
	if partial {
		r.HistoryAttributionStatus = "partial"
		r.Limitations = append(r.Limitations, "history attribution incomplete: "+reason)
	}
	groups, shared, unknown := classifyComparisonTests(matching, events, selected, identities)
	unknown += unmatched
	r.SharedTests, r.UnattributedTests, r.IndeterminateTests = comparisonAttributionCounts(selected.Available, !partial, shared, unknown, len(all))
	if partial || !selected.Available {
		// Incomplete history or missing identity prevents a complete cohort assignment.
		groups = map[string][]contributionTest{"selected_only": {}, "other_only": {}}
	}
	eligible := len(groups["selected_only"]) + len(groups["other_only"])
	r.EligibleTests = eligible
	selectedGroups := selectComparisonTests(groups, comparisonTestLimit)
	selectedTotal := len(selectedGroups["selected_only"]) + len(selectedGroups["other_only"])
	r.SelectedTests = selectedTotal
	r.OmittedTests = eligible - selectedTotal
	ids := append([]string(nil), manifestMutationIDs...)
	r.EligibleMutations = len(ids)
	sharedIDCount := len(ids)
	for _, name := range []string{"selected_only", "other_only"} {
		n := len(selectedGroups[name])
		if n > 0 && comparisonPairLimit/2/n < sharedIDCount {
			sharedIDCount = comparisonPairLimit / 2 / n
		}
	}
	if sharedIDCount > len(ids) {
		sharedIDCount = len(ids)
	}
	ids = ids[:sharedIDCount]
	r.MutationIDs = ids
	r.OmittedMutations = r.EligibleMutations - len(ids)
	r.Paired.SelectedMutationIDs = append([]string(nil), ids...)
	for _, id := range r.Paired.EligibleMutationIDs {
		if !containsString(ids, id) {
			r.Paired.OmittedMutationIDs = append(r.Paired.OmittedMutationIDs, id)
			r.Paired.OmissionReasons[id] = []string{"comparison_pair_limit"}
		}
	}
	cohorts := []comparisonCohort{}
	for _, name := range []string{"selected_only", "other_only"} {
		eligibleC := len(groups[name])
		chosen := selectedGroups[name]
		cohorts = append(cohorts, comparisonCohort{Name: name, EligibleTests: eligibleC, SelectedTests: len(chosen), OmittedTests: eligibleC - len(chosen), CompleteMutationDenominatorUnit: "mutation IDs with complete outcomes across every selected test and passing control", Status: "partial"})
	}
	if len(ids) == 0 || len(selectedGroups["selected_only"])+len(selectedGroups["other_only"]) == 0 {
		r.Paired = reducePairedComparison(comparisonEvidence{MutationIDs: ids}, r.Paired.EligibleMutationIDs, ids, selectedGroups["selected_only"], selectedGroups["other_only"])
		if !selected.Available || partial {
			reason := "history_attribution_incomplete"
			if !selected.Available {
				reason = "selected_identity_unavailable"
			}
			r.Paired.Status = "unavailable"
			r.Paired.CompleteMutationDenominator = nil
			for _, id := range r.Paired.EligibleMutationIDs {
				r.Paired.OmissionReasons[id] = []string{reason}
			}
		}
		for i, name := range []string{"selected_only", "other_only"} {
			if len(selectedGroups[name]) == 0 {
				cohorts[i].Status = "unavailable"
			}
		}
		r.Cohorts = cohorts
		r.Limitations = append(r.Limitations, "no eligible mutation IDs or attributed cohort tests")
		if partial || !selected.Available {
			r.Status = "partial"
		}
		return r
	}
	// Prepare both cohort populations first so controls and pair evaluations can
	// be interleaved under the same deadline without changing their denominators.
	perCohort := comparisonPairLimit / 2
	pairLists := map[string][]contributionPair{"selected_only": {}, "other_only": {}}
	for ci, name := range []string{"selected_only", "other_only"} {
		pairSlots := perCohort
		if ci == 1 {
			pairSlots = comparisonPairLimit - perCohort
		}
		pairs, omitted := comparisonPairs(selectedGroups[name], ids, pairSlots)
		for i := range pairs {
			pairs[i].Population = name
		}
		pairLists[name] = pairs
		cohorts[ci].SelectedPairs = len(pairs)
		cohorts[ci].OmittedPairs = omitted
	}
	lookup := map[string]mutationSite{}
	for _, site := range manifest.Sites {
		lookup[site.ID] = site
	}
	matrix := comparisonEvidence{SourceIdentity: manifest.SourceIdentity, TestIdentity: manifest.TestIdentity, MutationIDs: append([]string(nil), ids...), Controls: []contributionControl{}, Pairs: []contributionPair{}}
	controlStatus := map[string]map[string]contributionControl{"selected_only": {}, "other_only": {}}
	for _, round := range comparisonControlRounds(selectedGroups) {
		for _, task := range round {
			control := executeComparisonControl(ctx, task, entries, repeats, per, mutations.artifactDir, mutations.artifactBudget)
			matrix.Controls = append(matrix.Controls, control)
			controlStatus[task.Population][task.Test.ID] = control
		}
	}
	for _, round := range comparisonPairRounds(ids, pairLists["selected_only"], pairLists["other_only"]) {
		for _, task := range round {
			var test contributionTest
			for _, candidate := range selectedGroups[task.Population] {
				if candidate.ID == task.Pair.TestID {
					test = candidate
					break
				}
			}
			site, ok := lookup[task.Pair.MutationID]
			pair := task.Pair
			if !ok || test.ID == "" {
				pair.Population = task.Population
				pair.Status = "inconclusive"
				pair.Classification = "scheduled_pair_identity_missing"
			} else {
				pair = executeComparisonPair(ctx, task, test, site, controlStatus[task.Population][test.ID], entries, repeats, per, mutations.artifactDir, mutations.artifactBudget)
			}
			matrix.Pairs = append(matrix.Pairs, pair)
		}
	}
	for ci, name := range []string{"selected_only", "other_only"} {
		cohorts[ci] = summarizeComparisonCohort(matrix, cohorts[ci], selectedGroups[name], ids, partial)
	}
	r.Paired = reducePairedComparison(matrix, r.Paired.EligibleMutationIDs, ids, selectedGroups["selected_only"], selectedGroups["other_only"])
	matrixBytes, matrixErr := json.Marshal(matrix)
	if matrixErr == nil && len(matrixBytes) <= dynamicArtifactBytes {
		r.MatrixArtifact = writeDynamicArtifact(abs, mutations.artifactBudget, "test-contribution-comparison-matrix-", ".json", matrixBytes)
	}
	if r.MatrixArtifact == "" {
		r.Paired, cohorts = withholdComparisonAggregates(r.Paired, cohorts, selectedGroups, ids)
		r.Limitations = append(r.Limitations, "comparison matrix evidence could not be persisted; measured aggregates withheld")
	} else {
		r.MatrixSHA256 = hashBytes(matrixBytes)
	}
	r.Cohorts = cohorts
	r.Status = "measured"
	for _, c := range cohorts {
		if c.Status != "measured" {
			r.Status = "partial"
		}
	}
	if r.Paired.Status != "measured" || partial || r.OmittedTests > 0 || (r.UnattributedTests != nil && *r.UnattributedTests > 0) || (r.SharedTests != nil && *r.SharedTests > 0) || r.IndeterminateTests > 0 {
		r.Status = "partial"
	}
	return r
}
