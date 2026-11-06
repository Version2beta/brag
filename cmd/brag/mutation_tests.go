package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const mutationOperatorVersion = "brag-go-mutation-v1"
const mutationScanLimit = 10000
const mutationPerFileLimit = 16 << 20

type mutationReport struct {
	artifactBudget      *artifactBudget
	artifactDir         string
	Status              string             `json:"status"`
	OperatorVersion     string             `json:"operator_version"`
	GoVersion           string             `json:"go_version"`
	GOOS                string             `json:"goos"`
	GOARCH              string             `json:"goarch"`
	PerRunTimeoutMillis int64              `json:"per_run_timeout_millis"`
	TotalTimeoutMillis  int64              `json:"total_timeout_millis"`
	TestConfiguration   map[string]string  `json:"test_configuration"`
	Snapshots           []mutationSnapshot `json:"snapshots"`
	Limitations         []string           `json:"limitations"`
}
type mutationSnapshot struct {
	Name                 string         `json:"snapshot"`
	SourceIdentity       string         `json:"source_identity"`
	TestIdentity         string         `json:"test_identity"`
	Selected             int            `json:"selected"`
	Eligible             int            `json:"eligible"`
	Unselected           int            `json:"unselected"`
	Unexamined           *int           `json:"unexamined"`
	PopulationIncomplete bool           `json:"population_incomplete"`
	Counts               map[string]int `json:"counts"`
	Score                *float64       `json:"score"`
	ManifestArtifact     string         `json:"manifest_artifact,omitempty"`
	ManifestSHA256       string         `json:"manifest_sha256,omitempty"`
	OutcomeArtifact      string         `json:"outcome_artifact,omitempty"`
	OutcomeSHA256        string         `json:"outcome_sha256,omitempty"`
	Status               string         `json:"status"`
	Diagnostic           string         `json:"diagnostic,omitempty"`
}
type mutationSite struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Package    string `json:"package"`
	Operator   string `json:"operator"`
	Offset     int    `json:"offset"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Expression string `json:"expression"`
	Before     string `json:"before"`
	After      string `json:"after"`
}
type mutationOutcome struct {
	ID             string        `json:"id"`
	Path           string        `json:"path"`
	Operator       string        `json:"operator"`
	Status         string        `json:"status"`
	Classification string        `json:"classification"`
	ElapsedMillis  int64         `json:"elapsed_millis"`
	Tests          []dynamicTest `json:"tests"`
	LogArtifact    string        `json:"log_artifact,omitempty"`
	LogSHA256      string        `json:"log_sha256,omitempty"`
}
type mutationExecution struct {
	Status         string        `json:"status"`
	Classification string        `json:"classification"`
	ElapsedMillis  int64         `json:"elapsed_millis"`
	Tests          []dynamicTest `json:"tests"`
	Output         []byte        `json:"-"`
	Valid          bool          `json:"-"`
}
type mutationManifest struct {
	FormatVersion   int            `json:"format_version"`
	Kind            string         `json:"kind"`
	OperatorVersion string         `json:"operator_version"`
	Snapshot        string         `json:"snapshot"`
	SourceIdentity  string         `json:"source_identity"`
	TestIdentity    string         `json:"test_identity"`
	SelectionLimit  int            `json:"selection_limit"`
	PackageFilters  []string       `json:"package_filters"`
	PathFilters     []string       `json:"path_filters"`
	Sites           []mutationSite `json:"sites"`
}
type mutationOutcomeArtifact struct {
	FormatVersion      int               `json:"format_version"`
	Kind               string            `json:"kind"`
	Control            mutationExecution `json:"control"`
	ControlLogArtifact string            `json:"control_log_artifact,omitempty"`
	ControlLogSHA256   string            `json:"control_log_sha256,omitempty"`
	Mutants            []mutationOutcome `json:"mutants"`
}

var mutationReplacements = map[string]string{"<": "<=", "<=": "<", ">": ">=", ">=": ">", "==": "!=", "!=": "==", "&&": "||", "||": "&&", "true": "false", "false": "true"}

func mutationModule(entries []snapshotEntry) string {
	for _, e := range entries {
		if e.path != "go.mod" {
			continue
		}
		for _, line := range strings.Split(string(e.data), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != "module" {
				continue
			}
			if len(fields) != 2 {
				return ""
			}
			module := fields[1]
			if strings.HasPrefix(module, "\"") {
				unquoted, err := strconv.Unquote(module)
				if err != nil {
					return ""
				}
				module = unquoted
			}
			if !validMutationPackageFilter(module) {
				return ""
			}
			return module
		}
	}
	return ""
}
func mutationBooleanReferences(entries []snapshotEntry) map[string]map[int]bool {
	type packageFiles struct {
		fset  *token.FileSet
		files []*ast.File
	}
	groups := map[string]*packageFiles{}
	unsafePackages := map[string]bool{}
	nested := map[string]bool{}
	for _, e := range entries {
		if e.path != "go.mod" && path.Base(e.path) == "go.mod" {
			nested[path.Dir(e.path)] = true
		}
	}
	for _, e := range entries {
		p := e.path
		if !strings.HasSuffix(p, ".go") || mutationExcludedPath(p) != "" {
			continue
		}
		dir := path.Dir(p)
		if len(e.data) > mutationPerFileLimit {
			unsafePackages[dir] = true
			continue
		}
		if mutationPlatformFilename(strings.TrimSuffix(p, "_test.go") + ".go") {
			unsafePackages[dir] = true
			continue
		}
		inside := false
		for d := dir; d != "."; d = path.Dir(d) {
			if nested[d] {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, e.data, parser.ParseComments|parser.AllErrors)
		if err != nil {
			unsafePackages[dir] = true
			continue
		}
		if hasBuildConstraints(f) {
			unsafePackages[dir] = true
			continue
		}
		groupKey := dir + "\x00" + f.Name.Name
		g := groups[groupKey]
		if g == nil {
			g = &packageFiles{fset: token.NewFileSet()}
			groups[groupKey] = g
		}
		// Reparse into the shared package FileSet so identifier positions map unambiguously.
		f, err = parser.ParseFile(g.fset, p, e.data, parser.ParseComments|parser.AllErrors)
		if err != nil {
			continue
		}
		g.files = append(g.files, f)
	}
	refs := map[string]map[int]bool{}
	for groupKey, g := range groups {
		dir := strings.SplitN(groupKey, "\x00", 2)[0]
		if unsafePackages[dir] {
			continue
		}
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
		conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
		_, _ = conf.Check("brag-mutation", g.fset, g.files, info)
		for _, f := range g.files {
			p := g.fset.Position(f.Pos()).Filename
			ast.Inspect(f, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || (id.Name != "true" && id.Name != "false") {
					return true
				}
				if sel, ok := parentSelector(f, id); ok && sel.Sel == id {
					return true
				}
				if info.Uses[id] != types.Universe.Lookup(id.Name) {
					return true
				}
				if refs[p] == nil {
					refs[p] = map[int]bool{}
				}
				refs[p][g.fset.Position(id.Pos()).Offset] = true
				return true
			})
		}
	}
	return refs
}
func parentSelector(f *ast.File, id *ast.Ident) (*ast.SelectorExpr, bool) {
	var found *ast.SelectorExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && s.Sel == id {
			found = s
			return false
		}
		return found == nil
	})
	return found, found != nil
}
func mutationSites(entries []snapshotEntry, packages, paths []string) ([]mutationSite, int, bool, []string, error) {
	module := mutationModule(entries)
	if len(packages) > 0 && module == "" {
		return nil, 0, false, nil, fmt.Errorf("package filters require a parseable tracked root go.mod module path")
	}
	all := make([]mutationSite, 0)
	skipped := []string{}
	skippedTruncated := false
	skip := func(value string) {
		var capped bool
		skipped, capped = appendBounded(skipped, value, 100)
		skippedTruncated = skippedTruncated || capped
	}
	booleanRefs := mutationBooleanReferences(entries)
	incomplete := false
	nestedModules := map[string]bool{}
	for _, e := range entries {
		if e.path != "go.mod" && path.Base(e.path) == "go.mod" {
			nestedModules[path.Dir(e.path)] = true
		}
	}
	seen := 0
	for _, entry := range entries {
		p := entry.path
		if !strings.HasSuffix(p, ".go") || !mutationPathSelected(p, paths) {
			continue
		}
		if strings.HasSuffix(p, "_test.go") {
			skip(p + ":test_file")
			continue
		}
		if reason := mutationExcludedPath(p); reason != "" {
			skip(p + ":" + reason)
			continue
		}
		nested := false
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if nestedModules[dir] {
				nested = true
				break
			}
		}
		if nested {
			skip(p + ":nested_module")
			continue
		}
		if mutationPlatformFilename(p) {
			skip(p + ":platform_filename")
			continue
		}
		if len(entry.data) > mutationPerFileLimit {
			incomplete = true
			skip(p)
			continue
		}
		if generatedGo(entry.data) {
			skip(p + ":generated")
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, p, entry.data, parser.ParseComments|parser.AllErrors)
		if err != nil {
			incomplete = true
			skip(p + ":parse_error")
			continue
		}
		if hasBuildConstraints(file) {
			skip(p + ":build_constraints")
			continue
		}
		pkg := module
		dir := path.Dir(p)
		if dir != "." {
			pkg += "/" + dir
		}
		if len(packages) > 0 && !mutationPackageSelected(pkg, packages) {
			continue
		}
		add := func(off int, before, after, expr string) bool {
			seen++
			if seen > mutationScanLimit {
				incomplete = true
				return false
			}
			pos := fset.Position(token.Pos(off + 1))
			h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s", mutationOperatorVersion, p, before+"_to_"+after, pos.Line, pos.Column, expr)))
			all = append(all, mutationSite{ID: hex.EncodeToString(h[:]), Path: p, Package: pkg, Operator: before + "_to_" + after, Offset: off, Line: pos.Line, Column: pos.Column, Expression: expr, Before: before, After: after})
			return true
		}
		stop := false
		booleanAmbiguous := false
		ast.Inspect(file, func(n ast.Node) bool {
			if stop {
				return false
			}
			if b, ok := n.(*ast.BinaryExpr); ok {
				before := b.Op.String()
				if after, ok := mutationReplacements[before]; ok {
					start := fset.Position(b.OpPos).Offset
					if !add(start, before, after, string(entry.data[fset.Position(b.Pos()).Offset:fset.Position(b.End()).Offset])) {
						stop = true
						return false
					}
				}
			}
			if id, ok := n.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
				start := fset.Position(id.Pos()).Offset
				if booleanRefs[p][start] {
					if !add(start, id.Name, mutationReplacements[id.Name], id.Name) {
						stop = true
						return false
					}
				} else {
					booleanAmbiguous = true
				}
			}
			return true
		})
		if booleanAmbiguous {
			skip(p + ":boolean_identifier_ambiguous_or_shadowed")
		}
		if stop {
			break
		}
	}
	if skippedTruncated && len(skipped) > 0 {
		skipped[len(skipped)-1] = "skipped_file_list_truncated"
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if len(all) > mutationScanLimit {
		all = all[:mutationScanLimit]
		incomplete = true
	}
	return all, len(all), incomplete, skipped, nil
}
func mutationExcludedPath(p string) string {
	for _, part := range strings.Split(path.Dir(p), "/") {
		if part == "vendor" {
			return "vendor"
		}
		if part == "testdata" {
			return "testdata"
		}
		if part != "." && (strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_")) {
			return "go_ignored_directory"
		}
	}
	return ""
}
func mutationPlatformFilename(p string) bool {
	name := strings.TrimSuffix(path.Base(p), ".go")
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return false
	}
	last := parts[len(parts)-1]
	goos := map[string]bool{"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true}
	goarch := map[string]bool{"386": true, "amd64": true, "amd64p32": true, "arm": true, "arm64": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true, "mips64le": true, "ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true}
	return goos[last] || goarch[last]
}
func generatedGo(b []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "generated.go", b, parser.ParseComments)
	if err != nil {
		return false
	}
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			continue
		}
		for _, c := range group.List {
			text := strings.TrimSpace(c.Text)
			if strings.HasPrefix(text, "// Code generated ") && strings.HasSuffix(text, " DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}
func hasBuildConstraints(f *ast.File) bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				return true
			}
		}
	}
	return false
}
func validMutationPackageFilter(p string) bool {
	return p != "" && strings.TrimSpace(p) == p && !strings.ContainsAny(p, "\\\\ \t\n:") && !strings.HasPrefix(p, "/") && !strings.HasSuffix(p, "/") && path.Clean(p) == p && !strings.Contains(p, "..")
}
func validMutationPathFilter(p string) bool {
	return p != "" && strings.TrimSpace(p) == p && !strings.Contains(p, "\\\\") && !path.IsAbs(p) && path.Clean(p) == p && p != "." && safeRelative(p)
}
func mutationPathSelected(p string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if p == f || strings.HasPrefix(p, strings.TrimSuffix(f, "/")+"/") {
			return true
		}
	}
	return false
}
func mutationPackageSelected(pkg string, filters []string) bool {
	for _, p := range filters {
		if pkg == p || strings.HasPrefix(pkg, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}
func mutationSourceIdentity(entries []snapshotEntry, tests bool) string {
	h := sha256.New()
	for _, e := range entries {
		if strings.HasSuffix(e.path, "_test.go") != tests {
			continue
		}
		fmt.Fprintf(h, "%s\x00%d\x00", e.path, len(e.data))
		h.Write(e.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func parseMutationEvents(data []byte) (dynamicRun, bool, bool) {
	r := dynamicRun{Tests: []dynamicTest{}, TestCounts: map[string]int{"fail": 0}}
	d := json.NewDecoder(strings.NewReader(string(data)))
	started, terminal, failedTests := map[string]bool{}, map[string]string{}, map[string]bool{}
	for {
		var e struct {
			Action  string
			Package string
			Test    string
			Elapsed float64
			Output  string
		}
		if err := d.Decode(&e); err != nil {
			if err.Error() == "EOF" {
				break
			}
			return r, false, false
		}
		if e.Output != "" {
			r.Diagnostic = bounded(r.Diagnostic+e.Output, 8192)
		}
		if e.Package != "" && e.Test == "" {
			switch e.Action {
			case "start":
				started[e.Package] = true
			case "pass", "fail", "skip":
				if !started[e.Package] || terminal[e.Package] != "" {
					return r, false, false
				}
				terminal[e.Package] = e.Action
			}
		}
		if e.Test != "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			r.Tests = append(r.Tests, dynamicTest{e.Package, e.Test, e.Action, e.Elapsed})
			if e.Action == "fail" {
				failedTests[e.Package] = true
				r.TestCounts["fail"]++
			}
		}
	}
	if len(started) == 0 || len(started) != len(terminal) {
		return r, false, false
	}
	for pkg := range failedTests {
		if terminal[pkg] != "fail" {
			return r, false, false
		}
	}
	if len(failedTests) == 0 {
		return r, true, false
	}
	for pkg, outcome := range terminal {
		if outcome == "fail" && !failedTests[pkg] {
			return r, true, false
		}
	}
	return r, true, true
}

func mutationFailureIsAccounted(stdout, stderr []byte, testFailure bool) bool {
	if !testFailure || len(bytes.TrimSpace(stderr)) != 0 || classifyOutput(string(stdout)) != "test_failure" {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var event struct {
			Action string
			Test   string
			Output string
		}
		err := dec.Decode(&event)
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		if event.Action != "output" || event.Test != "" {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(event.Output, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "FAIL" || (strings.HasPrefix(line, "?") && strings.HasSuffix(line, "[no test files]")) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 3 && (fields[0] == "FAIL" || fields[0] == "ok") {
				if _, err := time.ParseDuration(fields[2]); err == nil {
					continue
				}
			}
			// Unknown package-level output may be a build or infrastructure failure.
			return false
		}
	}
}
func executeMutation(ctx context.Context, dir string, timeout time.Duration) mutationExecution {
	started := time.Now()
	result := mutationExecution{Status: "inconclusive", Classification: "setup_error", Tests: []dynamicTest{}}
	runctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	work, err := os.MkdirTemp("", "brag-mutation-run-")
	if err != nil {
		return result
	}
	defer os.RemoveAll(work)
	home, tmp := filepath.Join(work, "home"), filepath.Join(work, "tmp")
	if os.Mkdir(home, 0700) != nil || os.Mkdir(tmp, 0700) != nil {
		return result
	}
	cmd := exec.CommandContext(runctx, "go", "test", "-json", "-count=1", "-shuffle=off", "./...")
	configureDynamicProcess(cmd)
	cmd.Dir = dir
	cmd.Env = goEnvironment(home, tmp)
	cmd.WaitDelay = 2 * time.Second
	out, stderr := &cappedOutput{limit: dynamicOutputBytes}, &cappedOutput{limit: dynamicOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err = cmd.Run()
	result.ElapsedMillis = time.Since(started).Milliseconds()
	result.Output = append(out.Bytes(), stderr.Bytes()...)
	if ctx.Err() == context.Canceled {
		result.Classification = "caller_cancellation"
		return result
	}
	if ctx.Err() == context.DeadlineExceeded || runctx.Err() == context.DeadlineExceeded {
		result.Classification = "timeout"
		return result
	}
	if out.truncated || stderr.truncated {
		result.Classification = "output_truncated"
		return result
	}
	parsed, complete, failures := parseMutationEvents(out.Bytes())
	result.Tests = parsed.Tests
	if !complete {
		result.Classification = "incomplete_test_stream"
		return result
	}
	if result.Tests == nil {
		result.Tests = []dynamicTest{}
	}
	if err == nil {
		result.Status = "pass"
		result.Classification = "control_pass"
		result.Valid = true
		return result
	}
	if failures {
		if !mutationFailureIsAccounted(out.Bytes(), stderr.Bytes(), true) {
			result.Classification = "unaccounted_failure_evidence"
			return result
		}
		result.Status = "fail"
		result.Classification = "test_failure"
		result.Valid = true
		return result
	}
	result.Classification = classifyOutput(parsed.Diagnostic + out.String() + stderr.String())
	if result.Classification == "compile_error" {
		result.Status = "invalid"
	}
	return result
}
func applyMutation(dir string, s mutationSite) error {
	p := filepath.Join(dir, filepath.FromSlash(s.Path))
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	if s.Offset < 0 || s.Offset+len(s.Before) > len(b) || string(b[s.Offset:s.Offset+len(s.Before)]) != s.Before {
		return fmt.Errorf("mutation token does not match source")
	}
	updated := append([]byte{}, b[:s.Offset]...)
	updated = append(updated, s.After...)
	updated = append(updated, b[s.Offset+len(s.Before):]...)
	return os.WriteFile(p, updated, 0600)
}
func materializeMutationCopy(entries []snapshotEntry) (string, error) {
	d, e := os.MkdirTemp("", "brag-mutation-snapshot-")
	if e != nil {
		return "", e
	}
	for _, x := range entries {
		if !safeRelative(x.path) {
			os.RemoveAll(d)
			return "", fmt.Errorf("unsafe path")
		}
		p := filepath.Join(d, filepath.FromSlash(x.path))
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			os.RemoveAll(d)
			return "", e
		}
		mode := os.FileMode(0600)
		if x.mode == "100755" {
			mode = 0700
		}
		if e = os.WriteFile(p, x.data, mode); e != nil {
			os.RemoveAll(d)
			return "", e
		}
	}
	return d, nil
}
func persistMutationLog(dir string, budget *artifactBudget, prefix string, output []byte) (string, string) {
	if len(output) == 0 {
		return "", ""
	}
	name := writeDynamicArtifact(dir, budget, prefix, ".log", output)
	if name == "" {
		return "", ""
	}
	return name, hashBytes(output)
}

func runMutationTests(parent context.Context, scope, head string, base *baseInfo, root string, limit int, per, total time.Duration, artifactPath string, packages, paths, snapshots []string) *mutationReport {
	r := &mutationReport{Status: "unavailable", OperatorVersion: mutationOperatorVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, PerRunTimeoutMillis: per.Milliseconds(), TotalTimeoutMillis: total.Milliseconds(), TestConfiguration: map[string]string{"command": "go test -json -count=1 -shuffle=off ./...", "count": "1", "shuffle": "off"}, Snapshots: []mutationSnapshot{}, Limitations: []string{"Mutation tests execute arbitrary repository code with current-user host privileges and host API access; this is not sandboxed", "Go module lookup is offline-constrained, but test code may use the network", "No individual-test or developer contribution is inferred", "Boolean true/false substitutions are limited to references resolved as the predeclared universe constants"}}
	specs := []snapshotSpec{}
	if scope == "branch" && base != nil {
		specs = []snapshotSpec{{"baseline", base.MergeBase, false}, {"HEAD", head, false}, {"current_tracked_worktree", head, true}}
	} else if scope == "all" {
		specs = []snapshotSpec{{"current_tracked_worktree", head, true}}
	}
	if scope == "all" && head == "" {
		r.Snapshots = append(r.Snapshots, mutationSnapshot{Name: "current_tracked_worktree", Status: "unavailable", Counts: emptyMutationCounts(), Diagnostic: "repository has no HEAD/current tracked snapshot"})
		r.Limitations = append(r.Limitations, "repository has no HEAD/current tracked snapshot")
		return r
	}
	allowed := map[string]bool{}
	for _, sp := range snapshots {
		allowed[sp] = true
	}
	if len(allowed) == 0 {
		if scope == "branch" {
			allowed["HEAD"] = true
		} else {
			allowed["current_tracked_worktree"] = true
		}
	}
	failSnapshots := func(status, reason string) {
		for _, sp := range specs {
			if allowed[sp.label] {
				r.Snapshots = append(r.Snapshots, mutationSnapshot{Name: sp.label, Status: status, Counts: emptyMutationCounts(), Diagnostic: reason})
			}
		}
	}
	if !supportsProcessGroupCleanup() || artifactPath == "" {
		r.Limitations = append(r.Limitations, "process cleanup and an external private artifact directory are required")
		failSnapshots("unavailable", "execution/artifact preflight unavailable")
		return r
	}
	abs, e := canonicalCandidate(root, artifactPath)
	if e != nil || pathWithin(root, abs) {
		r.Limitations = append(r.Limitations, "unsafe artifact directory")
		failSnapshots("unavailable", "unsafe artifact directory")
		return r
	}
	if os.MkdirAll(abs, 0700) != nil {
		failSnapshots("unavailable", "artifact directory unavailable")
		return r
	}
	resolved, e := filepath.EvalSymlinks(abs)
	fi, se := os.Stat(abs)
	if e != nil || pathWithin(root, resolved) || se != nil || !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		r.Limitations = append(r.Limitations, "artifact directory must be private and outside repository")
		failSnapshots("unavailable", "artifact directory is not private/outside repository")
		return r
	}
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	gv, e := goVersionContext(ctx)
	if e != nil {
		status, reason := "inconclusive", "local Go toolchain unavailable"
		if parent.Err() != nil {
			status, reason = "cancelled", "caller cancellation before toolchain check"
		} else if ctx.Err() != nil {
			status, reason = "inconclusive", "total timeout before toolchain check"
		}
		failSnapshots(status, reason)
		if parent.Err() != nil {
			r.Status = "cancelled"
		} else {
			r.Status = "partial"
		}
		return r
	}
	r.GoVersion = gv
	budget := &artifactBudget{}
	r.artifactBudget = budget
	r.artifactDir = abs
	started := time.Now()
	for _, sp := range specs {
		if !allowed[sp.label] {
			continue
		}
		r.Snapshots = append(r.Snapshots, mutationSnapshot{Name: sp.label, Status: "unavailable", Counts: emptyMutationCounts()})
		idx := len(r.Snapshots) - 1
		sm := &r.Snapshots[idx]
		if ctx.Err() != nil {
			if parent.Err() != nil {
				r.Snapshots[idx].Status = "cancelled"
			} else {
				r.Snapshots[idx].Status = "inconclusive"
			}
			r.Snapshots[idx].Diagnostic = "total timeout or caller cancellation before snapshot"
			continue
		}
		dir, _, err := materializeDynamicSnapshotContext(ctx, root, sp)
		if err != nil {
			r.Snapshots[idx].Status = "unavailable"
			r.Snapshots[idx].Diagnostic = "snapshot materialization failed"
			continue
		}
		entries, _, err := snapshotEntriesForMutation(dir)
		os.RemoveAll(dir)
		if err != nil {
			r.Snapshots[idx].Status = "unavailable"
			r.Snapshots[idx].Diagnostic = "snapshot inventory failed"
			continue
		}
		sm.SourceIdentity = mutationSourceIdentity(entries, false)
		sm.TestIdentity = mutationSourceIdentity(entries, true)
		sites, known, incomplete, skipped, err := mutationSites(entries, packages, paths)
		if err != nil {
			sm.Status = "inconclusive"
			sm.Diagnostic = err.Error()
			continue
		}
		sm.Eligible = len(sites)
		sm.PopulationIncomplete = incomplete
		if !incomplete {
			zero := 0
			sm.Unexamined = &zero
		} else {
			sm.Unexamined = nil
		}
		if len(skipped) > 0 {
			r.Limitations = append(r.Limitations, fmt.Sprintf("%s skipped/unsupported files: %s", sp.label, strings.Join(skipped, ",")))
		}
		if incomplete {
			sm.Status = "partial"
			sm.Unexamined = nil
			sm.Diagnostic = fmt.Sprintf("population incomplete; at least %d candidates observed", known)
		}
		selected := append([]mutationSite(nil), sites...)
		sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
		if len(selected) > limit {
			selected = selected[:limit]
		}
		sm.Selected = len(selected)
		sm.Eligible = known
		sm.Unselected = max(0, known-len(selected))
		packageFilters := append([]string{}, packages...)
		pathFilters := append([]string{}, paths...)
		sort.Strings(packageFilters)
		sort.Strings(pathFilters)
		manifest := mutationManifest{1, "brag-mutation-manifest", mutationOperatorVersion, sp.label, sm.SourceIdentity, sm.TestIdentity, limit, packageFilters, pathFilters, selected}
		mb, _ := json.Marshal(manifest)
		if len(mb) > dynamicArtifactBytes {
			sm.Status = "inconclusive"
			sm.Diagnostic = "manifest exceeds artifact size cap"
			for range selected {
				sm.Counts["inconclusive"]++
			}
			continue
		}
		sm.ManifestArtifact = writeDynamicArtifact(abs, budget, "mutation-manifest-", ".json", mb)
		if sm.ManifestArtifact == "" {
			sm.Status = "inconclusive"
			sm.Diagnostic = "manifest artifact could not be persisted; control and mutants not executed"
			for range selected {
				sm.Counts["inconclusive"]++
			}
			continue
		}
		sm.ManifestSHA256 = hashBytes(mb)
		controlDir, err := materializeMutationCopy(entries)
		control := mutationExecution{Status: "inconclusive", Classification: "control_setup_error", Tests: []dynamicTest{}}
		if err == nil {
			control = executeMutation(ctx, controlDir, per)
			os.RemoveAll(controlDir)
		} else {
			sm.Diagnostic = "control snapshot setup failed"
		}
		controlLog, controlLogHash := persistMutationLog(abs, budget, "mutation-control-", control.Output)
		if len(control.Output) > 0 && controlLog == "" {
			control.Valid = false
			control.Status = "inconclusive"
			control.Classification = "control_log_artifact_unavailable"
		}
		outcome := mutationOutcomeArtifact{FormatVersion: 1, Kind: "brag-mutation-outcomes", Control: control, ControlLogArtifact: controlLog, ControlLogSHA256: controlLogHash, Mutants: []mutationOutcome{}}
		outcome.Control.Output = nil
		for i, site := range selected {
			mo := mutationOutcome{ID: site.ID, Path: site.Path, Operator: site.Operator, Tests: []dynamicTest{}}
			if !control.Valid || control.Status != "pass" {
				mo.Status = "inconclusive"
				mo.Classification = "control_" + control.Classification
			} else if ctx.Err() != nil {
				mo.Status = "not_executed"
				if parent.Err() != nil {
					mo.Classification = "caller_cancellation"
				} else {
					mo.Classification = "total_timeout"
				}
			} else if time.Since(started) >= total || budget.used >= dynamicArtifactTotalBytes {
				mo.Status = "not_executed"
				mo.Classification = "budget_exhausted"
			} else {
				mutDir, ce := materializeMutationCopy(entries)
				if ce != nil {
					mo.Status = "inconclusive"
					mo.Classification = "setup_error"
				} else if ce = applyMutation(mutDir, site); ce != nil {
					mo.Status = "invalid"
					mo.Classification = "source_mismatch"
					os.RemoveAll(mutDir)
				} else {
					ex := executeMutation(ctx, mutDir, per)
					os.RemoveAll(mutDir)
					mo.Status = ex.Status
					mo.Classification = ex.Classification
					mo.ElapsedMillis = ex.ElapsedMillis
					mo.Tests = ex.Tests
					mo.LogArtifact, mo.LogSHA256 = persistMutationLog(abs, budget, "mutation-run-", ex.Output)
					if ex.Status == "fail" && ex.Classification != "test_failure" {
						mo.Status = "inconclusive"
					}
					if (ex.Status == "pass" || ex.Status == "fail") && mo.LogArtifact == "" {
						mo.Status = "inconclusive"
						mo.Classification = "log_artifact_unavailable"
					}
				}
			}
			if !control.Valid || control.Status != "pass" {
			} else if mo.Status == "fail" && mo.Classification == "test_failure" {
				mo.Status = "killed"
			} else if mo.Status == "pass" {
				mo.Status = "survived"
			}
			outcome.Mutants = append(outcome.Mutants, mo)
			_ = i
		}
		// Control log and mutant logs are retained as separate bounded artifacts; outcomes contain their hashes.
		outcome.Control.Output = nil
		outcome.Control.Classification = control.Classification
		outcome.Control.Status = control.Status
		ob, _ := json.Marshal(outcome)
		if len(ob) > dynamicArtifactBytes {
			sm.Diagnostic = "outcome artifact exceeds per-artifact limit"
		} else {
			sm.OutcomeArtifact = writeDynamicArtifact(abs, budget, "mutation-outcomes-", ".json", ob)
			if sm.OutcomeArtifact != "" {
				sm.OutcomeSHA256 = hashBytes(ob)
			}
		}
		if sm.OutcomeArtifact == "" {
			sm.Status = "inconclusive"
			sm.Diagnostic = "outcome artifact unavailable; no score evidence"
			for _, m := range outcome.Mutants {
				if m.Status == "killed" || m.Status == "survived" {
					sm.Counts["inconclusive"]++
				} else {
					sm.Counts[m.Status]++
				}
			}
			sm.Counts["killed"], sm.Counts["survived"] = 0, 0
			sm.Score = nil
			continue
		}
		for _, m := range outcome.Mutants {
			sm.Counts[m.Status]++
		}
		if sm.Counts["not_executed"] > 0 || ctx.Err() != nil {
			if parent.Err() != nil {
				sm.Status = "cancelled"
			} else {
				sm.Status = "partial"
			}
		}
		if sm.Counts["killed"]+sm.Counts["survived"] > 0 {
			score := float64(sm.Counts["killed"]) / float64(sm.Counts["killed"]+sm.Counts["survived"])
			sm.Score = &score
		}
		if len(outcome.Mutants) != sm.Selected {
			sm.Status = "partial"
			sm.Diagnostic = "outcomes do not reconcile to selected sites"
		} else if sm.Status != "partial" {
			sm.Status = "measured"
		}
	}
	if parent.Err() != nil {
		r.Status = "cancelled"
	} else if ctx.Err() != nil {
		r.Status = "partial"
	} else {
		r.Status = "measured"
		for _, s := range r.Snapshots {
			if s.Status != "measured" {
				r.Status = "partial"
			}
		}
	}
	return r
}
func emptyMutationCounts() map[string]int {
	return map[string]int{"killed": 0, "survived": 0, "invalid": 0, "inconclusive": 0, "not_executed": 0}
}
func snapshotEntriesForMutation(dir string) ([]snapshotEntry, string, error) {
	paths := []string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Strings(paths)
	out := []snapshotEntry{}
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		b, e := os.ReadFile(full)
		if e != nil {
			return nil, "", e
		}
		mode := "100644"
		if fi, e := os.Stat(full); e != nil {
			return nil, "", e
		} else if fi.Mode().Perm()&0111 != 0 {
			mode = "100755"
		}
		out = append(out, snapshotEntry{p, mode, b})
	}
	return out, hashString(mutationSourceIdentity(out, false) + mutationSourceIdentity(out, true)), nil
}
func hashString(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
