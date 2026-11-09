package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const historicalCommitLimit = 500
const historicalFilesLimit = 5000
const historicalFilesPerCommit = 100

type historicalResult struct {
	Definitions int
	Selected    int
	Commits     int
	Bytes       int
	Partial     bool
	Reason      string
}

func applyHistoricalTestAttribution(e testEvidence, root string, commits []string, selected identityInfo, identities maps) testEvidence {
	r := historicalResult{}
	includedCommitCount := len(commits)
	if len(commits) > historicalCommitLimit {
		r.Partial = true
		r.Reason = "commit analysis cap exceeded"
		commits = commits[:historicalCommitLimit]
	}
	parentMap, parentErr := historyParentMap(root, commits)
	if parentErr != nil {
		r.Partial = true
		r.Reason = "commit parent metadata unavailable"
	}
	authorMap, authorErr := historyAuthorMap(root, commits)
	var batch *gitBatchReader
	if len(commits) > 0 {
		batch, _ = newGitBatchReaderForCommit(root, commits[0])
	}
	blobCache := map[string][]byte{}
	readBlob := func(rev, path string, remaining int) ([]byte, bool, int, error) {
		if batch == nil {
			return historyBlob(root, rev, path, remaining)
		}
		b, ok, size, err := historyBlobBatch(root, rev, path, remaining, batch, blobCache)
		if err != nil && batch.failed {
			_ = batch.Close()
			batch = nil
			return historyBlob(root, rev, path, remaining)
		}
		return b, ok, size, err
	}
	filesAnalyzed := 0
	metadataBytes := 0
	for _, commit := range commits {
		if parentErr != nil {
			continue
		}
		parentIDs, ok := parentMap[commit]
		if !ok {
			r.Partial = true
			r.Reason = "commit parent metadata unavailable"
			continue
		}
		if len(parentIDs) > 1 {
			r.Partial = true
			if r.Reason == "" {
				r.Reason = "merge commits excluded to avoid duplicate attribution"
			}
			continue
		}
		parent := ""
		if len(parentIDs) == 1 {
			parent = parentIDs[0]
		}
		args := []string{"diff-tree", "--no-ext-diff", "--no-textconv", "--no-commit-id", "--name-only", "-r", "-z"}
		if parent == "" {
			args = append(args, "--root", commit)
		} else {
			args = append(args, parent, commit)
		}
		args = append(args, "--", "*.go")
		changed, err := runGitBounded(root, 8<<20, args...)
		if err != nil {
			r.Partial = true
			r.Reason = "changed-file history unavailable"
			continue
		}
		paths := strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00")
		testPaths := []string{}
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				testPaths = append(testPaths, p)
			}
		}
		if len(testPaths) > historicalFilesPerCommit || filesAnalyzed+len(testPaths) > historicalFilesLimit {
			r.Partial = true
			r.Reason = "historical changed-file cap exceeded"
			break
		}
		filesAnalyzed += len(testPaths)
		r.Commits++
		before, after := map[string]testDefinition{}, map[string]testDefinition{}
		for _, p := range testPaths {
			old, oldOK, oldSize, oldErr := readBlob(parent, p, complexityTotalBytes-r.Bytes)
			if oldErr == nil {
				r.Bytes += oldSize
			}
			new, newOK, newSize, newErr := readBlob(commit, p, complexityTotalBytes-r.Bytes)
			if newErr == nil {
				r.Bytes += newSize
			}
			if oldErr != nil || newErr != nil {
				r.Partial = true
				r.Reason = "historical blob unavailable"
				continue
			}
			if oldOK {
				ds, parseErr := definitionsForFile(p, old)
				if parseErr != nil {
					r.Partial = true
					r.Reason = "parent test file parse failed"
				} else {
					mergeTestDefs(before, ds)
				}
			}
			if newOK {
				ds, parseErr := definitionsForFile(p, new)
				if parseErr != nil {
					r.Partial = true
					r.Reason = "commit test file parse failed"
				} else {
					mergeTestDefs(after, ds)
				}
			}
		}
		introduced := []testDefinition{}
		for k, d := range after {
			if _, exists := before[k]; !exists && d.Kind == "test" {
				introduced = append(introduced, d)
			}
		}
		if len(introduced) == 0 {
			continue
		}
		r.Definitions += len(introduced)
		authorInfo, authorFound := authorMap[commit]
		message := ""
		metadataReady := authorErr == nil && authorFound && batch != nil
		if metadataReady {
			object, size, objectErr := readHistoryCommitObject(batch, commit, historyMetadataTotalMaxBytes-metadataBytes)
			if errors.Is(objectErr, errHistoryCommitMetadataLimit) {
				r.Partial = true
				r.Reason = "commit author metadata byte limit exceeded"
				continue
			}
			separator := bytes.Index(object, []byte("\n\n"))
			if objectErr == nil && separator >= 0 {
				message = string(object[separator+2:])
				metadataBytes += size
			} else {
				metadataReady = false
				if batch.failed {
					_ = batch.Close()
					batch = nil
				}
			}
		}
		if !metadataReady {
			remaining := historyMetadataTotalMaxBytes - metadataBytes
			if remaining <= 0 {
				r.Partial = true
				r.Reason = "commit author metadata byte limit exceeded"
				continue
			}
			limit := historyCommitObjectMaxBytes
			if remaining < limit {
				limit = remaining
			}
			meta, err := runGitBounded(root, int64(limit), "show", "-s", "--format=%an%x00%ae%x00%B", commit)
			if err != nil {
				r.Partial = true
				r.Reason = "commit author metadata unavailable or exceeded its byte cap"
				continue
			}
			metadataBytes += len(meta)
			fields := strings.SplitN(string(meta), "\x00", 3)
			if len(fields) < 3 {
				r.Partial = true
				r.Reason = "commit author metadata malformed"
				continue
			}
			authorInfo = historyAuthor{name: fields[0], email: fields[1]}
			message = fields[2]
		}
		if strings.TrimSpace(authorInfo.email) == "" {
			r.Partial = true
			r.Reason = "commit author metadata malformed"
			continue
		}
		author := resolve(identityInfo{Name: authorInfo.name, Email: authorInfo.email, Available: true}, identities)
		credited := idKey(author) == idKey(selected) && selected.Available
		for _, co := range parseCoauthors(message) {
			if idKey(resolve(co, identities)) == idKey(selected) && selected.Available {
				credited = true
			}
		}
		if credited {
			r.Selected += len(introduced)
		}
	}
	if batch != nil {
		if err := batch.Close(); err != nil {
			r.Partial = true
			r.Reason = "historical blob unavailable"
		}
	}
	if r.Partial {
		e.HistoricalAttribution.Status = "partial"
		e.HistoricalAttribution.Reason = r.Reason
		e.SelectedTestsAddedRatio.Status = "partial"
		e.SelectedTestsAddedRatio.Reason = r.Reason
		e.AllTestsAddedRatio.Status = "partial"
		e.AllTestsAddedRatio.Reason = r.Reason
	} else {
		e.HistoricalAttribution.Status = "measured"
		e.HistoricalAttribution.Reason = "non-merge included commits compared with first parent; coauthor trailer credits may overlap author credits"
		e.SelectedTestsAddedRatio.Status = "measured"
		e.SelectedTestsAddedRatio.Reason = "author or Co-authored-by identity matches selected identity"
		e.AllTestsAddedRatio.Status = "measured"
		e.AllTestsAddedRatio.Reason = "new valid TestX functions per included commit; merge commits contribute zero and are excluded"
	}
	n, d := r.Selected, r.Definitions
	e.SelectedTestsAddedRatio.Numerator = &n
	e.SelectedTestsAddedRatio.Denominator = d
	n, d = r.Definitions, includedCommitCount
	e.AllTestsAddedRatio.Numerator = &n
	e.AllTestsAddedRatio.Denominator = d
	if !selected.Available {
		e.SelectedTestsAddedRatio.Status = "unavailable"
		e.SelectedTestsAddedRatio.Numerator = nil
		e.SelectedTestsAddedRatio.Reason = "selected contributor identity unavailable"
	}
	e.HistoricalAttribution.Count = r.Definitions
	if r.Partial {
		e.Status = "partial"
	}
	e.Limitations = append(e.Limitations, "Historical TestX attribution uses first-parent changed-file comparisons; merge commits are excluded, aliases and Co-authored-by trailers are resolved, and coauthor credits may overlap author credits", fmt.Sprintf("History caps: %d commits, %d changed test files/commit, %d total changed test files, %d bytes total; over-limit or unparseable history is partial.", historicalCommitLimit, historicalFilesPerCommit, historicalFilesLimit, complexityTotalBytes))
	return e
}

func runGitBounded(root string, limit int64, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = gitEnvironment(os.Environ())
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	b, readErr := io.ReadAll(io.LimitReader(pipe, limit+1))
	if int64(len(b)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("git output exceeds %d byte cap", limit)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return b, nil
}
func gitFields(root string, args ...string) ([]string, error) {
	b, e := runGit(root, args...)
	if e != nil {
		return nil, e
	}
	return strings.Fields(string(b)), nil
}
func historyBlob(root, rev, path string, remaining int) ([]byte, bool, int, error) {
	if rev == "" {
		return nil, false, 0, nil
	}
	tree, e := runGit(root, "ls-tree", "-z", rev, "--", path)
	if e != nil {
		return nil, false, 0, e
	}
	if len(tree) == 0 {
		return nil, false, 0, nil
	}
	parts := strings.SplitN(strings.TrimSuffix(string(tree), "\x00"), "\t", 2)
	if len(parts) != 2 {
		return nil, false, 0, fmt.Errorf("invalid tree entry")
	}
	fields := strings.Fields(parts[0])
	if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, false, 0, fmt.Errorf("changed test path is not a regular blob")
	}
	oid := fields[2]
	size, e := runGit(root, "cat-file", "-s", oid)
	if e != nil {
		return nil, false, 0, e
	}
	n, e := strconv.Atoi(strings.TrimSpace(string(size)))
	if e != nil || n > complexityFileBytes || n > remaining {
		return nil, false, 0, fmt.Errorf("blob exceeds remaining history limit")
	}
	b, e := runGit(root, "cat-file", "blob", oid)
	if e != nil {
		return nil, false, 0, e
	}
	if len(b) != n {
		return nil, false, 0, fmt.Errorf("blob size changed")
	}
	return b, true, n, nil
}
func historyParentMap(root string, commits []string) (map[string][]string, error) {
	parents := map[string][]string{}
	if len(commits) == 0 {
		return parents, nil
	}
	args := append([]string{"rev-list", "--parents", "--no-walk"}, commits...)
	out, err := runGitBounded(root, 8<<20, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			parents[fields[0]] = fields[1:]
		}
	}
	for _, commit := range commits {
		if _, ok := parents[commit]; !ok {
			return nil, fmt.Errorf("parent metadata missing for commit")
		}
	}
	return parents, nil
}

func historyAuthorMap(root string, commits []string) (map[string]historyAuthor, error) {
	authors := map[string]historyAuthor{}
	if len(commits) == 0 {
		return authors, nil
	}
	args := append([]string{"log", "-z", "--no-walk=unsorted", "--format=%H%x00%an%x00%ae"}, commits...)
	out, err := runGitBounded(root, 8<<20, args...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(out, []byte{0})
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields) != 3*len(commits) {
		return nil, fmt.Errorf("history author record count mismatch")
	}
	for i := 0; i < len(fields); i += 3 {
		id := string(fields[i])
		authors[id] = historyAuthor{commit: id, name: string(fields[i+1]), email: string(fields[i+2])}
	}
	return authors, nil
}

func historyBlobBatch(root, rev, path string, remaining int, reader *gitBatchReader, cache map[string][]byte) ([]byte, bool, int, error) {
	if rev == "" {
		return nil, false, 0, nil
	}
	tree, err := runGit(root, "ls-tree", "-z", rev, "--", path)
	if err != nil {
		return nil, false, 0, err
	}
	if len(tree) == 0 {
		return nil, false, 0, nil
	}
	parts := strings.SplitN(strings.TrimSuffix(string(tree), "\x00"), "\t", 2)
	if len(parts) != 2 {
		return nil, false, 0, fmt.Errorf("invalid tree entry")
	}
	fields := strings.Fields(parts[0])
	if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, false, 0, fmt.Errorf("changed test path is not a regular blob")
	}
	oid := fields[2]
	kind, size, err := reader.Info(oid)
	if err != nil {
		return nil, false, 0, err
	}
	if kind != "blob" || size > complexityFileBytes || size > remaining {
		return nil, false, 0, fmt.Errorf("blob exceeds remaining history limit")
	}
	if content, ok := cache[oid]; ok {
		if len(content) != size {
			return nil, false, 0, fmt.Errorf("blob size changed")
		}
		return content, true, size, nil
	}
	kind, content, readSize, err := reader.Read(oid, remaining)
	if err != nil {
		return nil, false, 0, err
	}
	if kind != "blob" || readSize != size || len(content) != size {
		return nil, false, 0, fmt.Errorf("blob size changed")
	}
	cache[oid] = content
	return content, true, size, nil
}

func mergeTestDefs(dst, src map[string]testDefinition) {
	for k, v := range src {
		dst[k] = v
	}
}
func definitionsForFile(path string, src []byte) (map[string]testDefinition, error) {
	fset := token.NewFileSet()
	f, e := parser.ParseFile(fset, path, src, parser.AllErrors)
	if e != nil {
		return nil, e
	}
	return extractTestDefinitions(path, src, fset, f), nil
}
func extractTestDefinitions(path string, src []byte, fset *token.FileSet, file *ast.File) map[string]testDefinition {
	out := map[string]testDefinition{}
	if !strings.HasSuffix(path, "_test.go") {
		return out
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) > 0) || fn.Body == nil {
			continue
		}
		kind := ""
		switch {
		case fn.Name.Name == "TestMain" && testingParam(file, fn, "M"):
			kind = "test_main"
		case validTestName(fn.Name.Name, "Test") && testingParam(file, fn, "T"):
			kind = "test"
		case validTestName(fn.Name.Name, "Benchmark") && testingParam(file, fn, "B"):
			kind = "benchmark"
		case exampleName(fn.Name.Name) && (fn.Type.Params == nil || len(fn.Type.Params.List) == 0):
			kind = "example"
		}
		if kind != "" {
			start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
			hash := sha256.Sum256(src[start:end])
			key := path + "/" + kind + "/" + fn.Name.Name
			out[key] = testDefinition{Key: key, Kind: kind, BodyHash: fmt.Sprintf("%x", hash)}
		}
	}
	return out
}
