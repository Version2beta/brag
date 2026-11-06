package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const architectureDetailLimit = 500
const architectureImportLimit = 20000
const securityIndicatorLimit = 500

type integerDistribution struct {
	Count  int `json:"count"`
	Min    int `json:"min"`
	Median int `json:"median"`
	P95    int `json:"p95"`
	Max    int `json:"max"`
}
type packageNode struct {
	ImportPath                    string   `json:"import_path"`
	PackageName                   string   `json:"package_name"`
	DirectInternal                []string `json:"direct_internal_dependencies"`
	DirectExternal                []string `json:"direct_external_dependencies"`
	TestOnlyImports               []string `json:"test_only_imports"`
	DirectFanIn                   int      `json:"direct_fan_in"`
	DirectFanOut                  int      `json:"direct_fan_out"`
	TransitiveOnlyDependencyCount int      `json:"transitive_only_dependency_count"`
}
type architectureSnapshot struct {
	Status                           string              `json:"status"`
	Snapshot                         string              `json:"snapshot"`
	ModulePath                       string              `json:"module_path"`
	PackageCount                     int                 `json:"package_count"`
	InternalEdgeCount                int                 `json:"direct_internal_edge_count"`
	ExternalEdgeCount                int                 `json:"direct_external_edge_count"`
	TransitiveOnlyDependencyCount    int                 `json:"transitive_only_package_dependency_count"`
	FanInDistribution                integerDistribution `json:"direct_fan_in_distribution"`
	FanOutDistribution               integerDistribution `json:"direct_fan_out_distribution"`
	InternalDependencyDistribution   integerDistribution `json:"direct_internal_dependency_distribution"`
	ExternalDependencyDistribution   integerDistribution `json:"direct_external_dependency_distribution"`
	TransitiveDependencyDistribution integerDistribution `json:"transitive_dependency_distribution"`
	Cycles                           [][]string          `json:"cyclic_package_components"`
	Packages                         []packageNode       `json:"packages"`
	PackagesTruncated                bool                `json:"packages_truncated"`
	ParseFailures                    []string            `json:"parse_failures,omitempty"`
	ParseFailuresTruncated           bool                `json:"parse_failures_truncated"`
	UnsupportedLanguages             map[string]int      `json:"unsupported_language_files,omitempty"`
	UnresolvedInternalImports        []string            `json:"unresolved_internal_imports,omitempty"`
	Limitations                      []string            `json:"limitations"`
}
type architectureChange struct {
	Kind       string `json:"kind"`
	Package    string `json:"package"`
	Dependency string `json:"dependency,omitempty"`
}
type architectureComparison struct {
	From                 string               `json:"from"`
	To                   string               `json:"to"`
	PackagesAdded        int                  `json:"packages_added"`
	PackagesRemoved      int                  `json:"packages_removed"`
	DirectEdgesAdded     int                  `json:"direct_internal_edges_added"`
	DirectEdgesRemoved   int                  `json:"direct_internal_edges_removed"`
	ExternalEdgesAdded   int                  `json:"direct_external_edges_added"`
	ExternalEdgesRemoved int                  `json:"direct_external_edges_removed"`
	Details              []architectureChange `json:"details"`
	DetailsTruncated     bool                 `json:"details_truncated"`
}
type architectureEvidence struct {
	Status      string                   `json:"status"`
	Method      string                   `json:"method"`
	Snapshots   []architectureSnapshot   `json:"snapshots"`
	Comparisons []architectureComparison `json:"comparisons"`
	Limits      string                   `json:"limits"`
	Limitations []string                 `json:"limitations"`
}
type archPackage struct {
	name     string
	prod     map[string]bool
	external map[string]bool
	tests    map[string]bool
}
type archState struct {
	snapshot architectureSnapshot
	packages map[string]archPackage
	edges    map[string]map[string]bool
}
type parsedGoFile struct {
	path string
	file *ast.File
}

func buildArchitectureEvidence(root string, inputs []snapshotInput) architectureEvidence {
	e := architectureEvidence{Status: "measured", Method: "Go AST import graph; module path from tracked go.mod; production imports form direct graph; test-file imports reported separately; transitive-only counts are reachable internal packages excluding direct imports", Limits: fmt.Sprintf("%d Go files/snapshot, %d bytes/file, %d total bytes, %d import specs; package details capped at %d, parse failure paths at 50.", complexityFileLimit, complexityFileBytes, complexityTotalBytes, architectureImportLimit, architectureDetailLimit), Snapshots: []architectureSnapshot{}, Comparisons: []architectureComparison{}, Limitations: []string{"Internal/external means module-prefix classification, not runtime coupling", "Build tags are not evaluated; any build-tagged Go file makes graph partial", "replace directives, unresolved module/package paths, or invalid source make graph partial", "Test-only imports are kept separate and excluded from production fan-in/out and transitive graph", "No go list, module resolution, download, or source execution"}}
	states := []archState{}
	if len(inputs) == 0 {
		e.Status = "unavailable"
		e.Limitations = append(e.Limitations, "Repository has no HEAD snapshot")
	}
	for _, in := range inputs {
		s := loadArchitectureSnapshot(root, in)
		e.Snapshots = append(e.Snapshots, s.snapshot)
		states = append(states, s)
		if s.snapshot.Status != "measured" {
			e.Status = "partial"
		}
	}
	if len(states) == 3 {
		e.Comparisons = append(e.Comparisons, compareArchitecture("baseline", "HEAD", states[0], states[1]), compareArchitecture("HEAD", "current_tracked_worktree", states[1], states[2]))
	} else if len(states) == 2 {
		e.Comparisons = append(e.Comparisons, compareArchitecture("HEAD", "current_tracked_worktree", states[0], states[1]))
	}
	return e
}
func loadArchitectureSnapshot(root string, in snapshotInput) archState {
	st := archState{packages: map[string]archPackage{}, edges: map[string]map[string]bool{}}
	s := architectureSnapshot{Status: "measured", Snapshot: in.label, Packages: []packageNode{}, Cycles: [][]string{}, UnsupportedLanguages: map[string]int{}, Limitations: []string{"Direct and transitive dependencies are static import relationships; runtime coupling is not measured"}}
	st.snapshot = s
	paths, hashes, modes, err := snapshotPaths(root, in)
	if err != nil {
		st.snapshot.Status = "failed"
		return st
	}
	moduleBytes, ok, skip := readPath(root, in, "go.mod", hashes, modes, 0)
	if skip {
		st.snapshot.Status = "partial"
	}
	if ok {
		mod, replace, valid := parseModulePath(string(moduleBytes))
		st.snapshot.ModulePath = mod
		if !valid || replace {
			st.snapshot.Status = "partial"
			st.snapshot.Limitations = append(st.snapshot.Limitations, "go.mod module path is missing/invalid or replace directives make path mapping uncertain")
		}
	} else {
		st.snapshot.Status = "partial"
		st.snapshot.Limitations = append(st.snapshot.Limitations, "tracked root go.mod unavailable; internal import classification is incomplete")
	}
	mod := st.snapshot.ModulePath
	total, files := 0, 0
	parsed := map[string][]parsedGoFile{}
	pkgNames := map[string]string{}
	for _, p := range paths {
		if source(p) && filepath.Ext(p) != ".go" {
			st.snapshot.UnsupportedLanguages[strings.ToLower(filepath.Ext(p))]++
		}
		if !strings.HasSuffix(p, ".go") {
			if p != "go.mod" && filepath.Base(p) == "go.mod" {
				st.snapshot.Status = "partial"
			}
			continue
		}
		if files >= complexityFileLimit {
			st.snapshot.Status = "partial"
			continue
		}
		files++
		b, readOK, wasSkipped := readPath(root, in, p, hashes, modes, total)
		if wasSkipped {
			st.snapshot.Status = "partial"
			continue
		}
		if !readOK {
			continue
		}
		total += len(b)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, b, parser.ParseComments|parser.AllErrors)
		if perr != nil {
			st.snapshot.Status = "partial"
			st.snapshot.ParseFailures, st.snapshot.ParseFailuresTruncated = appendBounded(st.snapshot.ParseFailures, p, 50)
			continue
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.HasPrefix(strings.TrimSpace(c.Text), "//go:build ") || strings.HasPrefix(strings.TrimSpace(c.Text), "// +build ") {
					st.snapshot.Status = "partial"
				}
			}
		}
		dir := filepath.Dir(p)
		ip := moduleImportPath(mod, dir)
		if ip == "" {
			st.snapshot.Status = "partial"
			continue
		}
		parsed[ip] = append(parsed[ip], parsedGoFile{p, f})
		if !strings.HasSuffix(p, "_test.go") || pkgNames[ip] == "" {
			pkgNames[ip] = f.Name.Name
		}
	}
	importCount := 0
	importsTruncated := false
	parsedKeys := make([]string, 0, len(parsed))
	for ip := range parsed {
		parsedKeys = append(parsedKeys, ip)
	}
	sort.Strings(parsedKeys)
	for _, ip := range parsedKeys {
		files := parsed[ip]
		st.packages[ip] = archPackage{name: pkgNames[ip], prod: map[string]bool{}, external: map[string]bool{}, tests: map[string]bool{}}
		for _, item := range files {
			f := item.file
			testFile := strings.HasSuffix(item.path, "_test.go")
			for _, imp := range f.Imports {
				if importCount >= architectureImportLimit {
					st.snapshot.Status = "partial"
					importsTruncated = true
					continue
				}
				importCount++
				path, e := strconv.Unquote(imp.Path.Value)
				if e != nil {
					st.snapshot.Status = "partial"
					continue
				}
				if testFile {
					st.packages[ip].tests[path] = true
				} else if mod != "" && (path == mod || strings.HasPrefix(path, strings.TrimSuffix(mod, "/")+"/")) {
					st.packages[ip].prod[path] = true
				} else {
					st.packages[ip].external[path] = true
				}
			}
		}
	}
	for ip, p := range st.packages {
		edges := map[string]bool{}
		for dep := range p.prod {
			if _, exists := st.packages[dep]; exists {
				edges[dep] = true
			} else {
				st.snapshot.Status = "partial"
				st.snapshot.UnresolvedInternalImports = append(st.snapshot.UnresolvedInternalImports, ip+" -> "+dep)
			}
		}
		st.edges[ip] = edges
	}
	st.snapshot.PackageCount = len(st.packages)
	fanin := map[string]int{}
	fanout := map[string]int{}
	internalDist, externalDist, transitiveDist := map[string]int{}, map[string]int{}, map[string]int{}
	transitiveTotal := 0
	keys := sortedPackageKeys(st.packages)
	for _, ip := range keys {
		p := st.packages[ip]
		outs := sortedSet(st.edges[ip])
		ins := []string{}
		for from, to := range st.edges {
			if to[ip] {
				ins = append(ins, from)
			}
		}
		sort.Strings(ins)
		reach := reachable(st.edges, ip)
		transitiveOnly := 0
		for dependency := range reach {
			if !st.edges[ip][dependency] {
				transitiveOnly++
			}
		}
		transitiveTotal += transitiveOnly
		internalDist[ip], externalDist[ip], transitiveDist[ip] = len(outs), len(p.external), transitiveOnly
		fanout[ip] = len(outs)
		fanin[ip] = len(ins)
		st.snapshot.InternalEdgeCount += len(outs)
		st.snapshot.ExternalEdgeCount += len(p.external)
		testImports := sortedSet(p.tests)
		node := packageNode{ImportPath: ip, PackageName: p.name, DirectInternal: outs, DirectExternal: sortedSet(p.external), TestOnlyImports: testImports, DirectFanIn: len(ins), DirectFanOut: len(outs), TransitiveOnlyDependencyCount: transitiveOnly}
		if len(st.snapshot.Packages) < architectureDetailLimit {
			st.snapshot.Packages = append(st.snapshot.Packages, node)
		} else {
			st.snapshot.PackagesTruncated = true
		}
	}
	st.snapshot.TransitiveOnlyDependencyCount = transitiveTotal
	if importsTruncated {
		st.snapshot.Limitations = append(st.snapshot.Limitations, "Go import spec analysis cap reached; remaining imports were not included")
	}
	st.snapshot.FanInDistribution = makeIntegerDistribution(fanin)
	st.snapshot.FanOutDistribution = makeIntegerDistribution(fanout)
	st.snapshot.InternalDependencyDistribution = makeIntegerDistribution(internalDist)
	st.snapshot.ExternalDependencyDistribution = makeIntegerDistribution(externalDist)
	st.snapshot.TransitiveDependencyDistribution = makeIntegerDistribution(transitiveDist)
	st.snapshot.Cycles = strongComponents(st.edges)
	sort.Strings(st.snapshot.UnresolvedInternalImports)
	return st
}
func readPath(root string, in snapshotInput, path string, hashes, modes map[string]string, total int) ([]byte, bool, bool) {
	return readSnapshotFile(root, in, path, hashes[path], modes[path], total)
}
func parseModulePath(src string) (string, bool, bool) {
	mod := ""
	replace := false
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "module" && len(fields) >= 2 {
			mod = strings.Trim(fields[1], "\"`")
		}
		if fields[0] == "replace" {
			replace = true
		}
	}
	return mod, replace, mod != ""
}
func moduleImportPath(module, dir string) string {
	if module == "" {
		return ""
	}
	if dir == "." {
		return module
	}
	return strings.TrimSuffix(module, "/") + "/" + filepath.ToSlash(dir)
}
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func sortedPackageKeys(m map[string]archPackage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func reachable(edges map[string]map[string]bool, start string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{}
	for d := range edges[start] {
		stack = append(stack, d)
	}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x == start || seen[x] {
			continue
		}
		seen[x] = true
		for d := range edges[x] {
			if !seen[d] && d != start {
				stack = append(stack, d)
			}
		}
	}
	return seen
}
func makeIntegerDistribution(values map[string]int) integerDistribution {
	v := make([]int, 0, len(values))
	for _, n := range values {
		v = append(v, n)
	}
	sort.Ints(v)
	d := integerDistribution{Count: len(v)}
	if len(v) > 0 {
		d.Min = v[0]
		d.Max = v[len(v)-1]
		d.Median = v[(len(v)-1)/2]
		d.P95 = v[(95*len(v)+99)/100-1]
	}
	return d
}
func strongComponents(edges map[string]map[string]bool) [][]string {
	index := 0
	indices := map[string]int{}
	low := map[string]int{}
	on := map[string]bool{}
	stack := []string{}
	cycles := [][]string{}
	var visit func(string)
	visit = func(v string) {
		index++
		indices[v] = index
		low[v] = index
		stack = append(stack, v)
		on[v] = true
		for _, w := range sortedSet(edges[v]) {
			if indices[w] == 0 {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if on[w] && indices[w] < low[v] {
				low[v] = indices[w]
			}
		}
		if low[v] == indices[v] {
			component := []string{}
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}
			if len(component) > 1 || (len(component) == 1 && edges[v][v]) {
				sort.Strings(component)
				cycles = append(cycles, component)
			}
		}
	}
	for _, v := range sortedPackageKeysFromEdges(edges) {
		if indices[v] == 0 {
			visit(v)
		}
	}
	sort.Slice(cycles, func(i, j int) bool { return strings.Join(cycles[i], "\x00") < strings.Join(cycles[j], "\x00") })
	return cycles
}
func sortedPackageKeysFromEdges(m map[string]map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func compareArchitecture(from, to string, a, b archState) architectureComparison {
	c := architectureComparison{From: from, To: to, Details: []architectureChange{}}
	keys := map[string]bool{}
	for k := range a.packages {
		keys[k] = true
	}
	for k := range b.packages {
		keys[k] = true
	}
	ordered := []string{}
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, p := range ordered {
		_, aok := a.packages[p]
		_, bok := b.packages[p]
		switch {
		case !aok:
			c.PackagesAdded++
			c.add("package_added", p, "")
		case !bok:
			c.PackagesRemoved++
			c.add("package_removed", p, "")
		}
		ad, bd := a.edges[p], b.edges[p]
		deps := map[string]bool{}
		for d := range ad {
			deps[d] = true
		}
		for d := range bd {
			deps[d] = true
		}
		for _, d := range sortedSet(deps) {
			if ad[d] == bd[d] {
				continue
			}
			if bd[d] {
				c.DirectEdgesAdded++
				c.add("direct_internal_import_added", p, d)
			} else {
				c.DirectEdgesRemoved++
				c.add("direct_internal_import_removed", p, d)
			}
		}
		extA, extB := a.packages[p].external, b.packages[p].external
		exts := map[string]bool{}
		for d := range extA {
			exts[d] = true
		}
		for d := range extB {
			exts[d] = true
		}
		for _, d := range sortedSet(exts) {
			if extA[d] == extB[d] {
				continue
			}
			if extB[d] {
				c.ExternalEdgesAdded++
				c.add("direct_external_import_added", p, d)
			} else {
				c.ExternalEdgesRemoved++
				c.add("direct_external_import_removed", p, d)
			}
		}
	}
	return c
}
func (c *architectureComparison) add(kind, p, d string) {
	if len(c.Details) < architectureDetailLimit {
		c.Details = append(c.Details, architectureChange{kind, p, d})
	} else {
		c.DetailsTruncated = true
	}
}

// Security evidence is offline source/config inventory, not vulnerability or reachability analysis.
type dependencyDecl struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}
type securityIndicator struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Evidence string `json:"evidence"`
}
type scannerStatus struct {
	Status    string `json:"status"`
	RunStatus string `json:"run_status"`
	Reason    string `json:"reason"`
}
type securitySnapshot struct {
	Status                 string              `json:"status"`
	Snapshot               string              `json:"snapshot"`
	Dependencies           []dependencyDecl    `json:"dependencies"`
	IndicatorCounts        map[string]int      `json:"indicator_counts"`
	Indicators             []securityIndicator `json:"indicators"`
	IndicatorsTruncated    bool                `json:"indicators_truncated"`
	ParseFailures          []string            `json:"parse_failures,omitempty"`
	ParseFailuresTruncated bool                `json:"parse_failures_truncated"`
	UnsupportedLanguages   map[string]int      `json:"unsupported_language_files,omitempty"`
}
type dependencyChange struct {
	Module string `json:"module"`
	Change string `json:"change"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}
type securityComparison struct {
	From                 string             `json:"from"`
	To                   string             `json:"to"`
	DependenciesAdded    int                `json:"dependencies_added"`
	DependenciesRemoved  int                `json:"dependencies_removed"`
	DependenciesModified int                `json:"dependencies_modified"`
	Details              []dependencyChange `json:"dependency_changes"`
	DetailsTruncated     bool               `json:"details_truncated"`
}
type securityEvidence struct {
	Status               string               `json:"status"`
	Method               string               `json:"method"`
	Snapshots            []securitySnapshot   `json:"snapshots"`
	Comparisons          []securityComparison `json:"comparisons"`
	VulnerabilityScanner scannerStatus        `json:"vulnerability_scanner"`
	Limits               string               `json:"limits"`
	Limitations          []string             `json:"limitations"`
}
type securityState struct {
	snap securitySnapshot
	deps map[string]dependencyDecl
}

func buildSecurityEvidence(root string, inputs []snapshotInput) securityEvidence {
	e := securityEvidence{Status: "measured", Method: "Offline Go AST import/call/identifier indicators plus tracked go.mod require declarations; source locations are syntactic indicators only", VulnerabilityScanner: scannerStatus{"unavailable", "not_run", "No advisory database or vulnerability scanner is integrated; no network access is used"}, Limits: fmt.Sprintf("%d Go files/snapshot, %d bytes/file, %d total bytes; at most %d indicator locations and 50 parse failure paths/snapshot.", complexityFileLimit, complexityFileBytes, complexityTotalBytes, securityIndicatorLimit), Snapshots: []securitySnapshot{}, Comparisons: []securityComparison{}, Limitations: []string{"Indicators do not assert vulnerabilities, exploitability, reachability, or runtime behavior", "Auth/authz identifier matches are lexical and may be false positives", "Build tags are not resolved; incomplete/invalid source marks evidence partial", "Dependency data is direct go.mod require declarations only; no transitive resolution"}}
	states := []securityState{}
	if len(inputs) == 0 {
		e.Status = "unavailable"
		e.Limitations = append(e.Limitations, "Repository has no HEAD snapshot")
	}
	for _, in := range inputs {
		s := loadSecuritySnapshot(root, in)
		states = append(states, s)
		e.Snapshots = append(e.Snapshots, s.snap)
		if s.snap.Status != "measured" {
			e.Status = "partial"
		}
	}
	if len(states) == 3 {
		e.Comparisons = append(e.Comparisons, compareSecurity("baseline", "HEAD", states[0], states[1]), compareSecurity("HEAD", "current_tracked_worktree", states[1], states[2]))
	} else if len(states) == 2 {
		e.Comparisons = append(e.Comparisons, compareSecurity("HEAD", "current_tracked_worktree", states[0], states[1]))
	}
	return e
}
func loadSecuritySnapshot(root string, in snapshotInput) securityState {
	s := securitySnapshot{Status: "measured", Snapshot: in.label, Dependencies: []dependencyDecl{}, IndicatorCounts: map[string]int{}, Indicators: []securityIndicator{}, UnsupportedLanguages: map[string]int{}}
	st := securityState{snap: s, deps: map[string]dependencyDecl{}}
	paths, hashes, modes, err := snapshotPaths(root, in)
	if err != nil {
		st.snap.Status = "failed"
		return st
	}
	if b, ok, skip := readPath(root, in, "go.mod", hashes, modes, 0); skip {
		st.snap.Status = "partial"
	} else if ok {
		deps, valid := parseRequirements(string(b))
		st.deps = deps
		for _, d := range deps {
			st.snap.Dependencies = append(st.snap.Dependencies, d)
		}
		sort.Slice(st.snap.Dependencies, func(i, j int) bool { return st.snap.Dependencies[i].Module < st.snap.Dependencies[j].Module })
		if !valid {
			st.snap.Status = "partial"
		}
	} else {
		st.snap.Status = "partial"
	}
	total, files := 0, 0
	for _, p := range paths {
		if source(p) && filepath.Ext(p) != ".go" {
			st.snap.UnsupportedLanguages[strings.ToLower(filepath.Ext(p))]++
		}
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		if files >= complexityFileLimit {
			st.snap.Status = "partial"
			continue
		}
		files++
		b, ok, skip := readPath(root, in, p, hashes, modes, total)
		if skip {
			st.snap.Status = "partial"
			continue
		}
		if !ok {
			continue
		}
		total += len(b)
		fset := token.NewFileSet()
		f, pe := parser.ParseFile(fset, p, b, parser.ParseComments|parser.AllErrors)
		if pe != nil {
			st.snap.Status = "partial"
			st.snap.ParseFailures, st.snap.ParseFailuresTruncated = appendBounded(st.snap.ParseFailures, p, 50)
			continue
		}
		aliases := map[string]string{}
		for _, spec := range f.Imports {
			ip, e := strconv.Unquote(spec.Path.Value)
			if e != nil {
				continue
			}
			name := filepath.Base(ip)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			aliases[name] = ip
			kind := securityImportKind(ip)
			if kind != "" {
				st.add(kind, p, fset.Position(spec.Pos()).Line, ip)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if isHTTPHandlerSignature(x.Type, aliases) {
					st.add("http_handler_signature", p, fset.Position(x.Pos()).Line, x.Name.Name)
				}
			case *ast.FuncLit:
				if isHTTPHandlerSignature(x.Type, aliases) {
					st.add("http_handler_signature", p, fset.Position(x.Pos()).Line, "function literal")
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					line := fset.Position(x.Pos()).Line
					pkg := selectorPackage(sel.X)
					path := aliases[pkg]
					if path == "net/http" {
						switch sel.Sel.Name {
						case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS":
							st.add("http_server_api", p, line, sel.Sel.Name)
						case "Handle", "HandleFunc":
							st.add("http_handler_registration", p, line, sel.Sel.Name)
						}
					}
					if path == "os/exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
						st.add("os_exec_call", p, line, sel.Sel.Name)
					}
					if path == "syscall" && (sel.Sel.Name == "Setuid" || sel.Sel.Name == "Setgid" || sel.Sel.Name == "Exec") {
						st.add("privilege_api", p, line, "syscall."+sel.Sel.Name)
					}
					if path == "os" && (sel.Sel.Name == "Chmod" || sel.Sel.Name == "Chown") {
						st.add("privilege_api", p, line, "os."+sel.Sel.Name)
					}
					if sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc" {
						st.add("http_handler_registration_heuristic", p, line, sel.Sel.Name)
					}
				}
			case *ast.SelectorExpr:
				pkg := selectorPackage(x.X)
				if aliases[pkg] == "net/http" && (x.Sel.Name == "Server" || x.Sel.Name == "ServeMux") {
					st.add("http_server_type", p, fset.Position(x.Pos()).Line, x.Sel.Name)
				}
			case *ast.Ident:
				kind := authIdentifierKind(x.Name)
				if kind != "" {
					st.add(kind, p, fset.Position(x.Pos()).Line, x.Name)
				}
			}
			return true
		})
	}
	return st
}
func (st *securityState) add(kind, path string, line int, evidence string) {
	st.snap.IndicatorCounts[kind]++
	if len(st.snap.Indicators) < securityIndicatorLimit {
		st.snap.Indicators = append(st.snap.Indicators, securityIndicator{kind, path, line, evidence})
	} else {
		st.snap.IndicatorsTruncated = true
	}
}
func securityImportKind(p string) string {
	switch {
	case p == "crypto/md5" || p == "crypto/sha1" || p == "crypto/des" || p == "crypto/rc4":
		return "weak_crypto_import"
	case strings.HasPrefix(p, "crypto/"):
		return "crypto_import"
	case p == "net/http":
		return "net_http_import"
	case p == "os/exec":
		return "os_exec_import"
	case p == "syscall":
		return "syscall_import"
	case p == "unsafe":
		return "unsafe_import"
	}
	return ""
}
func selectorPackage(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}
func isHTTPHandlerSignature(ft *ast.FuncType, aliases map[string]string) bool {
	if ft == nil || ft.Params == nil {
		return false
	}
	writer, request := false, false
	for _, field := range ft.Params.List {
		typ := field.Type
		pointer := false
		if star, ok := typ.(*ast.StarExpr); ok {
			pointer = true
			typ = star.X
		}
		sel, ok := typ.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || aliases[id.Name] != "net/http" {
			continue
		}
		if sel.Sel.Name == "ResponseWriter" && !pointer {
			writer = true
		}
		if sel.Sel.Name == "Request" && pointer {
			request = true
		}
	}
	return writer && request
}
func authIdentifierKind(s string) string {
	v := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "_", ""), "-", ""))
	for _, needle := range []string{"authorization", "authenticate", "authorize", "authn", "authz", "credential", "password", "passwd", "token", "permission", "privilege", "role", "accesscontrol", "acl"} {
		if strings.Contains(v, needle) {
			return "auth_identifier"
		}
	}
	return ""
}
func parseRequirements(src string) (map[string]dependencyDecl, bool) {
	out := map[string]dependencyDecl{}
	block := false
	valid := true
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		comment := ""
		if i := strings.Index(line, "//"); i >= 0 {
			comment = strings.TrimSpace(line[i+2:])
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if block {
			if line == ")" {
				block = false
				continue
			}
			addRequirement(out, line, comment, &valid)
			continue
		}
		if !strings.HasPrefix(line, "require ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "require "))
		if rest == "(" {
			block = true
			continue
		}
		addRequirement(out, rest, comment, &valid)
	}
	if block {
		valid = false
	}
	return out, valid
}

func addRequirement(out map[string]dependencyDecl, line, comment string, valid *bool) {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		*valid = false
		return
	}
	module, version := fields[0], fields[1]
	for i, value := range []string{module, version} {
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "`") {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				*valid = false
				return
			}
			if i == 0 {
				module = unquoted
			} else {
				version = unquoted
			}
		}
	}
	if module == "" || version == "" {
		*valid = false
		return
	}
	out[module] = dependencyDecl{Module: module, Version: version, Indirect: comment == "indirect"}
}
func compareSecurity(from, to string, a, b securityState) securityComparison {
	c := securityComparison{From: from, To: to, Details: []dependencyChange{}}
	keys := map[string]bool{}
	for k := range a.deps {
		keys[k] = true
	}
	for k := range b.deps {
		keys[k] = true
	}
	ordered := []string{}
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		x, xok := a.deps[k]
		y, yok := b.deps[k]
		change := ""
		switch {
		case !xok:
			c.DependenciesAdded++
			change = "added"
		case !yok:
			c.DependenciesRemoved++
			change = "removed"
		case x.Version != y.Version || x.Indirect != y.Indirect:
			c.DependenciesModified++
			change = "modified"
		default:
			continue
		}
		if len(c.Details) < architectureDetailLimit {
			c.Details = append(c.Details, dependencyChange{k, change, x.Version, y.Version})
		} else {
			c.DetailsTruncated = true
		}
	}
	return c
}
func compareArchitectureStatus(a, b string) bool { return a == "measured" && b == "measured" }
