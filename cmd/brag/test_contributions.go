package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const contributionTestLimit = 200
const contributionPairLimit = 1000

type contributionTest struct {
	ID                string `json:"id"`
	Package           string `json:"package"`
	File              string `json:"file"`
	Name              string `json:"name"`
	DeclarationSHA256 string `json:"declaration_sha256"`
}
type contributionControl struct {
	Population       string `json:"population"`
	TestID           string `json:"test_id"`
	Status           string `json:"status"`
	Classification   string `json:"classification"`
	EvidenceArtifact string `json:"evidence_artifact,omitempty"`
	EvidenceSHA256   string `json:"evidence_sha256,omitempty"`
}
type contributionPair struct {
	Population       string `json:"population"`
	TestID           string `json:"test_id"`
	MutationID       string `json:"mutation_id"`
	Status           string `json:"status"`
	Classification   string `json:"classification"`
	ElapsedMillis    int64  `json:"elapsed_millis"`
	EvidenceArtifact string `json:"evidence_artifact,omitempty"`
	EvidenceSHA256   string `json:"evidence_sha256,omitempty"`
}
type contributionDefinitionChange struct {
	Key     string   `json:"key"`
	Status  string   `json:"status"`
	OldHash string   `json:"old_hash,omitempty"`
	NewHash string   `json:"new_hash,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Author  string   `json:"author_group,omitempty"`
	Credits []string `json:"credits,omitempty"`
}
type contributionGroup struct {
	Name                        string `json:"name"`
	ComparedWith                string `json:"compared_with"`
	SelectedTests               int    `json:"selected_tests"`
	EligibleTests               int    `json:"eligible_tests"`
	CompleteMutationDenominator int    `json:"complete_mutation_denominator"`
	AdditionalDetected          int    `json:"additional_detected"`
	IncompleteMutations         int    `json:"incomplete_mutations"`
	Status                      string `json:"status"`
	Definition                  string `json:"definition"`
}
type testContributionReport struct {
	Status                     string                         `json:"status"`
	SelectorCommand            string                         `json:"selector_command"`
	Repeat                     int                            `json:"repeat"`
	PerEvaluationTimeoutMillis int64                          `json:"per_evaluation_timeout_millis"`
	TotalTimeoutMillis         int64                          `json:"total_timeout_millis"`
	SourceIdentity             string                         `json:"source_identity,omitempty"`
	ManifestSHA256             string                         `json:"manifest_sha256,omitempty"`
	BaselineTestIdentity       string                         `json:"baseline_test_identity,omitempty"`
	BaselineTests              []contributionTest             `json:"-"`
	BaselineEligibleTests      int                            `json:"baseline_eligible_tests"`
	BaselineOmittedTests       int                            `json:"baseline_omitted_tests"`
	BaselineStatus             string                         `json:"baseline_status"`
	HistoryAttributionStatus   string                         `json:"history_attribution_status"`
	HistoryEvents              []contributionDefinitionChange `json:"-"`
	EligibleTests              int                            `json:"eligible_tests"`
	SelectedTests              int                            `json:"selected_tests"`
	OmittedTests               int                            `json:"omitted_tests"`
	EligibleMutations          int                            `json:"eligible_mutations"`
	SelectedPairs              int                            `json:"selected_pairs"`
	OmittedPairs               int                            `json:"omitted_pairs"`
	Tests                      []contributionTest             `json:"-"`
	Pairs                      []contributionPair             `json:"-"`
	Controls                   []contributionControl          `json:"-"`
	OverlapCandidates          []string                       `json:"overlap_candidates"`
	SetDerivedMarginalCounts   map[string]int                 `json:"set_derived_marginal_counts"`
	DetectedMutationIDs        []string                       `json:"detected_mutation_ids"`
	DetectorCounts             map[string]int                 `json:"detector_counts"`
	TestDetectorCounts         map[string]int                 `json:"test_detector_counts"`
	ConfirmedUniqueMutationIDs []string                       `json:"confirmed_unique_mutation_ids"`
	SuiteInteractions          []string                       `json:"suite_interactions"`
	DefinitionChanges          []contributionDefinitionChange `json:"-"`
	Groups                     []contributionGroup            `json:"groups"`
	DefinitionCounts           map[string]int                 `json:"definition_counts"`
	UncommittedTestVersions    int                            `json:"uncommitted_test_versions"`
	MatrixArtifact             string                         `json:"matrix_artifact,omitempty"`
	MatrixSHA256               string                         `json:"matrix_sha256,omitempty"`
	Limitations                []string                       `json:"limitations"`
}

// contributionTests identifies top-level conventional TestX declarations. The
// full declaration bytes are hashed so equal names across revisions are not conflated.
func contributionTests(entries []snapshotEntry) []contributionTest {
	module := mutationModule(entries)
	nestedModules := map[string]bool{}
	for _, entry := range entries {
		if path.Base(entry.path) == "go.mod" && entry.path != "go.mod" {
			nestedModules[path.Dir(entry.path)] = true
		}
	}
	out := []contributionTest{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.path, "_test.go") || mutationExcludedPath(entry.path) != "" || mutationPlatformFilename(strings.TrimSuffix(entry.path, "_test.go")+".go") {
			continue
		}
		insideNested := false
		for dir := path.Dir(entry.path); dir != "."; dir = path.Dir(dir) {
			if nestedModules[dir] {
				insideNested = true
				break
			}
		}
		if insideNested {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, entry.path, entry.data, 0)
		if err != nil || hasBuildConstraints(f) {
			continue
		}
		pkg := path.Dir(entry.path)
		if pkg == "." {
			pkg = ""
		}
		if module != "" {
			if pkg == "" {
				pkg = module
			} else {
				pkg = module + "/" + pkg
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) > 0) || !validTestName(fn.Name.Name, "Test") || !testingParam(f, fn, "T") || fn.Body == nil {
				continue
			}
			start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
			if start < 0 || end > len(entry.data) || start >= end {
				continue
			}
			sum := sha256.Sum256(entry.data[start:end])
			hash := hex.EncodeToString(sum[:])
			key := pkg + "\x00" + entry.path + "\x00" + fn.Name.Name + "\x00" + hash
			id := sha256.Sum256([]byte(key))
			out = append(out, contributionTest{ID: hex.EncodeToString(id[:]), Package: pkg, File: entry.path, Name: fn.Name.Name, DeclarationSHA256: hash})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func contributionMatrix(tests []contributionTest, mutationIDs []string, pairLimit int) (selected []contributionPair, omitted int) {
	all := len(tests) * len(mutationIDs)
	for _, test := range tests {
		for _, mutation := range mutationIDs {
			if len(selected) < pairLimit {
				selected = append(selected, contributionPair{Population: "current", TestID: test.ID, MutationID: mutation, Status: "not_executed"})
			}
		}
	}
	return selected, all - len(selected)
}

func executeContributionTest(ctx context.Context, dir string, test contributionTest, timeout time.Duration, mutant bool) (string, string, int64, []byte) {
	started := time.Now()
	runctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	work, err := os.MkdirTemp("", "brag-contribution-run-")
	if err != nil {
		return "inconclusive", "setup_error", 0, nil
	}
	defer os.RemoveAll(work)
	home, tmp := filepath.Join(work, "home"), filepath.Join(work, "tmp")
	if os.Mkdir(home, 0700) != nil || os.Mkdir(tmp, 0700) != nil {
		return "inconclusive", "setup_error", 0, nil
	}
	selector := "^" + regexp.QuoteMeta(test.Name) + "$"
	cmd := exec.CommandContext(runctx, "go", "test", "-json", "-count=1", "-shuffle=off", "-run", selector, test.Package)
	configureDynamicProcess(cmd)
	cmd.Dir = dir
	cmd.Env = goEnvironment(home, tmp)
	cmd.WaitDelay = 2 * time.Second
	out, stderr := &cappedOutput{limit: dynamicOutputBytes}, &cappedOutput{limit: dynamicOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err = cmd.Run()
	elapsed := time.Since(started).Milliseconds()
	raw := append(out.Bytes(), stderr.Bytes()...)
	if ctx.Err() != nil {
		return "inconclusive", "caller_or_total_timeout", elapsed, raw
	}
	if runctx.Err() != nil {
		return "inconclusive", "evaluation_timeout", elapsed, raw
	}
	if out.truncated || stderr.truncated {
		return "inconclusive", "output_truncated", elapsed, raw
	}
	parsed, complete, failures := parseMutationEvents(out.Bytes())
	if !complete {
		return "inconclusive", "malformed_or_incomplete_events", elapsed, raw
	}
	matched, topPass, topFail := 0, false, false
	for _, ev := range parsed.Tests {
		if ev.Package != test.Package {
			continue
		}
		if ev.Name == test.Name {
			matched++
			topPass = ev.Outcome == "pass"
			topFail = ev.Outcome == "fail"
		} else if strings.HasPrefix(ev.Name, test.Name+"/") && ev.Outcome == "skip" {
			return "inconclusive", "selected_subtest_skipped", elapsed, raw
		}
	}
	if matched != 1 {
		return "inconclusive", "no_or_multiple_selected_test_events", elapsed, raw
	}
	if failures {
		if !mutationFailureIsAccounted(out.Bytes(), stderr.Bytes(), true) {
			return "inconclusive", "unaccounted_package_failure", elapsed, raw
		}
		if topFail && err != nil {
			if mutant {
				return "detected", "selected_test_failure", elapsed, raw
			}
			return "inconclusive", "unmutated_control_test_failure", elapsed, raw
		}
		return "inconclusive", "package_or_descendant_failure_unattributed", elapsed, raw
	}
	if err == nil && topPass {
		return "not_detected", "selected_test_passed", elapsed, raw
	}
	return "inconclusive", classifyOutput(parsed.Diagnostic + string(out.Bytes()) + string(stderr.Bytes())), elapsed, raw
}

func collectContributionPopulation(parent context.Context, root, scope, head string, base *baseInfo, artifactPath string, selectedNames []string, mutations *mutationReport, per, total time.Duration, repeats int, commits []string, selected identityInfo, identities maps) *testContributionReport {
	r := &testContributionReport{Status: "partial", BaselineStatus: "unavailable", HistoryAttributionStatus: "unavailable", DefinitionCounts: map[string]int{}, Groups: []contributionGroup{{Name: "current_vs_baseline", ComparedWith: "baseline", Status: "unavailable", Definition: "baseline evidence unavailable"}, {Name: "selected_developer_vs_baseline", ComparedWith: "baseline", Status: "unavailable", Definition: "baseline/attribution evidence unavailable"}, {Name: "all_developers_vs_baseline", ComparedWith: "baseline", Status: "unavailable", Definition: "baseline/attribution evidence unavailable"}}, SelectorCommand: "go test -json -count=1 -shuffle=off -run '^<escaped TestX>$' <package>", Repeat: repeats, PerEvaluationTimeoutMillis: per.Milliseconds(), TotalTimeoutMillis: total.Milliseconds(), Tests: []contributionTest{}, BaselineTests: []contributionTest{}, HistoryEvents: []contributionDefinitionChange{}, Pairs: []contributionPair{}, Controls: []contributionControl{}, OverlapCandidates: []string{}, SetDerivedMarginalCounts: map[string]int{}, DetectedMutationIDs: []string{}, DetectorCounts: map[string]int{}, TestDetectorCounts: map[string]int{}, ConfirmedUniqueMutationIDs: []string{}, SuiteInteractions: []string{}, DefinitionChanges: []contributionDefinitionChange{}, Limitations: []string{"set-derived results do not model actual remove-test suite reruns", "package initialization and TestMain execute for every selector; shared-state/order effects remain possible", "nested modules, Go-ignored directories, platform-suffixed and build-constrained test files are outside the attributed selector inventory"}}
	if mutations == nil || len(mutations.Snapshots) != 1 {
		r.Limitations = append(r.Limitations, "exactly one successful mutation snapshot is required")
		return r
	}
	ms := mutations.Snapshots[0]
	if ms.ManifestArtifact == "" || ms.ManifestSHA256 == "" {
		r.Limitations = append(r.Limitations, "selected mutation manifest is unavailable")
		return r
	}
	abs, err := canonicalCandidate(root, artifactPath)
	if err != nil {
		r.Limitations = append(r.Limitations, "artifact directory cannot be verified")
		return r
	}
	manifestPath := filepath.Join(abs, filepath.Base(ms.ManifestArtifact))
	data, err := os.ReadFile(manifestPath)
	if err != nil || hashBytes(data) != ms.ManifestSHA256 {
		r.Limitations = append(r.Limitations, "selected mutation manifest hash verification failed")
		return r
	}
	var manifest mutationManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.SourceIdentity != ms.SourceIdentity || manifest.Snapshot != ms.Name || manifest.OperatorVersion != mutationOperatorVersion {
		r.Limitations = append(r.Limitations, "selected mutation manifest identity verification failed")
		return r
	}
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	started := time.Now()
	specs := map[string]snapshotSpec{}
	if scope == "branch" && base != nil {
		for _, s := range []snapshotSpec{{"baseline", base.MergeBase, false}, {"HEAD", head, false}, {"current_tracked_worktree", head, true}} {
			specs[s.label] = s
		}
	} else if scope == "all" {
		specs["current_tracked_worktree"] = snapshotSpec{"current_tracked_worktree", head, true}
	}
	sp, ok := specs[ms.Name]
	if !ok {
		r.Limitations = append(r.Limitations, "selected snapshot cannot be reconstructed")
		return r
	}
	dir, _, err := materializeDynamicSnapshotContext(ctx, root, sp)
	if err != nil {
		r.Limitations = append(r.Limitations, "selected snapshot materialization failed")
		return r
	}
	entries, _, err := snapshotEntriesForMutation(dir)
	os.RemoveAll(dir)
	if err != nil || mutationSourceIdentity(entries, false) != manifest.SourceIdentity || mutationSourceIdentity(entries, true) != manifest.TestIdentity {
		r.Limitations = append(r.Limitations, "selected source/test identities do not match the manifest")
		return r
	}
	r.SourceIdentity = manifest.SourceIdentity
	r.ManifestSHA256 = ms.ManifestSHA256
	headVersions := map[string]bool{}
	headAttributionUnavailable := false
	if ms.Name == "current_tracked_worktree" {
		var e error
		headVersions, e = trackedHeadContributionVersions(ctx, root, head)
		if e != nil {
			headAttributionUnavailable = true
			r.Limitations = append(r.Limitations, "HEAD test definitions unavailable for worktree attribution: "+e.Error())
		}
	}
	tests := contributionTests(entries)
	allEligibleTests := append([]contributionTest(nil), tests...)
	r.EligibleTests = len(tests)
	if len(tests) > contributionTestLimit {
		r.OmittedTests = len(tests) - contributionTestLimit
		tests = tests[:contributionTestLimit]
	}
	r.Tests = tests
	r.SelectedTests = len(tests)
	ids := make([]string, 0, len(manifest.Sites))
	for _, site := range manifest.Sites {
		ids = append(ids, site.ID)
	}
	sort.Strings(ids)
	r.EligibleMutations = len(ids)
	if len(tests) == 0 {
		r.Limitations = append(r.Limitations, "no supported top-level TestX declarations discovered")
	}
	if len(ids) == 0 {
		r.Limitations = append(r.Limitations, "selected manifest contains no mutation sites")
	}
	currentPairLimit := contributionPairLimit
	if scope == "branch" && base != nil && base.MergeBase != "" {
		currentPairLimit = contributionPairLimit / 2
	}
	r.Pairs, r.OmittedPairs = contributionMatrix(tests, ids, currentPairLimit)
	r.SelectedPairs = len(r.Pairs)
	if repeats < 1 || repeats > 3 {
		r.Limitations = append(r.Limitations, "repeat count outside supported range")
		return r
	}
	if scope == "branch" {
		if n, e := countUncommittedContributionTests(ctx, root, head); e == nil {
			r.UncommittedTestVersions = n
		} else {
			r.Limitations = append(r.Limitations, "uncommitted test comparison unavailable: "+e.Error())
		}
	}
	lookup := map[string]mutationSite{}
	for _, site := range manifest.Sites {
		lookup[site.ID] = site
	}
	controls := map[string]string{}
	if mutations.artifactBudget == nil {
		r.Limitations = append(r.Limitations, "shared artifact budget unavailable; contribution evaluations not run")
		return r
	}
	for _, test := range tests {
		outcomes := []string{}
		var evidence []byte
		for n := 0; n < repeats; n++ {
			if ctx.Err() != nil || time.Since(started) >= total {
				break
			}
			copyDir, e := materializeMutationCopy(entries)
			if e != nil {
				outcomes = append(outcomes, "inconclusive")
				continue
			}
			status, _, _, output := executeContributionTest(ctx, copyDir, test, per, false)
			os.RemoveAll(copyDir)
			evidence = append(evidence, output...)
			outcomes = append(outcomes, status)
		}
		controls[test.ID] = stableContributionStatus(outcomes, repeats)
		controlRecord := contributionControl{Population: "current", TestID: test.ID, Status: controls[test.ID], Classification: "selected_test_control"}
		if len(outcomes) > 0 && len(evidence) == 0 {
			evidence = []byte("evaluation produced no captured output\n")
		}
		if len(evidence) > 0 {
			controlRecord.EvidenceArtifact, controlRecord.EvidenceSHA256 = persistMutationLog(mutations.artifactDir, mutations.artifactBudget, "contribution-control-", evidence)
			if controlRecord.EvidenceArtifact == "" {
				controlRecord.Status = "inconclusive"
				controlRecord.Classification = "control_evidence_artifact_unavailable"
				controls[test.ID] = "inconclusive"
			}
		}
		if (controlRecord.Status == "not_detected" || controlRecord.Status == "detected") && controlRecord.EvidenceArtifact == "" {
			controlRecord.Status = "inconclusive"
			controlRecord.Classification = "control_evidence_missing"
			controls[test.ID] = "inconclusive"
		}
		r.Controls = append(r.Controls, controlRecord)
	}
	for i := range r.Pairs {
		pair := &r.Pairs[i]
		if ctx.Err() != nil || time.Since(started) >= total {
			pair.Status = "not_executed"
			pair.Classification = "total_contribution_budget"
			continue
		}
		control := controls[pair.TestID]
		if control != "not_detected" {
			pair.Status = "inconclusive"
			pair.Classification = "selected_test_control_" + control
			continue
		}
		test := contributionTest{}
		for _, candidate := range tests {
			if candidate.ID == pair.TestID {
				test = candidate
				break
			}
		}
		site, ok := lookup[pair.MutationID]
		if !ok {
			pair.Status = "inconclusive"
			pair.Classification = "manifest_site_missing"
			continue
		}
		outcomes := []string{}
		classifications := []string{}
		var evidence []byte
		var elapsed int64
		for n := 0; n < repeats; n++ {
			if ctx.Err() != nil || time.Since(started) >= total {
				break
			}
			copyDir, e := materializeMutationCopy(entries)
			if e != nil {
				outcomes = append(outcomes, "inconclusive")
				classifications = append(classifications, "snapshot_setup_error")
				continue
			}
			if e = applyMutation(copyDir, site); e != nil {
				os.RemoveAll(copyDir)
				outcomes = append(outcomes, "inconclusive")
				classifications = append(classifications, "manifest_source_mismatch")
				continue
			}
			status, classification, duration, output := executeContributionTest(ctx, copyDir, test, per, true)
			evidence = append(evidence, output...)
			os.RemoveAll(copyDir)
			outcomes = append(outcomes, status)
			classifications = append(classifications, classification)
			elapsed += duration
		}
		pair.ElapsedMillis = elapsed
		pair.Status = stableContributionStatus(outcomes, repeats)
		if pair.Status == "unstable" {
			pair.Classification = "repeats_differ"
		} else if len(classifications) > 0 {
			pair.Classification = classifications[0]
		} else {
			pair.Classification = "not_executed"
		}
		if len(outcomes) > 0 {
			if len(evidence) == 0 {
				evidence = []byte("evaluation produced no captured output\n")
			}
			if len(evidence) == 0 {
				pair.Status = "inconclusive"
				pair.Classification = "pair_evidence_missing"
			} else {
				pair.EvidenceArtifact, pair.EvidenceSHA256 = persistMutationLog(mutations.artifactDir, mutations.artifactBudget, "contribution-pair-", evidence)
				if pair.EvidenceArtifact == "" {
					pair.Status = "inconclusive"
					pair.Classification = "pair_evidence_artifact_unavailable"
				}
			}
		}
	}
	if scope == "all" {
		r.BaselineStatus = "unavailable"
		for i := range r.Groups {
			r.Groups[i].Definition = "historical baseline and developer attribution unavailable in all scope"
		}
		r.Limitations = append(r.Limitations, "historical and developer group comparison unavailable in all scope")
	} else if base == nil || base.MergeBase == "" {
		r.BaselineStatus = "unavailable"
		r.Limitations = append(r.Limitations, "historical merge-base unavailable")
	} else {
		baselineEntries, overlayErr := overlayBaselineTests(root, base.MergeBase, entries)
		if overlayErr != nil {
			r.BaselineStatus = "unavailable"
			r.Limitations = append(r.Limitations, "historical test overlay unavailable: "+overlayErr.Error())
		} else {
			baselineTests := contributionTests(baselineEntries)
			r.BaselineEligibleTests = len(baselineTests)
			r.BaselineTestIdentity = mutationSourceIdentity(baselineEntries, true)
			r.DefinitionChanges = compareContributionDefinitions(contributionDefinitions(baselineEntries), contributionDefinitions(entries))
			r.DefinitionCounts = map[string]int{}
			for _, change := range r.DefinitionChanges {
				r.DefinitionCounts[change.Status]++
			}
			credits, historyEvents, historyPartial, historyReason := resolveContributionHistory(root, commits, selected, identities, contributionCandidatePaths(allEligibleTests))
			r.HistoryEvents = historyEvents
			if historyPartial {
				r.HistoryAttributionStatus = "partial"
				// Partial history cannot establish a current definition's latest event.
				credits["selected"] = map[string]bool{}
				credits["all_developers"] = map[string]bool{}
			} else {
				r.HistoryAttributionStatus = "measured"
			}
			selectedEligible, selectedTests := filterCreditedContributionTests(allEligibleTests, credits["selected"], ms.Name == "current_tracked_worktree", headVersions, contributionTestLimit)
			developerEligible, developerTests := filterCreditedContributionTests(allEligibleTests, credits["all_developers"], ms.Name == "current_tracked_worktree", headVersions, contributionTestLimit)
			remainingPairs := max(0, contributionPairLimit-r.SelectedPairs)
			if len(baselineTests) > contributionTestLimit {
				r.BaselineOmittedTests = len(baselineTests) - contributionTestLimit
				baselineTests = baselineTests[:contributionTestLimit]
				r.Limitations = append(r.Limitations, "baseline test population truncated")
			}
			r.BaselineTests = baselineTests
			baselinePairs, baselineControls, baselineOmitted := evaluateHistoricalPopulation(ctx, baselineEntries, baselineTests, manifest.Sites, ids, "baseline", remainingPairs, repeats, per, started, total, mutations.artifactDir, mutations.artifactBudget)
			r.Pairs = append(r.Pairs, baselinePairs...)
			r.Controls = append(r.Controls, baselineControls...)
			r.SelectedPairs += len(baselinePairs)
			r.OmittedPairs += baselineOmitted
			baselineGood := len(baselineTests) > 0 && len(baselineControls) == len(baselineTests)
			for _, control := range baselineControls {
				if control.Status != "not_detected" {
					baselineGood = false
				}
			}
			if baselineGood {
				r.BaselineStatus = "measured"
			} else {
				r.BaselineStatus = "inconclusive"
				r.Limitations = append(r.Limitations, "historical test population did not establish valid selector controls")
			}
			r.Groups = []contributionGroup{}
			currentPairs := filterContributionPairs(r.Pairs, "current")
			basePairs := filterContributionPairs(r.Pairs, "baseline")
			currentGroup := compareContributionGroup("current_vs_baseline", tests, baselineTests, currentPairs, basePairs, ids, "current test population minus merge-base test population; same selected source snapshot and mutation IDs")
			currentGroup.EligibleTests = r.EligibleTests
			markContributionGroupOmitted(&currentGroup, r.OmittedTests+r.BaselineOmittedTests, len(ids))
			r.Groups = append(r.Groups, currentGroup)
			if selected.Available {
				selectedGroup, allGroup := compareDeveloperGroups(selectedTests, developerTests, baselineTests, currentPairs, basePairs, ids)
				selectedGroup.Definition = "current test definitions introduced/modified by selected author or Co-authored-by identity versus merge-base tests"
				allGroup.Definition = "current test definitions introduced/modified by in-scope authors or Co-authored-by identities versus merge-base tests; credits may overlap"
				selectedGroup.EligibleTests = len(selectedEligible)
				allGroup.EligibleTests = len(developerEligible)
				markContributionGroupOmitted(&selectedGroup, max(0, len(selectedEligible)-len(selectedTests))+r.BaselineOmittedTests, len(ids))
				markContributionGroupOmitted(&allGroup, max(0, len(developerEligible)-len(developerTests))+r.BaselineOmittedTests, len(ids))
				r.Groups = append(r.Groups, selectedGroup, allGroup)
			} else {
				r.Groups = append(r.Groups, contributionGroup{Name: "selected_developer_vs_baseline", ComparedWith: "baseline", Status: "unavailable", Definition: "selected contributor identity unavailable"}, contributionGroup{Name: "all_developers_vs_baseline", ComparedWith: "baseline", Status: "unavailable", Definition: "selected contributor unavailable; comparable denominator unavailable"})
			}
			if historyPartial || headAttributionUnavailable {
				for i := range r.Groups {
					if r.Groups[i].Status == "measured" {
						r.Groups[i].Status = "partial"
					}
				}
				r.Limitations = append(r.Limitations, "test attribution history partial: "+historyReason)
			}
		}
	}
	detectedIDs, detectorCounts, testDetectorCounts, uniqueIDs := summarizeContributionDetections(tests, r.Pairs, ids)
	r.DetectedMutationIDs = detectedIDs
	r.DetectorCounts = detectorCounts
	r.TestDetectorCounts = testDetectorCounts
	r.ConfirmedUniqueMutationIDs = uniqueIDs
	// Compare isolated detections with the verified Phase 3B.1 full-suite outcomes.
	if ms.OutcomeArtifact != "" && ms.OutcomeSHA256 != "" {
		outPath := filepath.Join(abs, filepath.Base(ms.OutcomeArtifact))
		outcomeBytes, readErr := os.ReadFile(outPath)
		if readErr == nil && hashBytes(outcomeBytes) == ms.OutcomeSHA256 {
			var suite mutationOutcomeArtifact
			if json.Unmarshal(outcomeBytes, &suite) == nil {
				r.SuiteInteractions = contributionSuiteInteractions(tests, controls, r.Pairs, suite)
			} else {
				r.Limitations = append(r.Limitations, "suite outcome artifact could not be parsed")
			}
		} else {
			r.Limitations = append(r.Limitations, "suite outcome artifact hash verification failed")
		}
	} else {
		r.Limitations = append(r.Limitations, "suite outcome artifact unavailable for interaction comparison")
	}
	r.SetDerivedMarginalCounts, r.OverlapCandidates = contributionSetDerivedStats(tests, r.Pairs, ids)
	// A unique result is withheld whenever selection or execution omitted any eligible test/mutation pair.
	if r.OmittedTests > 0 || r.OmittedPairs > 0 {
		r.ConfirmedUniqueMutationIDs = []string{}
	}
	if ctx.Err() != nil || r.OmittedPairs > 0 || r.OmittedTests > 0 || len(tests) == 0 || len(ids) == 0 || r.BaselineStatus != "measured" {
		r.Status = "partial"
	} else {
		r.Status = "measured"
	}
	for _, group := range r.Groups {
		if group.Status != "measured" {
			r.Status = "partial"
		}
	}
	matrixBytes, _ := json.Marshal(struct {
		FormatVersion        int                            `json:"format_version"`
		Kind                 string                         `json:"kind"`
		SourceIdentity       string                         `json:"source_identity"`
		ManifestSHA256       string                         `json:"manifest_sha256"`
		BaselineTestIdentity string                         `json:"baseline_test_identity,omitempty"`
		Tests                []contributionTest             `json:"tests"`
		BaselineTests        []contributionTest             `json:"baseline_tests"`
		Controls             []contributionControl          `json:"controls"`
		Pairs                []contributionPair             `json:"pairs"`
		DefinitionChanges    []contributionDefinitionChange `json:"definition_changes"`
		HistoryEvents        []contributionDefinitionChange `json:"history_events"`
		Groups               []contributionGroup            `json:"groups"`
	}{1, "brag-test-contribution-matrix", r.SourceIdentity, r.ManifestSHA256, r.BaselineTestIdentity, r.Tests, r.BaselineTests, r.Controls, r.Pairs, r.DefinitionChanges, r.HistoryEvents, r.Groups})
	if len(matrixBytes) <= dynamicArtifactBytes {
		r.MatrixArtifact = writeDynamicArtifact(abs, mutations.artifactBudget, "test-contribution-matrix-", ".json", matrixBytes)
		if r.MatrixArtifact != "" {
			r.MatrixSHA256 = hashBytes(matrixBytes)
		}
	}
	if r.MatrixArtifact == "" {
		r.Status = "partial"
		r.Limitations = append(r.Limitations, "bounded matrix artifact could not be persisted; contribution outcomes invalidated")
		for i := range r.Pairs {
			if r.Pairs[i].Status != "not_executed" {
				r.Pairs[i].Status = "inconclusive"
				r.Pairs[i].Classification = "matrix_artifact_unavailable"
			}
		}
		for i := range r.Controls {
			r.Controls[i].Status = "inconclusive"
			r.Controls[i].Classification = "matrix_artifact_unavailable"
		}
		r.DetectedMutationIDs = []string{}
		r.DetectorCounts = map[string]int{}
		r.TestDetectorCounts = map[string]int{}
		r.ConfirmedUniqueMutationIDs = []string{}
		r.SetDerivedMarginalCounts = map[string]int{}
		r.OverlapCandidates = []string{}
		r.SuiteInteractions = []string{}
		for i := range r.Groups {
			r.Groups[i].Status = "partial"
			r.Groups[i].AdditionalDetected = 0
			r.Groups[i].CompleteMutationDenominator = 0
			r.Groups[i].IncompleteMutations = len(ids)
		}
	}
	return r
}

func contributionSetDerivedStats(tests []contributionTest, pairs []contributionPair, mutationIDs []string) (marginals map[string]int, overlaps []string) {
	marginals = map[string]int{}
	overlaps = []string{}
	for _, id := range mutationIDs {
		detectorTests := []string{}
		complete := true
		for _, test := range tests {
			found := false
			for _, pair := range pairs {
				if pair.Population == "current" && pair.MutationID == id && pair.TestID == test.ID {
					found = true
					if pair.Status == "detected" {
						detectorTests = append(detectorTests, test.ID)
					}
					if pair.Status != "detected" && pair.Status != "not_detected" {
						complete = false
					}
					break
				}
			}
			if !found {
				complete = false
			}
		}
		if len(detectorTests) > 1 {
			overlaps = append(overlaps, id+":"+strings.Join(detectorTests, ","))
		}
		if complete && len(detectorTests) == 1 {
			marginals[detectorTests[0]]++
		}
	}
	return
}

func summarizeContributionDetections(tests []contributionTest, pairs []contributionPair, mutationIDs []string) (detectedIDs []string, detectorCounts, testDetectorCounts map[string]int, uniqueIDs []string) {
	detectedIDs = []string{}
	detectorCounts = map[string]int{}
	testDetectorCounts = map[string]int{}
	uniqueIDs = []string{}
	currentPairs := filterContributionPairs(pairs, "current")
	for _, id := range mutationIDs {
		for _, pair := range currentPairs {
			if pair.MutationID == id && pair.Status == "detected" {
				detectorCounts[id]++
				testDetectorCounts[pair.TestID]++
			}
		}
		if detectorCounts[id] > 0 {
			detectedIDs = append(detectedIDs, id)
		}
		if confirmedUniqueMutation(tests, currentPairs, id) {
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	return
}

func contributionSuiteInteractions(tests []contributionTest, controls map[string]string, pairs []contributionPair, suite mutationOutcomeArtifact) []string {
	interactions := []string{}
	for _, test := range tests {
		for _, event := range suite.Control.Tests {
			if event.Package == test.Package && event.Name == test.Name && controls[test.ID] == "not_detected" && event.Outcome != "pass" {
				interactions = append(interactions, test.ID+":suite_control_differs_from_isolated_control")
			}
		}
	}
	isolated := map[string]bool{}
	for _, pair := range pairs {
		if pair.Population == "current" && pair.Status == "detected" {
			isolated[pair.MutationID] = true
		}
	}
	for _, mutant := range suite.Mutants {
		if mutant.Status == "killed" && !isolated[mutant.ID] {
			interactions = append(interactions, mutant.ID+":suite_killed_no_isolated_detector")
		}
		if mutant.Status == "survived" && isolated[mutant.ID] {
			interactions = append(interactions, mutant.ID+":isolated_detector_suite_survived")
		}
	}
	return interactions
}

func confirmedUniqueMutation(tests []contributionTest, pairs []contributionPair, mutationID string) bool {
	detectors := 0
	for _, test := range tests {
		found := false
		for _, pair := range pairs {
			if pair.TestID == test.ID && pair.MutationID == mutationID {
				found = true
				if pair.Status == "detected" {
					detectors++
				} else if pair.Status != "not_detected" {
					return false
				}
				break
			}
		}
		if !found {
			return false
		}
	}
	return detectors == 1
}

func stableContributionStatus(statuses []string, expected int) string {
	if len(statuses) == 0 {
		return "not_executed"
	}
	if len(statuses) < expected {
		return "inconclusive"
	}
	first := statuses[0]
	for _, status := range statuses[1:] {
		if status != first {
			return "unstable"
		}
	}
	return first
}

func filterCreditedContributionTests(tests []contributionTest, credits map[string]bool, requireHeadMatch bool, headVersions map[string]bool, limit int) (eligible, selected []contributionTest) {
	eligible = []contributionTest{}
	selected = []contributionTest{}
	for _, test := range tests {
		key := test.Package + "\x00" + test.File + "/test/" + test.Name
		version := key + "\x00" + test.DeclarationSHA256
		if !credits[version] || (requireHeadMatch && !headVersions[version]) {
			continue
		}
		eligible = append(eligible, test)
		if len(selected) < limit {
			selected = append(selected, test)
		}
	}
	return eligible, selected
}
