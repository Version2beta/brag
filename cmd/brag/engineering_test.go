package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFunctionMetricsNestedAndLogicalBranches(t *testing.T) {
	src := `package p
func F(a, b bool) {
 if a && b {
  for a { if b || a {} }
 }
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	m := functionMetrics(fn, fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line)
	if m.Cyclomatic != 6 || m.Cognitive != 6 || m.MaxNesting != 3 || m.Lines != 5 {
		t.Fatalf("metrics=%+v", m)
	}
}

func TestPromisorBlobReadDoesNotFetch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "init", "-q", "-b", "main")
	gitCmd(t, repo, "config", "user.name", "Fixture")
	gitCmd(t, repo, "config", "user.email", "fixture@example.test")
	gitCmd(t, repo, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(repo, "x.go"), []byte("package p\nfunc F() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "x.go")
	gitCmd(t, repo, "commit", "-qm", "source")
	head := gitCmd(t, repo, "rev-parse", "HEAD")
	blob := gitCmd(t, repo, "rev-parse", "HEAD:x.go")
	gitCmd(t, root, "init", "--bare", "-q", remote)
	gitCmd(t, repo, "push", remote, "main")
	gitCmd(t, repo, "config", "core.repositoryFormatVersion", "1")
	gitCmd(t, repo, "config", "extensions.partialClone", "origin")
	gitCmd(t, repo, "config", "remote.origin.url", remote)
	gitCmd(t, repo, "config", "remote.origin.promisor", "true")
	gitCmd(t, repo, "config", "remote.origin.partialclonefilter", "blob:none")
	objectPath := filepath.Join(repo, ".git", "objects", blob[:2], blob[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove fixture blob: %v", err)
	}

	_, snapshot := loadComplexitySnapshot(repo, snapshotInput{label: "HEAD", rev: head})
	if snapshot.Status != "partial" || snapshot.SkippedFiles != 1 {
		t.Fatalf("missing promised blob should be partial, got %+v", snapshot)
	}
	if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
		t.Fatalf("promised blob was fetched into the local object store: stat err=%v", err)
	}
	if _, err := runGit(repo, "cat-file", "-e", blob); err == nil {
		t.Fatal("missing promised blob unexpectedly resolved")
	}
}

func TestGitSnapshotComplexityComparisonsAndDeterminism(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Fixture")
	gitCmd(t, d, "config", "user.email", "fixture@example.test")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, "x.go"), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("package p\nfunc Keep() { if true {} }\nfunc Remove() {}\n")
	gitCmd(t, d, "add", "x.go")
	gitCmd(t, d, "commit", "-qm", "base")
	base := gitCmd(t, d, "rev-parse", "HEAD")
	write("package p\nfunc Keep() { if true { if false {} } }\nfunc Added() {}\n")
	gitCmd(t, d, "add", "x.go")
	gitCmd(t, d, "commit", "-qm", "head")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	write("package p\nfunc Keep() { if true { if false { for {} } } }\nfunc Added() {}\nfunc Overlay() {}\n")
	bi := &baseInfo{MergeBase: base}
	a := buildEngineeringEvidence("branch", head, bi, d, nil, identityInfo{}, maps{}).Complexity
	b := buildEngineeringEvidence("branch", head, bi, d, nil, identityInfo{}, maps{}).Complexity
	if a.Status != "measured" || len(a.Snapshots) != 3 || len(a.Comparisons) != 2 {
		t.Fatalf("evidence=%+v", a)
	}
	if a.Snapshots[0].Functions != 2 || a.Snapshots[1].Functions != 2 || a.Snapshots[2].Functions != 3 {
		t.Fatalf("snapshots=%+v", a.Snapshots)
	}
	c := a.Comparisons[0]
	if c.Added != 1 || c.Removed != 1 || c.Modified != 1 || c.MatchedComplexityChanged != 1 {
		t.Fatalf("comparison=%+v", c)
	}
	if a.Comparisons[1].Added != 1 || a.Comparisons[1].Modified != 1 {
		t.Fatalf("overlay=%+v", a.Comparisons[1])
	}
	if !reflect.DeepEqual(a, b) || a.Comparisons[0].Ambiguity == "" {
		t.Fatal("unstable output or missing identity caveat")
	}
	complexityJSON, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var complexityObject map[string]json.RawMessage
	if err := json.Unmarshal(complexityJSON, &complexityObject); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"person", "developer", "selected_identity", "contributor_metrics"} {
		if _, exists := complexityObject[key]; exists {
			t.Fatalf("repo-wide complexity contains attribution field %q", key)
		}
	}
	all := buildEngineeringEvidence("all", head, nil, d, nil, identityInfo{}, maps{}).Complexity
	if len(all.Snapshots) != 2 || len(all.Comparisons) != 1 {
		t.Fatalf("all snapshots=%+v", all)
	}
	os.Remove(filepath.Join(d, "x.go"))
	deleted := buildEngineeringEvidence("branch", head, bi, d, nil, identityInfo{}, maps{}).Complexity
	if deleted.Snapshots[2].Status != "measured" || deleted.Comparisons[1].Removed != 2 {
		t.Fatalf("tracked deletion should be absence: %+v", deleted)
	}
}

func TestLogicalBranchesAffectCyclomaticNotCognitive(t *testing.T) {
	src := `package p
func F(a, b bool) { if a && b {} }
func G(a, b bool) { if a || b {} }
`
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn := decl.(*ast.FuncDecl)
		m := functionMetrics(fn, fs.Position(fn.Pos()).Line, fs.Position(fn.End()).Line)
		if m.Cyclomatic != 3 || m.Cognitive != 1 {
			t.Errorf("%s metrics=%+v; want cyclomatic 3, cognitive 1", fn.Name.Name, m)
		}
	}
}

func TestComplexityCaseBranchesAndFunctionLiteralBoundary(t *testing.T) {
	src := `package p
func F(x int) { switch x { case 1: if x > 0 {} ; case 2: ; default: } }
func G() { func() { if true {} }() }
`
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fns := []*ast.FuncDecl{f.Decls[0].(*ast.FuncDecl), f.Decls[1].(*ast.FuncDecl)}
	m := functionMetrics(fns[0], fs.Position(fns[0].Pos()).Line, fs.Position(fns[0].End()).Line)
	if m.Cyclomatic != 6 || m.Cognitive != 3 || m.MaxNesting != 2 {
		t.Fatalf("case nesting metrics=%+v", m)
	}
	m = functionMetrics(fns[1], fs.Position(fns[1].Pos()).Line, fs.Position(fns[1].End()).Line)
	if m.Cyclomatic != 1 || m.Cognitive != 0 || m.MaxNesting != 0 {
		t.Fatalf("function literal leaked into parent: %+v", m)
	}
}

func TestGoTestInventoryConventionsAndComparisons(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Fixture")
	gitCmd(t, d, "config", "user.email", "fixture@example.test")
	write := func(path, src string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(d, path), []byte(src), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("prod.go", "package p\nfunc Production() {}\n")
	write("p_test.go", `package p
import "testing"
func TestOld(t *testing.T) {}
func Testlower(t *testing.T) {}
func TestBad(t *testing.T, x int) {}
func TestResult(t *testing.T) bool { return true }
func TestMain(m *testing.M) {}
func BenchmarkWork(b *testing.B) {}
func Example_suffix() {}
func ExampleFoo_suffix() {}
func Examplebad() {}
func (x *thing) TestMethod(t *testing.T) {}
type thing struct{}
func helper() {}
`)
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "base")
	base := gitCmd(t, d, "rev-parse", "HEAD")
	write("p_test.go", `package p
import "testing"
func TestOld(t *testing.T) {}
func TestNew(t *testing.T) { cases := []int{1,2}; for _, tc := range cases { t.Run("case", func(t *testing.T) {}) } }
func TestMain(m *testing.M) {}
func BenchmarkWork(b *testing.B) {}
func Example_suffix() {}
type thing struct{}
func (x *thing) TestMethod(t *testing.T) {}
func helper() {}
`)
	write("alias_test.go", `package p
import tt "testing"
func TestAlias(ttt *tt.T) {}
`)
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "-c", "user.name=Ada", "-c", "user.email=ada-old@example.test", "commit", "-qm", "tests\n\nCo-authored-by: Grace <grace@example.test>")
	first := gitCmd(t, d, "rev-parse", "HEAD")
	write("p_test.go", `package p
import "testing"
func TestOld(t *testing.T) {}
func TestNew(tt *testing.T) { cases := []int{1,2}; for _, tc := range cases { tt.Run("changed", func(t *testing.T) {}) } }
func TestMain(m *testing.M) {}
func BenchmarkWork(b *testing.B) {}
func Example_suffix() {}
type thing struct{}
func (x *thing) TestMethod(t *testing.T) {}
func helper() {}
`)
	gitCmd(t, d, "add", "p_test.go")
	gitCmd(t, d, "-c", "user.name=Modify", "-c", "user.email=modify@example.test", "commit", "-qm", "modify test signature and body")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	write("p_test.go", `package p
import "testing"
func TestOld(t *testing.T) {}
func TestNew(tt *testing.T) { cases := []int{1,2}; for _, tc := range cases { tt.Run("overlay", func(t *testing.T) {}) } }
func TestOverlay(t *testing.T) {}
func TestMain(m *testing.M) {}
func BenchmarkWork(b *testing.B) {}
func Example_suffix() {}
type thing struct{}
func (x *thing) TestMethod(t *testing.T) {}
func helper() {}
`)
	ids := maps{exact: map[string]identityInfo{}, aliases: map[string]string{"ada-old@example.test": "ada@example.test"}}
	ada := identityInfo{Name: "Ada", Email: "ada@example.test", Available: true}
	e := buildEngineeringEvidence("branch", head, &baseInfo{MergeBase: base}, d, []string{first, head}, ada, ids).TestInventory
	if e.Status != "measured" || len(e.Snapshots) != 3 {
		t.Fatalf("inventory=%+v", e)
	}
	baseSnap, headSnap, cur := e.Snapshots[0], e.Snapshots[1], e.Snapshots[2]
	if baseSnap.Tests != 1 || baseSnap.TestMain != 1 || baseSnap.Benchmarks != 1 || baseSnap.Examples != 2 {
		t.Fatalf("base=%+v", baseSnap)
	}
	if headSnap.Tests != 3 || headSnap.SubtestSites != 1 || headSnap.TableDrivenSites != 1 || headSnap.ProductionFunctions != 1 {
		t.Fatalf("head=%+v", headSnap)
	}
	if cur.Tests != 4 || cur.TestToProduction.Numerator != 4 || cur.TestToProduction.Denominator != 1 || cur.TestRatioUnits == "" {
		t.Fatalf("current=%+v", cur)
	}
	if e.Comparisons[0].Added != 2 || e.Comparisons[0].Removed != 1 || e.Comparisons[1].Added != 1 || e.Comparisons[1].Modified != 1 {
		t.Fatalf("comparisons=%+v", e.Comparisons)
	}
	if e.HistoricalAttribution.Status != "measured" || e.HistoricalAttribution.Count != 2 || e.SelectedTestsAddedRatio.Numerator == nil || *e.SelectedTestsAddedRatio.Numerator != 2 || e.SelectedTestsAddedRatio.Denominator != 2 || e.AllTestsAddedRatio.Denominator != 2 {
		t.Fatalf("history=%+v", e)
	}
	grace := buildEngineeringEvidence("branch", head, &baseInfo{MergeBase: base}, d, []string{first, head}, identityInfo{Name: "Grace", Email: "grace@example.test", Available: true}, ids).TestInventory
	if grace.SelectedTestsAddedRatio.Numerator == nil || *grace.SelectedTestsAddedRatio.Numerator != 2 {
		t.Fatalf("coauthor credit=%+v", grace.SelectedTestsAddedRatio)
	}
}

func TestTestDefinitionHashIncludesSignature(t *testing.T) {
	before, err := definitionsForFile("sig_test.go", []byte("package p\nimport \"testing\"\nfunc TestSig(t *testing.T) {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := definitionsForFile("sig_test.go", []byte("package p\nimport \"testing\"\nfunc TestSig(tt *testing.T) {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := compareTests("before", "after", before, after)
	if c.Added != 0 || c.Modified != 1 {
		t.Fatalf("signature change counted incorrectly: %+v", c)
	}
}
