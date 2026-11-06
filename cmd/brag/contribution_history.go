package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const contributionHistoryCommitLimit = 500
const contributionHistoryFileLimit = 100
const contributionHistoryTotalFiles = 5000
const contributionHistoryModuleBytes = 32 << 20

type contributionDefinition struct {
	Key, Name, Hash string
	Test            contributionTest
}

func contributionDefinitions(entries []snapshotEntry) map[string]contributionDefinition {
	out := map[string]contributionDefinition{}
	for _, t := range contributionTests(entries) {
		k := t.Package + "\x00" + t.File + "/test/" + t.Name
		out[k] = contributionDefinition{Key: k, Name: t.Name, Hash: t.DeclarationSHA256, Test: t}
	}
	return out
}
func overlayBaselineTests(root, mergeBase string, source []snapshotEntry) ([]snapshotEntry, error) {
	if mergeBase == "" {
		return nil, fmt.Errorf("merge-base unavailable")
	}
	b, e := runGitBounded(root, 32<<20, "ls-tree", "-r", "--name-only", "-z", mergeBase)
	if e != nil {
		return nil, e
	}
	out := []snapshotEntry{}
	for _, x := range source {
		if !strings.HasSuffix(x.path, "_test.go") {
			out = append(out, x)
		}
	}
	paths := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	tests := []string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			tests = append(tests, p)
		}
	}
	sort.Strings(tests)
	if len(tests) > contributionTestLimit*10 {
		return nil, fmt.Errorf("baseline test file cap exceeded")
	}
	remaining := complexityTotalBytes
	for _, p := range tests {
		data, ok, n, e := historyBlob(root, mergeBase, p, remaining)
		if e != nil {
			return nil, e
		}
		if !ok {
			return nil, fmt.Errorf("listed merge-base test blob %q could not be read", p)
		}
		remaining -= n
		out = append(out, snapshotEntry{path: p, mode: "100644", data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}
func compareContributionDefinitions(base, current map[string]contributionDefinition) []contributionDefinitionChange {
	keys := map[string]bool{}
	for k := range base {
		keys[k] = true
	}
	for k := range current {
		keys[k] = true
	}
	order := []string{}
	for k := range keys {
		order = append(order, k)
	}
	sort.Strings(order)
	changes := []contributionDefinitionChange{}
	for _, k := range order {
		a, ha := base[k]
		b, hb := current[k]
		switch {
		case ha && hb && a.Hash == b.Hash:
			changes = append(changes, contributionDefinitionChange{Key: k, Status: "existing_unchanged", OldHash: a.Hash, NewHash: b.Hash})
		case ha && hb:
			changes = append(changes, contributionDefinitionChange{Key: k, Status: "modified", OldHash: a.Hash, NewHash: b.Hash})
		case ha:
			changes = append(changes, contributionDefinitionChange{Key: k, Status: "removed", OldHash: a.Hash})
		default:
			changes = append(changes, contributionDefinitionChange{Key: k, Status: "introduced", NewHash: b.Hash})
		}
	}
	for i := range changes {
		if changes[i].Status != "introduced" && changes[i].Status != "removed" {
			continue
		}
		h := changes[i].NewHash
		if h == "" {
			h = changes[i].OldHash
		}
		name := path.Base(changes[i].Key)
		for j := range changes {
			if i == j {
				continue
			}
			oh := changes[j].NewHash
			if oh == "" {
				oh = changes[j].OldHash
			}
			if oh == h && path.Base(changes[j].Key) == name && changes[j].Status != "existing_unchanged" {
				changes[i].Status = "uncertain"
				changes[j].Status = "uncertain"
			}
		}
	}
	return changes
}

type historicalModuleIndex struct {
	rootModule string
	nestedDirs map[string]bool
}
type historicalModuleCache struct {
	root       string
	byteLimit  int64
	bytesUsed  int64
	byRevision map[string]historicalModuleIndex
	failures   map[string]error
}

func (c *historicalModuleCache) index(rev string) (historicalModuleIndex, error) {
	if c.byRevision == nil {
		c.byRevision = map[string]historicalModuleIndex{}
		c.failures = map[string]error{}
	}
	if idx, ok := c.byRevision[rev]; ok {
		return idx, nil
	}
	if err, ok := c.failures[rev]; ok {
		return historicalModuleIndex{}, err
	}
	limit := int64(contributionHistoryModuleBytes)
	if c.byteLimit > 0 {
		limit = c.byteLimit
	}
	remaining := limit - c.bytesUsed
	if remaining <= 0 {
		err := fmt.Errorf("historical module metadata byte cap exceeded")
		c.failures[rev] = err
		return historicalModuleIndex{}, err
	}
	listing, err := runGitBounded(c.root, remaining, "ls-tree", "-r", "--name-only", "-z", rev)
	if err != nil {
		// A failed bounded read may already have consumed the remaining allowance.
		c.bytesUsed = limit
		c.failures[rev] = err
		return historicalModuleIndex{}, err
	}
	c.bytesUsed += int64(len(listing))
	idx := historicalModuleIndex{nestedDirs: map[string]bool{}}
	for _, file := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
		if path.Base(file) == "go.mod" {
			dir := path.Dir(file)
			if dir == "." {
				remaining = limit - c.bytesUsed
				if remaining <= 0 {
					e := fmt.Errorf("historical module metadata byte cap exceeded")
					c.failures[rev] = e
					return historicalModuleIndex{}, e
				}
				blobLimit := int(remaining)
				if blobLimit > 1<<20 {
					blobLimit = 1 << 20
				}
				data, ok, n, e := historyBlob(c.root, rev, file, blobLimit)
				if e != nil || !ok {
					if e == nil {
						e = fmt.Errorf("root go.mod unavailable")
					}
					// Do not reuse allowance after a failed bounded blob read.
					c.bytesUsed = limit
					c.failures[rev] = e
					return historicalModuleIndex{}, e
				}
				c.bytesUsed += int64(n)
				idx.rootModule = mutationModule([]snapshotEntry{{path: "go.mod", data: data}})
			} else {
				idx.nestedDirs[dir] = true
			}
		}
	}
	if idx.rootModule == "" {
		err = fmt.Errorf("root module path unavailable")
		c.failures[rev] = err
		return historicalModuleIndex{}, err
	}
	c.byRevision[rev] = idx
	return idx, nil
}
func historicalDefinitionKey(cache *historicalModuleCache, rev, file, definitionKey string) (string, error) {
	idx, err := cache.index(rev)
	if err != nil {
		return "", err
	}
	dir := path.Dir(file)
	for parent := dir; parent != "."; parent = path.Dir(parent) {
		if idx.nestedDirs[parent] {
			return "", fmt.Errorf("nested-module test attribution is unsupported")
		}
	}
	pkg := idx.rootModule
	if dir != "." {
		pkg += "/" + dir
	}
	return pkg + "\x00" + definitionKey, nil
}

func resolveContributionHistory(root string, commits []string, selected identityInfo, identities maps) (map[string]map[string]bool, []contributionDefinitionChange, bool, string) {
	credits := map[string]map[string]bool{"selected": {}, "all_developers": {}}
	changes := []contributionDefinitionChange{}
	partial := false
	reason := ""
	if len(commits) > contributionHistoryCommitLimit {
		commits = commits[:contributionHistoryCommitLimit]
		partial = true
		reason = "included commit cap exceeded"
	}
	files := 0
	historyBytes := 0
	moduleCache := &historicalModuleCache{root: root}
	for _, commit := range commits {
		parents, e := gitFields(root, "rev-list", "--parents", "-n", "1", commit)
		if e != nil || len(parents) == 0 {
			partial = true
			reason = "commit parent metadata unavailable"
			continue
		}
		if len(parents) > 2 {
			partial = true
			reason = "merge commits excluded from first-parent test attribution"
			continue
		}
		parent := ""
		if len(parents) == 2 {
			parent = parents[1]
		}
		args := []string{"diff-tree", "--no-ext-diff", "--no-textconv", "--no-commit-id", "--name-only", "-r", "-z"}
		if parent == "" {
			args = append(args, "--root", commit)
		} else {
			args = append(args, parent, commit)
		}
		args = append(args, "--", "*.go")
		b, e := runGitBounded(root, 8<<20, args...)
		if e != nil {
			partial = true
			reason = "changed test path history unavailable"
			continue
		}
		paths := []string{}
		for _, p := range strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00") {
			if strings.HasSuffix(p, "_test.go") {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		if len(paths) > contributionHistoryFileLimit || files+len(paths) > contributionHistoryTotalFiles {
			partial = true
			reason = "historical test file cap exceeded"
			break
		}
		files += len(paths)
		meta, e := runGitBounded(root, 1<<20, "show", "-s", "--format=%an%x00%ae%x00%B", commit)
		if e != nil {
			partial = true
			reason = "commit attribution metadata unavailable"
			continue
		}
		f := strings.SplitN(string(meta), "\x00", 3)
		if len(f) < 3 || strings.TrimSpace(f[1]) == "" {
			partial = true
			reason = "commit author metadata malformed"
			continue
		}
		person := resolve(identityInfo{Name: f[0], Email: f[1], Available: true}, identities)
		selectedCredit := selected.Available && idKey(person) == idKey(resolve(selected, identities))
		creditEmails := []string{person.Email}
		for _, co := range parseCoauthors(f[2]) {
			resolved := resolve(co, identities)
			creditEmails = append(creditEmails, resolved.Email)
			if selected.Available && idKey(resolved) == idKey(resolve(selected, identities)) {
				selectedCredit = true
			}
		}
		sort.Strings(creditEmails)
		creditEmails = uniqueStrings(creditEmails)
		before, after := map[string]contributionDefinition{}, map[string]contributionDefinition{}
		for _, p := range paths {
			remaining := complexityTotalBytes - historyBytes
			old, ook, oldBytes, oe := historyBlob(root, parent, p, remaining)
			if oe == nil {
				historyBytes += oldBytes
			}
			remaining = complexityTotalBytes - historyBytes
			now, nok, newBytes, ne := historyBlob(root, commit, p, remaining)
			if ne == nil {
				historyBytes += newBytes
			}
			if oe != nil || ne != nil {
				partial = true
				reason = "historical test blob unavailable"
				continue
			}
			if ook {
				ds, pe := definitionsForFile(p, old)
				if pe != nil {
					partial = true
					reason = "parent test definitions malformed"
				} else {
					for k, d := range ds {
						if d.Kind == "test" {
							qualified, qe := historicalDefinitionKey(moduleCache, parent, p, k)
							if qe != nil {
								partial = true
								reason = "historical package identity unavailable"
								continue
							}
							before[qualified] = contributionDefinition{Key: qualified, Name: path.Base(k), Hash: d.BodyHash}
						}
					}
				}
			}
			if nok {
				ds, pe := definitionsForFile(p, now)
				if pe != nil {
					partial = true
					reason = "commit test definitions malformed"
				} else {
					for k, d := range ds {
						if d.Kind == "test" {
							qualified, qe := historicalDefinitionKey(moduleCache, commit, p, k)
							if qe != nil {
								partial = true
								reason = "historical package identity unavailable"
								continue
							}
							after[qualified] = contributionDefinition{Key: qualified, Name: path.Base(k), Hash: d.BodyHash}
						}
					}
				}
			}
		}
		keys := map[string]bool{}
		for k := range before {
			keys[k] = true
		}
		for k := range after {
			keys[k] = true
		}
		order := []string{}
		for k := range keys {
			order = append(order, k)
		}
		sort.Strings(order)
		for _, k := range order {
			a, ha := before[k]
			b, hb := after[k]
			status, hash := "", ""
			if !ha && hb {
				status = "introduced"
				hash = b.Hash
			} else if ha && !hb {
				status = "removed"
			} else if a.Hash != b.Hash {
				status = "modified"
				hash = b.Hash
			} else {
				continue
			}
			if selectedCredit {
				credits["selected"][k+"\x00"+hash] = true
			}
			credits["all_developers"][k+"\x00"+hash] = true
			changes = append(changes, contributionDefinitionChange{Key: k, Status: status, OldHash: a.Hash, NewHash: b.Hash, Commit: commit, Author: person.Email, Credits: creditEmails})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Commit != changes[j].Commit {
			return changes[i].Commit < changes[j].Commit
		}
		return changes[i].Key < changes[j].Key
	})
	return credits, changes, partial, reason
}
func evaluateHistoricalPopulation(ctx context.Context, entries []snapshotEntry, tests []contributionTest, sites []mutationSite, ids []string, population string, pairLimit, repeats int, per time.Duration, started time.Time, total time.Duration, artifactDir string, budget *artifactBudget) ([]contributionPair, []contributionControl, int) {
	pairs, omitted := contributionMatrix(tests, ids, pairLimit)
	for i := range pairs {
		pairs[i].Population = population
	}
	controls := []contributionControl{}
	controlStatus := map[string]string{}
	for _, t := range tests {
		results := []string{}
		var evidence []byte
		for n := 0; n < repeats; n++ {
			if ctx.Err() != nil || time.Since(started) >= total {
				break
			}
			d, e := materializeMutationCopy(entries)
			if e != nil {
				results = append(results, "inconclusive")
				continue
			}
			s, _, _, output := executeContributionTest(ctx, d, t, per, false)
			_ = os.RemoveAll(d)
			evidence = append(evidence, output...)
			results = append(results, s)
		}
		s := stableContributionStatus(results, repeats)
		controlStatus[t.ID] = s
		control := contributionControl{Population: population, TestID: t.ID, Status: s, Classification: "historical_selector_control"}
		if len(results) > 0 && len(evidence) == 0 {
			evidence = []byte("evaluation produced no captured output\n")
		}
		if len(evidence) > 0 {
			control.EvidenceArtifact, control.EvidenceSHA256 = persistMutationLog(artifactDir, budget, "contribution-control-", evidence)
			if control.EvidenceArtifact == "" {
				control.Status = "inconclusive"
				control.Classification = "control_evidence_artifact_unavailable"
				controlStatus[t.ID] = "inconclusive"
			}
		}
		if (control.Status == "not_detected" || control.Status == "detected") && control.EvidenceArtifact == "" {
			control.Status = "inconclusive"
			control.Classification = "control_evidence_missing"
			controlStatus[t.ID] = "inconclusive"
		}
		controls = append(controls, control)
	}
	lookup := map[string]mutationSite{}
	for _, s := range sites {
		lookup[s.ID] = s
	}
	for i := range pairs {
		p := &pairs[i]
		if ctx.Err() != nil || time.Since(started) >= total {
			p.Classification = "total_contribution_budget"
			continue
		}
		if controlStatus[p.TestID] != "not_detected" {
			p.Status = "inconclusive"
			p.Classification = "historical_control_" + controlStatus[p.TestID]
			continue
		}
		var test contributionTest
		for _, t := range tests {
			if t.ID == p.TestID {
				test = t
				break
			}
		}
		site, ok := lookup[p.MutationID]
		if !ok {
			p.Status = "inconclusive"
			p.Classification = "manifest_site_missing"
			continue
		}
		outcomes, classes := []string{}, []string{}
		var evidence []byte
		for n := 0; n < repeats; n++ {
			if ctx.Err() != nil || time.Since(started) >= total {
				break
			}
			d, e := materializeMutationCopy(entries)
			if e != nil {
				outcomes = append(outcomes, "inconclusive")
				continue
			}
			if e = applyMutation(d, site); e != nil {
				_ = os.RemoveAll(d)
				outcomes = append(outcomes, "inconclusive")
				classes = append(classes, "manifest_source_mismatch")
				continue
			}
			s, c, elapsed, output := executeContributionTest(ctx, d, test, per, true)
			evidence = append(evidence, output...)
			_ = os.RemoveAll(d)
			outcomes = append(outcomes, s)
			classes = append(classes, c)
			p.ElapsedMillis += elapsed
		}
		p.Status = stableContributionStatus(outcomes, repeats)
		if len(outcomes) > 0 {
			if len(evidence) == 0 {
				evidence = []byte("evaluation produced no captured output\n")
			}
			if len(evidence) == 0 {
				p.Status = "inconclusive"
				p.Classification = "pair_evidence_missing"
			} else {
				p.EvidenceArtifact, p.EvidenceSHA256 = persistMutationLog(artifactDir, budget, "contribution-pair-", evidence)
				if p.EvidenceArtifact == "" {
					p.Status = "inconclusive"
					p.Classification = "pair_evidence_artifact_unavailable"
				}
			}
		}
		if p.Status == "unstable" {
			p.Classification = "repeats_differ"
		} else if len(classes) > 0 {
			p.Classification = classes[0]
		}
	}
	return pairs, controls, omitted
}
func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
func filterContributionPairs(pairs []contributionPair, pop string) []contributionPair {
	out := []contributionPair{}
	for _, p := range pairs {
		if p.Population == pop {
			out = append(out, p)
		}
	}
	return out
}
func markContributionGroupOmitted(group *contributionGroup, omitted, mutations int) {
	if omitted <= 0 {
		return
	}
	group.IncompleteMutations = mutations
	group.CompleteMutationDenominator = 0
	group.AdditionalDetected = 0
	group.Status = "partial"
}
func compareContributionGroup(name string, group, base []contributionTest, gp, bp []contributionPair, ids []string, definition string) contributionGroup {
	g := contributionGroup{Name: name, ComparedWith: "baseline", SelectedTests: len(group), EligibleTests: len(group), Status: "measured", Definition: definition}
	if len(group) == 0 || len(base) == 0 {
		g.Status = "unavailable"
		g.IncompleteMutations = len(ids)
		return g
	}
	for _, id := range ids {
		gd, gv := populationMutationOutcome(group, gp, id)
		bd, bv := populationMutationOutcome(base, bp, id)
		if !gv || !bv {
			g.IncompleteMutations++
			continue
		}
		g.CompleteMutationDenominator++
		if gd && !bd {
			g.AdditionalDetected++
		}
	}
	if g.IncompleteMutations > 0 {
		g.Status = "partial"
	}
	return g
}
func compareDeveloperGroups(selected, all, base []contributionTest, currentPairs, basePairs []contributionPair, ids []string) (contributionGroup, contributionGroup) {
	s := contributionGroup{Name: "selected_developer_vs_baseline", ComparedWith: "baseline", SelectedTests: len(selected), EligibleTests: len(selected), Status: "measured"}
	a := contributionGroup{Name: "all_developers_vs_baseline", ComparedWith: "baseline", SelectedTests: len(all), EligibleTests: len(all), Status: "measured"}
	if len(selected) == 0 || len(all) == 0 || len(base) == 0 {
		s.Status = "unavailable"
		a.Status = "unavailable"
		s.IncompleteMutations = len(ids)
		a.IncompleteMutations = len(ids)
		return s, a
	}
	for _, id := range ids {
		sd, sv := populationMutationOutcome(selected, currentPairs, id)
		ad, av := populationMutationOutcome(all, currentPairs, id)
		bd, bv := populationMutationOutcome(base, basePairs, id)
		if !sv || !av || !bv {
			s.IncompleteMutations++
			a.IncompleteMutations++
			continue
		}
		s.CompleteMutationDenominator++
		a.CompleteMutationDenominator++
		if sd && !bd {
			s.AdditionalDetected++
		}
		if ad && !bd {
			a.AdditionalDetected++
		}
	}
	if s.IncompleteMutations > 0 {
		s.Status = "partial"
		a.Status = "partial"
	}
	return s, a
}
func populationMutationOutcome(tests []contributionTest, pairs []contributionPair, id string) (bool, bool) {
	if len(tests) == 0 {
		return false, false
	}
	detected, valid := false, true
	for _, t := range tests {
		found := false
		for _, p := range pairs {
			if p.TestID == t.ID && p.MutationID == id {
				found = true
				if p.Status == "detected" {
					detected = true
				} else if p.Status != "not_detected" {
					valid = false
				}
				break
			}
		}
		if !found {
			valid = false
		}
	}
	return detected, valid
}
func trackedHeadContributionVersions(ctx context.Context, root, head string) (map[string]bool, error) {
	dir, _, err := materializeDynamicSnapshotContext(ctx, root, snapshotSpec{"HEAD", head, false})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	entries, _, err := snapshotEntriesForMutation(dir)
	if err != nil {
		return nil, err
	}
	versions := map[string]bool{}
	for key, definition := range contributionDefinitions(entries) {
		versions[key+"\x00"+definition.Hash] = true
	}
	return versions, nil
}

func countUncommittedContributionTests(ctx context.Context, root, head string) (int, error) {
	if head == "" {
		return 0, fmt.Errorf("HEAD unavailable")
	}
	hd, _, e := materializeDynamicSnapshotContext(ctx, root, snapshotSpec{"HEAD", head, false})
	if e != nil {
		return 0, e
	}
	defer os.RemoveAll(hd)
	wd, _, e := materializeDynamicSnapshotContext(ctx, root, snapshotSpec{"current_tracked_worktree", head, true})
	if e != nil {
		return 0, e
	}
	defer os.RemoveAll(wd)
	he, _, e := snapshotEntriesForMutation(hd)
	if e != nil {
		return 0, e
	}
	we, _, e := snapshotEntriesForMutation(wd)
	if e != nil {
		return 0, e
	}
	before, after := contributionDefinitions(he), contributionDefinitions(we)
	n := 0
	for k, v := range after {
		old, ok := before[k]
		if !ok || old.Hash != v.Hash {
			n++
		}
	}
	return n, nil
}
