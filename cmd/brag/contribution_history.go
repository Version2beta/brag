package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

const contributionHistoryChunkSize = 500
const contributionHistoryChunkBytes = 8 << 20
const contributionHistoryDiffOutputBytes = 32 << 20
const contributionHistoryFileLimit = 100
const contributionHistoryTotalFiles = 5000
const contributionHistoryEventLimit = 50000
const contributionHistoryModuleBytes = 32 << 20

type contributionDefinition struct {
	Key, Name, Hash string
	Test            contributionTest
}

func contributionCandidatePaths(tests []contributionTest) []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, test := range tests {
		if !seen[test.File] {
			seen[test.File] = true
			paths = append(paths, test.File)
		}
	}
	sort.Strings(paths)
	return paths
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

type contributionDiffFile struct {
	path, sourcePath, oldOID, newOID, status string
}

type contributionCommit struct {
	message string
}

func candidatePathContains(paths []string, candidate string) bool {
	index := sort.SearchStrings(paths, candidate)
	return index < len(paths) && paths[index] == candidate
}

func contributionDiffChunk(root string, commits, candidatePaths []string, filterCandidates bool) (map[string][]contributionDiffFile, int, error) {
	input := strings.NewReader(strings.Join(commits, "\n") + "\n")
	args := []string{"-C", root, "diff-tree", "--no-ext-diff", "--no-textconv", "--root", "--raw", "--no-abbrev", "-M", "-m", "-r", "-z", "--stdin", "--"}
	if filterCandidates {
		for _, candidate := range candidatePaths {
			args = append(args, ":(literal)"+candidate)
		}
	} else {
		args = append(args, ":(glob)**/*_test.go")
	}
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnvironment(os.Environ())
	cmd.Stdin = input
	out, err := runCommandBounded(cmd, contributionHistoryChunkBytes)
	if err != nil {
		return nil, 0, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	result := map[string][]contributionDiffFile{}
	for i := 0; i < len(fields); {
		commit := fields[i]
		i++
		for i < len(fields) && strings.HasPrefix(fields[i], ":") {
			header := strings.Fields(strings.TrimPrefix(fields[i], ":"))
			i++
			if len(header) != 5 || i >= len(fields) {
				return nil, len(out), fmt.Errorf("malformed raw contribution diff")
			}
			name := fields[i]
			i++
			status := header[4]
			file := contributionDiffFile{path: name, sourcePath: name, oldOID: header[2], newOID: header[3], status: status}
			if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
				if i >= len(fields) {
					return nil, len(out), fmt.Errorf("malformed renamed contribution path")
				}
				// Raw rename records list the source path first and destination second.
				file.path = fields[i]
				i++
			}
			result[commit] = append(result[commit], file)
		}
	}
	return result, len(out), nil
}

func contributionCommitMetadata(reader *gitBatchReader, oid string, remaining int) (contributionCommit, int, error) {
	kind, size, err := reader.Info(oid)
	if err != nil {
		return contributionCommit{}, 0, err
	}
	if kind != "commit" || size > historyCommitObjectMaxBytes || size > remaining {
		return contributionCommit{}, size, errHistoryCommitMetadataLimit
	}
	kind, data, actual, err := reader.Read(oid, historyCommitObjectMaxBytes)
	if err != nil {
		return contributionCommit{}, 0, err
	}
	if kind != "commit" || actual != size {
		return contributionCommit{}, 0, fmt.Errorf("history commit metadata type or size mismatch")
	}
	separator := strings.Index(string(data), "\n\n")
	if separator < 0 {
		return contributionCommit{}, size, fmt.Errorf("commit metadata boundary unavailable")
	}
	return contributionCommit{message: string(data[separator+2:])}, size, nil
}

func contributionAuthorMap(root string, commits []string) (map[string]historyAuthor, int, error) {
	if len(commits) == 0 {
		return map[string]historyAuthor{}, 0, nil
	}
	args := append([]string{"log", "-z", "--no-walk=unsorted", "--format=%H%x00%an%x00%ae"}, commits...)
	data, err := runGitBounded(root, historyAuthorLogMaxBytes, args...)
	if err != nil {
		return nil, 0, err
	}
	authors, err := parseHistoryAuthors(data, commits)
	if err != nil {
		return nil, len(data), err
	}
	result := make(map[string]historyAuthor, len(authors))
	for _, author := range authors {
		if strings.TrimSpace(author.email) == "" {
			return nil, len(data), fmt.Errorf("commit author email is empty")
		}
		result[author.commit] = author
	}
	return result, len(data), nil
}

func resolveContributionHistory(root string, commits []string, selected identityInfo, identities maps, candidatePathLists ...[]string) (map[string]map[string]bool, []contributionDefinitionChange, bool, string) {
	credits := map[string]map[string]bool{"selected": {}, "all_developers": {}}
	changes := []contributionDefinitionChange{}
	filterCandidates := len(candidatePathLists) > 0
	candidatePaths := []string{}
	if filterCandidates {
		seen := map[string]bool{}
		for _, candidate := range candidatePathLists[0] {
			if strings.HasSuffix(candidate, "_test.go") && !seen[candidate] {
				seen[candidate] = true
				candidatePaths = append(candidatePaths, candidate)
			}
		}
		sort.Strings(candidatePaths)
		if len(candidatePaths) == 0 {
			return credits, changes, false, ""
		}
	}
	partial := false
	reason := ""
	files := 0
	historyBytes := 0
	metadataBytes := 0
	scannedCommits := 0
	processedCandidateCommits := 0
	stopHistory := false
	moduleCache := &historicalModuleCache{root: root}

	pathDiffBytes := 0
	for chunkStart := 0; chunkStart < len(commits); chunkStart += contributionHistoryChunkSize {
		chunkEnd := min(chunkStart+contributionHistoryChunkSize, len(commits))
		chunk := commits[chunkStart:chunkEnd]
		parents, parentErr := historyParentMap(root, chunk)
		if parentErr != nil {
			partial = true
			reason = fmt.Sprintf("parent metadata chunk incomplete after %d commits; omitted %d commits: %v", scannedCommits, len(commits)-scannedCommits, parentErr)
			break
		}
		diffs, diffBytes, diffErr := contributionDiffChunk(root, chunk, candidatePaths, filterCandidates)
		if diffErr != nil {
			partial = true
			reason = fmt.Sprintf("changed-path chunk incomplete after %d commits; omitted %d commits: %v", scannedCommits, len(commits)-scannedCommits, diffErr)
			break
		}
		if diffBytes > contributionHistoryDiffOutputBytes-pathDiffBytes {
			partial = true
			reason = fmt.Sprintf("history path-output byte cap reached after %d commits; omitted %d commits", scannedCommits, len(commits)-scannedCommits)
			break
		}
		pathDiffBytes += diffBytes
		relevant := make([]string, 0, len(chunk))
		renameCommits := []string{}
		uncertainRenames := map[string]bool{}
		for _, commit := range chunk {
			paths := diffs[commit]
			for _, file := range paths {
				if strings.HasPrefix(file.status, "R") || strings.HasPrefix(file.status, "C") {
					partial = true
					reason = fmt.Sprintf("candidate test-file rename/copy at %s makes path identity uncertain", commit)
					uncertainRenames[commit] = true
				} else if file.status == "A" || file.status == "D" {
					renameCommits = append(renameCommits, commit)
					break
				}
			}
		}
		if len(renameCommits) > 0 {
			renameDiffs, renameBytes, renameErr := contributionDiffChunk(root, renameCommits, nil, false)
			if renameErr != nil {
				partial = true
				reason = fmt.Sprintf("candidate rename check incomplete after %d commits; omitted %d commits: %v", scannedCommits, len(commits)-scannedCommits, renameErr)
				break
			}
			if renameBytes > contributionHistoryDiffOutputBytes-pathDiffBytes {
				partial = true
				reason = fmt.Sprintf("history path-output byte cap reached in rename check after %d commits; omitted %d commits", scannedCommits, len(commits)-scannedCommits)
				break
			}
			pathDiffBytes += renameBytes
			for _, commit := range renameCommits {
				for _, file := range renameDiffs[commit] {
					if (strings.HasPrefix(file.status, "R") || strings.HasPrefix(file.status, "C")) && (candidatePathContains(candidatePaths, file.path) || candidatePathContains(candidatePaths, file.sourcePath)) {
						uncertainRenames[commit] = true
						partial = true
						reason = fmt.Sprintf("candidate test-file rename/copy at %s makes path identity uncertain", commit)
					}
				}
			}
		}
		scannedCommits += len(chunk)
		for _, commit := range chunk {
			paths := diffs[commit]
			if uncertainRenames[commit] {
				continue
			}
			parentList := parents[commit]
			if len(parentList) > 1 {
				if len(paths) > 0 {
					partial = true
					reason = fmt.Sprintf("merge commit %s changes candidate test paths; latest provenance is uncertain", commit)
				}
				continue
			}
			if len(paths) == 0 {
				continue
			}
			if len(paths) > contributionHistoryFileLimit || files+len(paths) > contributionHistoryTotalFiles {
				partial = true
				reason = fmt.Sprintf("historical relevant test-file cap exceeded at commit %s", commit)
				stopHistory = true
				break
			}
			files += len(paths)
			relevant = append(relevant, commit)
		}
		if stopHistory {
			break
		}
		if len(relevant) == 0 {
			continue
		}

		authors, authorBytes, authorErr := contributionAuthorMap(root, relevant)
		if authorErr != nil {
			partial = true
			reason = fmt.Sprintf("candidate commit author chunk incomplete at commit %s: %v", relevant[0], authorErr)
			stopHistory = true
			break
		}
		if authorBytes > historyMetadataTotalMaxBytes-metadataBytes {
			partial = true
			reason = fmt.Sprintf("history metadata byte cap reached before candidate commit %s after %d candidate commits were processed", relevant[0], processedCandidateCommits)
			stopHistory = true
			break
		}
		metadataBytes += authorBytes

		batch, batchErr := newGitBatchReader(root)
		if batchErr != nil {
			partial = true
			reason = fmt.Sprintf("history object batch unavailable before candidate commit %s", relevant[0])
			stopHistory = true
			break
		}
		for _, commit := range relevant {
			if _, ok := authors[commit]; !ok {
				partial = true
				reason = fmt.Sprintf("candidate commit author metadata missing for %s", commit)
				stopHistory = true
				break
			}
			remainingMetadata := historyMetadataTotalMaxBytes - metadataBytes
			meta, objectBytes, err := contributionCommitMetadata(batch, commit, remainingMetadata)
			if err != nil {
				partial = true
				reason = fmt.Sprintf("history metadata byte/object cap reached at candidate commit %s after %d candidate commits were processed", commit, processedCandidateCommits)
				stopHistory = true
				break
			}
			metadataBytes += objectBytes
			processedCandidateCommits++
			author := authors[commit]
			person := resolve(identityInfo{Name: author.name, Email: author.email, Available: true}, identities)
			selectedCredit := selected.Available && idKey(person) == idKey(resolve(selected, identities))
			creditEmails := []string{person.Email}
			for _, co := range parseCoauthors(meta.message) {
				co = resolve(co, identities)
				creditEmails = append(creditEmails, co.Email)
				if selected.Available && idKey(co) == idKey(resolve(selected, identities)) {
					selectedCredit = true
				}
			}
			sort.Strings(creditEmails)
			creditEmails = uniqueStrings(creditEmails)

			before, after := map[string]contributionDefinition{}, map[string]contributionDefinition{}
			parent := ""
			if len(parents[commit]) > 0 {
				parent = parents[commit][0]
			}
			for _, file := range diffs[commit] {
				if strings.HasPrefix(file.status, "R") || strings.HasPrefix(file.status, "C") {
					partial = true
					reason = fmt.Sprintf("candidate test-file rename/copy at %s makes path identity uncertain", commit)
					continue
				}
				old, oldExists, oldBytes, oldErr := historyBatchBlob(batch, file.oldOID, complexityTotalBytes-historyBytes)
				if oldErr == nil {
					historyBytes += oldBytes
				}
				now, newExists, newBytes, newErr := historyBatchBlob(batch, file.newOID, complexityTotalBytes-historyBytes)
				if newErr == nil {
					historyBytes += newBytes
				}
				if oldErr != nil || newErr != nil {
					partial = true
					reason = fmt.Sprintf("candidate test blob unavailable or byte cap exceeded at %s", file.path)
					continue
				}
				for _, side := range []struct {
					data   []byte
					exists bool
					rev    string
					dst    map[string]contributionDefinition
				}{{old, oldExists, parent, before}, {now, newExists, commit, after}} {
					if !side.exists {
						continue
					}
					definitions, parseErr := definitionsForFile(file.path, side.data)
					if parseErr != nil {
						partial = true
						reason = fmt.Sprintf("candidate test definitions malformed at %s", file.path)
						continue
					}
					for key, definition := range definitions {
						if definition.Kind != "test" {
							continue
						}
						qualified, keyErr := historicalDefinitionKey(moduleCache, side.rev, file.path, key)
						if keyErr != nil {
							partial = true
							reason = fmt.Sprintf("candidate historical package identity unavailable at %s", file.path)
							continue
						}
						side.dst[qualified] = contributionDefinition{Key: qualified, Name: path.Base(key), Hash: definition.BodyHash}
					}
				}
			}

			keys := map[string]bool{}
			for key := range before {
				keys[key] = true
			}
			for key := range after {
				keys[key] = true
			}
			orderedKeys := make([]string, 0, len(keys))
			for key := range keys {
				orderedKeys = append(orderedKeys, key)
			}
			sort.Strings(orderedKeys)
			for _, key := range orderedKeys {
				old, hadOld := before[key]
				current, hasCurrent := after[key]
				status, hash := "", ""
				switch {
				case !hadOld && hasCurrent:
					status, hash = "introduced", current.Hash
				case hadOld && !hasCurrent:
					status = "removed"
				case old.Hash != current.Hash:
					status, hash = "modified", current.Hash
				default:
					continue
				}
				if len(changes) >= contributionHistoryEventLimit {
					partial = true
					reason = fmt.Sprintf("historical definition event cap reached after %d events at commit %s; remaining history not scanned", len(changes), commit)
					stopHistory = true
					break
				}
				prefix := key + "\x00"
				for version := range credits["selected"] {
					if strings.HasPrefix(version, prefix) {
						delete(credits["selected"], version)
					}
				}
				for version := range credits["all_developers"] {
					if strings.HasPrefix(version, prefix) {
						delete(credits["all_developers"], version)
					}
				}
				if status != "removed" {
					if selectedCredit {
						credits["selected"][key+"\x00"+hash] = true
					}
					credits["all_developers"][key+"\x00"+hash] = true
				}
				changes = append(changes, contributionDefinitionChange{Key: key, Status: status, OldHash: old.Hash, NewHash: current.Hash, Commit: commit, Author: person.Email, Credits: creditEmails})
			}
			if stopHistory {
				break
			}
		}
		if closeErr := batch.Close(); closeErr != nil {
			partial = true
			reason = "history object batch close failed"
			stopHistory = true
		}
		if stopHistory {
			break
		}
	}
	if partial {
		credits = map[string]map[string]bool{"selected": {}, "all_developers": {}}
		reason = fmt.Sprintf("%s (scanned %d/%d included commits; %d omitted)", reason, scannedCommits, len(commits), len(commits)-scannedCommits)
	}
	return credits, changes, partial, reason
}

func historyBatchBlob(reader *gitBatchReader, oid string, remaining int) ([]byte, bool, int, error) {
	if oid != "" && strings.Trim(oid, "0") == "" {
		return nil, false, 0, nil
	}
	if remaining < 0 {
		return nil, false, 0, fmt.Errorf("history blob byte cap exceeded")
	}
	kind, size, err := reader.Info(oid)
	if err != nil {
		return nil, false, 0, err
	}
	if kind != "blob" || size > complexityFileBytes || size > remaining {
		return nil, false, size, fmt.Errorf("historical blob exceeds bounds")
	}
	kind, data, actual, err := reader.Read(oid, remaining)
	if err != nil {
		return nil, false, 0, err
	}
	if kind != "blob" || actual != size {
		return nil, false, 0, fmt.Errorf("historical blob changed")
	}
	return data, true, size, nil
}

func runCommandBounded(cmd *exec.Cmd, limit int) ([]byte, error) {
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	if len(data) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("command output exceeds %d byte cap", limit)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return data, nil
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
