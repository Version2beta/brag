package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const complexityDetailLimit = 100
const complexityFileLimit = 2000
const complexityFileBytes = 1 << 20
const complexityTotalBytes = 20 << 20

type metricSet struct {
	Cyclomatic int `json:"cyclomatic"`
	Cognitive  int `json:"cognitive"`
	MaxNesting int `json:"max_control_nesting"`
	Lines      int `json:"physical_function_lines"`
}
type distribution struct {
	Count                int     `json:"count"`
	Median               int     `json:"median"`
	P95                  int     `json:"p95"`
	Max                  int     `json:"max"`
	TopDecileShare       float64 `json:"top_decile_share"`
	Threshold            int     `json:"threshold"`
	ThresholdExceedances int     `json:"threshold_exceedances"`
}
type snapshotComplexity struct {
	Status                 string                  `json:"status"`
	Snapshot               string                  `json:"snapshot"`
	Functions              int                     `json:"function_count"`
	Distributions          map[string]distribution `json:"distributions"`
	ParseFailures          []string                `json:"parse_failures,omitempty"`
	ParseFailuresTruncated bool                    `json:"parse_failures_truncated"`
	SkippedFiles           int                     `json:"skipped_files"`
	UnsupportedLanguages   map[string]int          `json:"unsupported_language_files,omitempty"`
}
type functionChange struct {
	Key    string     `json:"key"`
	Change string     `json:"change"`
	Before *metricSet `json:"before,omitempty"`
	After  *metricSet `json:"after,omitempty"`
}
type comparison struct {
	From                     string           `json:"from"`
	To                       string           `json:"to"`
	Added                    int              `json:"added_functions"`
	Removed                  int              `json:"removed_functions"`
	Modified                 int              `json:"modified_functions"`
	Unchanged                int              `json:"unchanged_functions"`
	MatchedComplexityChanged int              `json:"matched_complexity_changed"`
	Ambiguity                string           `json:"identity_ambiguity"`
	Details                  []functionChange `json:"changed_function_details"`
	DetailsTruncated         bool             `json:"details_truncated"`
}
type evidenceMetric struct {
	Status   string   `json:"status"`
	Method   string   `json:"method"`
	Units    string   `json:"units,omitempty"`
	Count    int      `json:"count,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}
type complexityEvidence struct {
	Status      string               `json:"status"`
	Method      string               `json:"method"`
	Algorithm   string               `json:"algorithm"`
	Snapshots   []snapshotComplexity `json:"snapshots"`
	Comparisons []comparison         `json:"comparisons"`
	Limits      string               `json:"limits"`
	Limitations []string             `json:"limitations"`
}
type testSnapshot struct {
	Status                 string           `json:"status"`
	Snapshot               string           `json:"snapshot"`
	TestFiles              int              `json:"test_file_count"`
	Tests                  int              `json:"test_functions"`
	TestMain               int              `json:"test_main_functions"`
	Benchmarks             int              `json:"benchmark_functions"`
	Examples               int              `json:"example_functions"`
	SubtestSites           int              `json:"subtest_run_sites"`
	TableDrivenSites       int              `json:"table_driven_test_function_sites"`
	ProductionFunctions    int              `json:"production_function_declarations"`
	TestToProduction       ratio            `json:"test_to_production_ratio"`
	TestRatioUnits         string           `json:"test_to_production_ratio_units"`
	Definitions            []testDefinition `json:"definitions"`
	DefinitionsTruncated   bool             `json:"definitions_truncated"`
	ParseFailures          []string         `json:"parse_failures,omitempty"`
	ParseFailuresTruncated bool             `json:"parse_failures_truncated"`
	UnsupportedLanguages   map[string]int   `json:"unsupported_language_files,omitempty"`
}
type testDefinition struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	BodyHash string `json:"definition_hash"`
}
type testComparison struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Modified  int    `json:"modified"`
	Unchanged int    `json:"unchanged"`
	Ambiguity string `json:"identity_ambiguity"`
}
type attributedRatio struct {
	Status          string `json:"status"`
	Numerator       *int   `json:"numerator"`
	Denominator     int    `json:"denominator"`
	DenominatorUnit string `json:"denominator_unit"`
	Reason          string `json:"reason,omitempty"`
}
type testEvidence struct {
	Status                  string           `json:"status"`
	Method                  string           `json:"method"`
	Snapshots               []testSnapshot   `json:"snapshots"`
	Comparisons             []testComparison `json:"comparisons"`
	HistoricalAttribution   evidenceMetric   `json:"historical_attribution"`
	SelectedTestsAddedRatio attributedRatio  `json:"selected_tests_added_ratio"`
	AllTestsAddedRatio      attributedRatio  `json:"all_tests_added_ratio"`
	Limitations             []string         `json:"limitations"`
}
type engineeringEvidence struct {
	Version       string                `json:"algorithm_version"`
	Complexity    complexityEvidence    `json:"complexity"`
	TestInventory testEvidence          `json:"test_inventory"`
	Documentation documentationEvidence `json:"documentation"`
	Architecture  architectureEvidence  `json:"architecture"`
	Security      securityEvidence      `json:"security"`
	Duplication   evidenceMetric        `json:"duplication"`
	Scope         string                `json:"scope"`
}
type functionInfo struct {
	M      metricSet
	Source string
}
type functionMap map[string]functionInfo
type snapshotInput struct {
	label, rev string
	overlay    bool
}

func buildEngineeringEvidence(scope, head string, base *baseInfo, root string, commits []string, selected identityInfo, identities maps) engineeringEvidence {
	out := engineeringEvidence{Version: "brag-go-ast-v2", Scope: "Repository-wide Go AST evidence; never attributed to selected identity", Documentation: documentationEvidence{Status: "unavailable", Method: "Go AST comments and tracked documentation content hashes", Limits: "Go/doc file and byte limits apply", Snapshots: []docSnapshot{}, Comparisons: []docComparison{}, Limitations: []string{"Repository has no HEAD snapshot"}}, Duplication: evidenceMetric{Status: "unavailable", Method: "No duplication algorithm", Reason: "duplication is deliberately unmeasured; no simplistic source-text metric is used"}}
	out.TestInventory = testEvidence{Status: "unavailable", Method: "Go test AST conventions; historical attribution unavailable", Snapshots: []testSnapshot{}, Comparisons: []testComparison{}, HistoricalAttribution: evidenceMetric{Status: "unavailable", Method: "per-commit first-parent symbol comparison", Reason: "no HEAD snapshot"}, SelectedTestsAddedRatio: attributedRatio{Status: "unavailable", DenominatorUnit: "newly introduced TestX functions", Reason: "no HEAD snapshot"}, AllTestsAddedRatio: attributedRatio{Status: "unavailable", DenominatorUnit: "included commits", Reason: "no HEAD snapshot"}, Limitations: []string{"Historical additions are not attributed to authors in this implementation"}}
	ce := complexityEvidence{Status: "measured", Snapshots: []snapshotComplexity{}, Comparisons: []comparison{}, Method: "Custom brag-go-ast-v2. Cyclomatic=1 plus if/for/range/switch/select/case/comm-clause nodes and each &&/||; cognitive adds control constructs plus nesting depth and else; cases add cyclomatic paths but do not nest; function literals are excluded from parent metrics and not independently keyed; physical lines include declaration start/end.", Algorithm: "brag-go-ast-v2 (custom; not a standard score)", Limits: fmt.Sprintf("Maximum %d Go files/snapshot, %d bytes/file, %d total bytes/snapshot; changed details capped at %d and parse-failure paths at 50.", complexityFileLimit, complexityFileBytes, complexityTotalBytes, complexityDetailLimit), Limitations: []string{"Identity is package/file/receiver/name; moves/renames/splits/merges may appear as removals/additions", "AST syntax only; build tags/configuration/runtime are not evaluated", "Complexity evidence is repository-wide and is not attributed to selected identity"}}
	if head == "" {
		out.Architecture = buildArchitectureEvidence(root, nil)
		out.Security = buildSecurityEvidence(root, nil)
		ce.Status = "unavailable"
		ce.Limitations = append(ce.Limitations, "Repository has no HEAD snapshot")
		out.Complexity = ce
		return out
	}
	inputs := []snapshotInput{}
	if scope == "branch" && base != nil {
		inputs = append(inputs, snapshotInput{"baseline", base.MergeBase, false})
	}
	inputs = append(inputs, snapshotInput{"HEAD", head, false}, snapshotInput{"current_tracked_worktree", head, true})
	maps := []functionMap{}
	bad := false
	for _, in := range inputs {
		fm, s := loadComplexitySnapshot(root, in)
		maps = append(maps, fm)
		ce.Snapshots = append(ce.Snapshots, s)
		if s.Status != "measured" {
			bad = true
		}
	}
	if bad {
		ce.Status = "partial"
	}
	if scope == "branch" && len(maps) == 3 {
		ce.Comparisons = append(ce.Comparisons, compareFunctions("baseline", "HEAD", maps[0], maps[1]), compareFunctions("HEAD", "current_tracked_worktree", maps[1], maps[2]))
	} else if len(maps) == 2 {
		ce.Comparisons = append(ce.Comparisons, compareFunctions("HEAD", "current_tracked_worktree", maps[0], maps[1]))
	}
	out.Complexity = ce
	out.TestInventory = buildTestEvidence(root, inputs, commits, selected, identities)
	out.Documentation = buildDocumentationEvidence(root, inputs)
	out.Architecture = buildArchitectureEvidence(root, inputs)
	out.Security = buildSecurityEvidence(root, inputs)
	return out
}
func runGit(root string, args ...string) ([]byte, error) {
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	c.Env = gitEnvironment(os.Environ())
	return c.Output()
}
func snapshotPaths(root string, in snapshotInput) ([]string, map[string]string, map[string]string, error) {
	b, e := runGit(root, "ls-tree", "-r", "-z", "--full-tree", in.rev)
	if in.overlay {
		b, e = runGit(root, "ls-files", "-z")
	}
	paths := []string{}
	hashes := map[string]string{}
	modes := map[string]string{}
	if e != nil {
		return nil, hashes, modes, e
	}
	for _, ent := range bytes.Split(b, []byte{0}) {
		if len(ent) == 0 {
			continue
		}
		if in.overlay {
			paths = append(paths, string(ent))
			continue
		}
		p := bytes.SplitN(ent, []byte("\t"), 2)
		if len(p) != 2 {
			continue
		}
		f := strings.Fields(string(p[0]))
		if len(f) != 3 || f[1] != "blob" {
			continue
		}
		path := string(p[1])
		paths = append(paths, path)
		hashes[path] = f[2]
		modes[path] = f[0]
	}
	sort.Strings(paths)
	return paths, hashes, modes, nil
}
func readSnapshotFile(root string, in snapshotInput, path, hash, mode string, total int) ([]byte, bool, bool) {
	if in.overlay {
		full := filepath.Join(root, filepath.FromSlash(path))
		fi, e := os.Lstat(full)
		if os.IsNotExist(e) {
			return nil, false, false
		}
		if e != nil || !fi.Mode().IsRegular() || hasSymlinkParent(root, path) {
			return nil, false, true
		}
		if fi.Size() > complexityFileBytes || total+int(fi.Size()) > complexityTotalBytes {
			return nil, false, true
		}
		b, e := os.ReadFile(full)
		return b, e == nil, e != nil
	}
	if mode != "100644" && mode != "100755" {
		return nil, false, true
	}
	sz, e := runGit(root, "cat-file", "-s", hash)
	n, pe := strconv.Atoi(strings.TrimSpace(string(sz)))
	if e != nil || pe != nil || n > complexityFileBytes || total+n > complexityTotalBytes {
		return nil, false, true
	}
	b, e := runGit(root, "cat-file", "blob", hash)
	if e != nil || len(b) != n {
		return nil, false, true
	}
	return b, true, false
}
func loadComplexitySnapshot(root string, in snapshotInput) (functionMap, snapshotComplexity) {
	fm := functionMap{}
	s := snapshotComplexity{Snapshot: in.label, Status: "measured", Distributions: map[string]distribution{}, UnsupportedLanguages: map[string]int{}}
	paths, hashes, modes, e := snapshotPaths(root, in)
	if e != nil {
		s.Status = "failed"
		return fm, s
	}
	total, files := 0, 0
	for _, p := range paths {
		if source(p) && filepath.Ext(p) != ".go" {
			s.UnsupportedLanguages[strings.ToLower(filepath.Ext(p))]++
		}
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		if files >= complexityFileLimit {
			s.SkippedFiles++
			continue
		}
		files++
		b, ok, skip := readSnapshotFile(root, in, p, hashes[p], modes[p], total)
		if skip {
			s.SkippedFiles++
			continue
		}
		if !ok {
			continue
		}
		total += len(b)
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, p, b, parser.AllErrors)
		if err != nil {
			s.ParseFailures, s.ParseFailuresTruncated = appendBounded(s.ParseFailures, p, 50)
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			recv := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				recv = receiverName(fn.Recv.List[0].Type)
			}
			key := f.Name.Name + "/" + p + "/" + recv + "/" + fn.Name.Name
			st, en := fs.Position(fn.Pos()).Line, fs.Position(fn.End()).Line
			fm[key] = functionInfo{functionMetrics(fn, st, en), string(b[fn.Pos()-1 : fn.End()-1])}
		}
	}
	if len(s.ParseFailures) > 0 || s.SkippedFiles > 0 {
		s.Status = "partial"
	}
	s.Functions = len(fm)
	s.Distributions = makeDistributions(fm)
	return fm, s
}
func hasSymlinkParent(root, path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	cur := root
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		i, e := os.Lstat(cur)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
func appendBounded(a []string, s string, n int) ([]string, bool) {
	if len(a) >= n {
		return a, true
	}
	return append(a, s), false
}
func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.IndexListExpr:
		return receiverName(x.X)
	case *ast.SelectorExpr:
		return receiverName(x.X) + "." + x.Sel.Name
	}
	return "?"
}
func functionMetrics(fn *ast.FuncDecl, start, end int) metricSet {
	m := metricSet{Cyclomatic: 1, Lines: end - start + 1}
	var visit func(ast.Node, int)
	visit = func(n ast.Node, depth int) {
		if n == nil {
			return
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return
		}
		next := depth
		control := false
		switch n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			control = true
			m.Cyclomatic++
			m.Cognitive += 1 + depth
		case *ast.CaseClause, *ast.CommClause:
			m.Cyclomatic++
		}
		if control {
			next++
			if next > m.MaxNesting {
				m.MaxNesting = next
			}
		}
		if x, ok := n.(*ast.IfStmt); ok && x.Else != nil {
			m.Cognitive++
		}
		if b, ok := n.(*ast.BinaryExpr); ok && (b.Op.String() == "&&" || b.Op.String() == "||") {
			m.Cyclomatic++
		}
		ast.Inspect(n, func(ch ast.Node) bool {
			if ch == nil {
				return false
			}
			if ch == n {
				return true
			}
			visit(ch, next)
			return false
		})
	}
	visit(fn.Body, 0)
	return m
}
func makeDistributions(fm functionMap) map[string]distribution {
	vals := map[string][]int{"cyclomatic": {}, "cognitive": {}, "max_control_nesting": {}, "physical_function_lines": {}}
	for _, f := range fm {
		vals["cyclomatic"] = append(vals["cyclomatic"], f.M.Cyclomatic)
		vals["cognitive"] = append(vals["cognitive"], f.M.Cognitive)
		vals["max_control_nesting"] = append(vals["max_control_nesting"], f.M.MaxNesting)
		vals["physical_function_lines"] = append(vals["physical_function_lines"], f.M.Lines)
	}
	out := map[string]distribution{}
	th := map[string]int{"cyclomatic": 10, "cognitive": 15, "max_control_nesting": 4, "physical_function_lines": 100}
	for k, v := range vals {
		sort.Ints(v)
		d := distribution{Count: len(v), Threshold: th[k]}
		if len(v) > 0 {
			d.Max = v[len(v)-1]
			d.Median = v[(len(v)-1)/2]
			d.P95 = v[(95*len(v)+99)/100-1]
			top := (len(v) + 9) / 10
			sum := 0
			for i, x := range v {
				sum += x
				if x > d.Threshold {
					d.ThresholdExceedances++
				}
				if i >= len(v)-top {
					d.TopDecileShare += float64(x)
				}
			}
			if sum > 0 {
				d.TopDecileShare /= float64(sum)
			}
		}
		out[k] = d
	}
	return out
}
func compareFunctions(from, to string, a, b functionMap) comparison {
	c := comparison{From: from, To: to, Ambiguity: "moves/renames/splits/merges cannot be reliably matched; identity uses package/file/receiver/name", Details: []functionChange{}}
	ks := map[string]bool{}
	for k := range a {
		ks[k] = true
	}
	for k := range b {
		ks[k] = true
	}
	ordered := []string{}
	for k := range ks {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		x, xok := a[k]
		y, yok := b[k]
		ch := ""
		var before, after *metricSet
		switch {
		case !xok:
			c.Added++
			ch = "added"
			z := y.M
			after = &z
		case !yok:
			c.Removed++
			ch = "removed"
			z := x.M
			before = &z
		case x.Source == y.Source:
			c.Unchanged++
			continue
		default:
			c.Modified++
			ch = "modified"
			z, w := x.M, y.M
			before = &z
			after = &w
			if x.M != y.M {
				c.MatchedComplexityChanged++
			}
		}
		if len(c.Details) < complexityDetailLimit {
			c.Details = append(c.Details, functionChange{k, ch, before, after})
		} else {
			c.DetailsTruncated = true
		}
	}
	return c
}

func buildTestEvidence(root string, inputs []snapshotInput, commits []string, selected identityInfo, identities maps) testEvidence {
	e := testEvidence{Status: "measured", Method: "Tracked Go AST inventory: valid top-level *_test.go conventions; historical additions count TestX only; examples listed separately; testing-parameter Run sites are call sites; table-driven heuristic requires range, composite literal and Run in same executable test.", HistoricalAttribution: evidenceMetric{Status: "unavailable", Method: "per-commit first-parent TestX definition comparison", Reason: "historical author attribution is not implemented; no introduced-test attribution is inferred"}, SelectedTestsAddedRatio: attributedRatio{Status: "unavailable", DenominatorUnit: "newly introduced TestX functions", Reason: "historical TestX attribution unavailable"}, AllTestsAddedRatio: attributedRatio{Status: "unavailable", DenominatorUnit: "included commits", Reason: "historical TestX introduction counts unavailable"}, Limitations: []string{"Stable key includes file/name/kind; moves/renames are ambiguous", "Example inventory is naming/signature based and does not establish output-based go test execution", "Historical test additions and selected/all attribution ratios unavailable", fmt.Sprintf("Per snapshot Go source caps: %d files, %d bytes/file, %d total bytes; definition details cap at 500 and parse-failure paths at 50.", complexityFileLimit, complexityFileBytes, complexityTotalBytes)}}
	maps := []map[string]testDefinition{}
	for _, in := range inputs {
		s, m := loadTestSnapshot(root, in)
		e.Snapshots = append(e.Snapshots, s)
		maps = append(maps, m)
		if s.Status != "measured" {
			e.Status = "partial"
		}
	}
	if len(maps) == 3 {
		e.Comparisons = append(e.Comparisons, compareTests("baseline", "HEAD", maps[0], maps[1]), compareTests("HEAD", "current_tracked_worktree", maps[1], maps[2]))
	} else if len(maps) == 2 {
		e.Comparisons = append(e.Comparisons, compareTests("HEAD", "current_tracked_worktree", maps[0], maps[1]))
	}
	return applyHistoricalTestAttribution(e, root, commits, selected, identities)
}
func loadTestSnapshot(root string, in snapshotInput) (testSnapshot, map[string]testDefinition) {
	s := testSnapshot{Status: "measured", Snapshot: in.label, Definitions: []testDefinition{}, TestRatioUnits: "executable TestX functions / non-test named Go function declarations", UnsupportedLanguages: map[string]int{}}
	defs := map[string]testDefinition{}
	paths, hashes, modes, err := snapshotPaths(root, in)
	if err != nil {
		s.Status = "failed"
		return s, defs
	}
	total, files := 0, 0
	for _, path := range paths {
		if source(path) && filepath.Ext(path) != ".go" {
			s.UnsupportedLanguages[strings.ToLower(filepath.Ext(path))]++
		}
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if files >= complexityFileLimit {
			s.Status = "partial"
			continue
		}
		files++
		b, ok, skipped := readSnapshotFile(root, in, path, hashes[path], modes[path], total)
		if skipped {
			s.Status = "partial"
			continue
		}
		if !ok {
			continue
		}
		total += len(b)
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, b, parser.AllErrors)
		if parseErr != nil {
			s.Status = "partial"
			if len(s.ParseFailures) >= 50 {
				s.ParseFailuresTruncated = true
			} else {
				s.ParseFailures = append(s.ParseFailures, path)
			}
			continue
		}
		isTestFile := strings.HasSuffix(path, "_test.go")
		if isTestFile {
			s.TestFiles++
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if !isTestFile {
				s.ProductionFunctions++
				continue
			}
			if fn.Recv != nil || fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) > 0) || fn.Body == nil {
				continue
			}
			kind := ""
			switch {
			case fn.Name.Name == "TestMain" && testingParam(f, fn, "M"):
				kind = "test_main"
			case validTestName(fn.Name.Name, "Test") && testingParam(f, fn, "T"):
				kind = "test"
			case validTestName(fn.Name.Name, "Benchmark") && testingParam(f, fn, "B"):
				kind = "benchmark"
			case exampleName(fn.Name.Name) && (fn.Type.Params == nil || len(fn.Type.Params.List) == 0):
				kind = "example"
			}
			if kind == "" {
				continue
			}
			start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
			hash := sha256.Sum256(b[start:end])
			key := path + "/" + kind + "/" + fn.Name.Name
			defs[key] = testDefinition{key, kind, fmt.Sprintf("%x", hash)}
			switch kind {
			case "test":
				s.Tests++
				sites, table := testStructure(fn)
				s.SubtestSites += sites
				if table {
					s.TableDrivenSites++
				}
			case "test_main":
				s.TestMain++
			case "benchmark":
				s.Benchmarks++
			case "example":
				s.Examples++
			}
		}
	}
	s.Definitions = make([]testDefinition, 0, len(defs))
	for _, d := range defs {
		s.Definitions = append(s.Definitions, d)
	}
	sort.Slice(s.Definitions, func(i, j int) bool { return s.Definitions[i].Key < s.Definitions[j].Key })
	if len(s.Definitions) > 500 {
		s.Definitions = s.Definitions[:500]
		s.DefinitionsTruncated = true
	}
	s.TestToProduction = makeRatio(s.Tests, s.ProductionFunctions)
	return s, defs
}
func testingParam(file *ast.File, fn *ast.FuncDecl, name string) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	p, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := p.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "testing" {
			continue
		}
		alias := filepath.Base(path)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias == pkg.Name {
			return true
		}
	}
	return false
}
func validTestName(s, prefix string) bool {
	if !strings.HasPrefix(s, prefix) || len(s) == len(prefix) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[len(prefix):])
	return !unicode.IsLower(r)
}
func exampleName(s string) bool {
	if !strings.HasPrefix(s, "Example") {
		return false
	}
	tail := strings.TrimPrefix(s, "Example")
	parts := strings.Split(tail, "_")
	base := parts[0]
	if base != "" {
		r, _ := utf8.DecodeRuneInString(base)
		if !unicode.IsUpper(r) {
			return false
		}
		for _, r := range base {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	if len(parts) > 2 {
		return false
	}
	if len(parts) == 2 {
		suffix := parts[1]
		if suffix == "" {
			return false
		}
		r, _ := utf8.DecodeRuneInString(suffix)
		if !unicode.IsLower(r) {
			return false
		}
		for _, r := range suffix {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}
func testStructure(fn *ast.FuncDecl) (int, bool) {
	sites := 0
	testingParamName := ""
	if fn.Type.Params != nil && len(fn.Type.Params.List) == 1 && len(fn.Type.Params.List[0].Names) == 1 {
		testingParamName = fn.Type.Params.List[0].Names[0].Name
	}
	hasRange, hasLiteral := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.RangeStmt:
			hasRange = true
		case *ast.CompositeLit:
			hasLiteral = true
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
				if id, ok := sel.X.(*ast.Ident); ok && testingParamName != "" && id.Name == testingParamName {
					sites++
				}
			}
		}
		return true
	})
	return sites, sites > 0 && hasRange && hasLiteral
}
func compareTests(from, to string, a, b map[string]testDefinition) testComparison {
	c := testComparison{From: from, To: to, Ambiguity: "definition identity is file/kind/name; moves and renames may appear as remove/add"}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		x, xok := a[k]
		y, yok := b[k]
		switch {
		case !xok:
			c.Added++
		case !yok:
			c.Removed++
		case x.BodyHash == y.BodyHash:
			c.Unchanged++
		default:
			c.Modified++
		}
	}
	return c
}
