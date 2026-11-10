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
	credits := map[string]map[string]bool{}
	for _, change := range changes {
		if change.NewHash == "" {
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

func recordInconclusiveComparisonPair(matrix *comparisonEvidence, validByMutation map[string]bool, pair contributionPair, classification string) {
	pair.Status = "inconclusive"
	pair.Classification = classification
	matrix.Pairs = append(matrix.Pairs, pair)
	validByMutation[pair.MutationID] = false
}

func collectTestContributionComparison(parent context.Context, root, head, artifactPath string, snapshots []string, mutations *mutationReport, per, total time.Duration, repeats int, commits []string, selected identityInfo, identities maps) *testContributionComparison {
	r := &testContributionComparison{Status: "unavailable", MutationIDs: []string{}, Cohorts: []comparisonCohort{}, Limitations: []string{"experimental bounded evidence comparison; not a quality score or causal claim", "tests without safely resolved definition-change history are excluded from both cohorts", "package initialization and TestMain execute for each isolated selector"}}
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
	_, verErr := trackedHeadContributionVersions(ctx, root, head)
	// Keep all current tracked tests in attribution accounting. Worktree-only or
	// modified versions have no matching committed history event and remain unknown.
	matching := all
	if verErr != nil {
		r.HistoryAttributionStatus = "unavailable"
		r.IndeterminateTests = len(matching)
		r.Limitations = append(r.Limitations, "HEAD definition matching unavailable; attribution not evaluated")
		return r
	}
	_, events, partial, reason := resolveContributionHistory(root, commits, selected, identities)
	r.HistoryAttributionStatus = "measured"
	if partial {
		r.HistoryAttributionStatus = "partial"
		r.Limitations = append(r.Limitations, "history attribution incomplete: "+reason)
	}
	groups, shared, unknown := classifyComparisonTests(matching, events, selected, identities)
	r.SharedTests, r.UnattributedTests, r.IndeterminateTests = comparisonAttributionCounts(selected.Available, !partial, shared, unknown, len(matching))
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
	ids := []string{}
	for _, s := range manifest.Sites {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
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
	cohorts := []comparisonCohort{}
	for _, name := range []string{"selected_only", "other_only"} {
		eligibleC := len(groups[name])
		chosen := selectedGroups[name]
		cohorts = append(cohorts, comparisonCohort{Name: name, EligibleTests: eligibleC, SelectedTests: len(chosen), OmittedTests: eligibleC - len(chosen), CompleteMutationDenominatorUnit: "mutation IDs with complete outcomes across every selected test and passing control", Status: "partial"})
	}
	if len(ids) == 0 || len(selectedGroups["selected_only"])+len(selectedGroups["other_only"]) == 0 {
		r.Cohorts = cohorts
		r.Limitations = append(r.Limitations, "no eligible mutation IDs or attributed cohort tests")
		if partial {
			r.Status = "partial"
		}
		return r
	}
	// Pair budget is apportioned deterministically between cohorts. Every pair uses
	// the identical source snapshot and mutation ID list.
	perCohort := comparisonPairLimit / 2
	lookup := map[string]mutationSite{}
	for _, s := range manifest.Sites {
		lookup[s.ID] = s
	}
	started := time.Now()
	matrix := comparisonEvidence{SourceIdentity: manifest.SourceIdentity, TestIdentity: manifest.TestIdentity, MutationIDs: append([]string(nil), ids...), Controls: []contributionControl{}, Pairs: []contributionPair{}}
	for ci, name := range []string{"selected_only", "other_only"} {
		cohort := &cohorts[ci]
		tests := selectedGroups[name]
		pairSlots := perCohort
		if ci == 1 {
			pairSlots = comparisonPairLimit - perCohort
		}
		pairs, _ := comparisonPairs(tests, ids, pairSlots)
		cohort.SelectedPairs = len(pairs)
		cohort.OmittedPairs = len(tests)*len(ids) - len(pairs)
		validControls := map[string]bool{}
		for _, t := range tests {
			outs := []string{}
			var controlEvidence []byte
			for n := 0; n < repeats && time.Since(started) < total; n++ {
				d, e := materializeMutationCopy(entries)
				if e != nil {
					outs = append(outs, "inconclusive")
					continue
				}
				s, _, _, output := executeContributionTest(ctx, d, t, per, false)
				os.RemoveAll(d)
				controlEvidence = append(controlEvidence, output...)
				outs = append(outs, s)
			}
			control := contributionControl{Population: name, TestID: t.ID, Status: stableContributionStatus(outs, repeats), Classification: "comparison_selector_control"}
			if len(controlEvidence) == 0 {
				controlEvidence = []byte("evaluation produced no captured output\\n")
			}
			control.EvidenceArtifact, control.EvidenceSHA256 = persistMutationLog(mutations.artifactDir, mutations.artifactBudget, "comparison-control-", controlEvidence)
			if control.EvidenceArtifact == "" {
				control.Status = "inconclusive"
				control.Classification = "control_evidence_artifact_unavailable"
			}
			matrix.Controls = append(matrix.Controls, control)
			validControls[t.ID] = control.Status == "not_detected"
		}
		validByMutation := map[string]bool{}
		detected := map[string]bool{}
		for _, id := range ids {
			validByMutation[id] = true
		}
		for _, pair := range pairs {
			pair.Population = name
			if !validControls[pair.TestID] {
				pair.Status = "inconclusive"
				pair.Classification = "control_not_valid"
				matrix.Pairs = append(matrix.Pairs, pair)
				validByMutation[pair.MutationID] = false
				continue
			}
			var test contributionTest
			for _, t := range tests {
				if t.ID == pair.TestID {
					test = t
					break
				}
			}
			d, e := materializeMutationCopy(entries)
			if e != nil {
				recordInconclusiveComparisonPair(&matrix, validByMutation, pair, "setup_error")
				continue
			}
			e = applyMutation(d, lookup[pair.MutationID])
			if e != nil {
				os.RemoveAll(d)
				recordInconclusiveComparisonPair(&matrix, validByMutation, pair, "mutation_application_failed")
				continue
			}
			outcomes := []string{}
			var pairEvidence []byte
			for n := 0; n < repeats && time.Since(started) < total; n++ {
				if n > 0 {
					os.RemoveAll(d)
					d, e = materializeMutationCopy(entries)
					if e != nil {
						outcomes = append(outcomes, "inconclusive")
						continue
					}
					if e = applyMutation(d, lookup[pair.MutationID]); e != nil {
						os.RemoveAll(d)
						outcomes = append(outcomes, "inconclusive")
						continue
					}
				}
				status, _, _, output := executeContributionTest(ctx, d, test, per, true)
				pairEvidence = append(pairEvidence, output...)
				outcomes = append(outcomes, status)
			}
			os.RemoveAll(d)
			status := stableContributionStatus(outcomes, repeats)
			pair.Status = status
			pair.Classification = "comparison_pair_outcome"
			if len(pairEvidence) == 0 {
				pairEvidence = []byte("evaluation produced no captured output\\n")
			}
			pair.EvidenceArtifact, pair.EvidenceSHA256 = persistMutationLog(mutations.artifactDir, mutations.artifactBudget, "comparison-pair-", pairEvidence)
			if pair.EvidenceArtifact == "" {
				pair.Status = "inconclusive"
				pair.Classification = "pair_evidence_artifact_unavailable"
				status = "inconclusive"
			}
			matrix.Pairs = append(matrix.Pairs, pair)
			if status != "detected" && status != "not_detected" {
				validByMutation[pair.MutationID] = false
			} else if status == "detected" {
				detected[pair.MutationID] = true
			}
		}
		for _, id := range ids {
			if validByMutation[id] {
				cohort.CompleteMutationDenominator++
				if detected[id] {
					cohort.DetectedMutations++
				}
			} else {
				cohort.IncompleteMutations++
			}
		}
		cohort.Status = "measured"
		if cohort.OmittedTests > 0 || cohort.OmittedPairs > 0 || cohort.IncompleteMutations > 0 || partial {
			cohort.Status = "partial"
		}
		if len(tests) == 0 {
			cohort.Status = "unavailable"
		}
	}
	matrixBytes, matrixErr := json.Marshal(matrix)
	if matrixErr == nil && len(matrixBytes) <= dynamicArtifactBytes {
		r.MatrixArtifact = writeDynamicArtifact(abs, mutations.artifactBudget, "test-contribution-comparison-matrix-", ".json", matrixBytes)
	}
	if r.MatrixArtifact == "" {
		for i := range cohorts {
			cohorts[i].Status = "partial"
			cohorts[i].CompleteMutationDenominator = 0
			cohorts[i].DetectedMutations = 0
			cohorts[i].IncompleteMutations = len(ids)
			cohorts[i].Status = "inconclusive"
		}
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
	if partial || r.OmittedTests > 0 || (r.UnattributedTests != nil && *r.UnattributedTests > 0) || (r.SharedTests != nil && *r.SharedTests > 0) || r.IndeterminateTests > 0 {
		r.Status = "partial"
	}
	return r
}
