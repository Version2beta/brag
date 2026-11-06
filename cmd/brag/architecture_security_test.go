package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequirementsIgnoresUnrelatedComments(t *testing.T) {
	source := `module example.test/app

go 1.22 // require text in a comment is not a dependency

require (
  github.com/example/direct v1.2.3
  golang.org/x/indirect v0.4.0 // indirect
)
require "example.test/quoted" "v2.0.0"
replace github.com/example/direct => ../direct
`
	deps, valid := parseRequirements(source)
	if !valid || len(deps) != 3 {
		t.Fatalf("requirements=%+v valid=%t", deps, valid)
	}
	if deps["github.com/example/direct"].Version != "v1.2.3" || deps["github.com/example/direct"].Indirect {
		t.Fatalf("direct requirement parsed incorrectly: %+v", deps["github.com/example/direct"])
	}
	if deps["golang.org/x/indirect"].Version != "v0.4.0" || !deps["golang.org/x/indirect"].Indirect {
		t.Fatalf("indirect requirement parsed incorrectly: %+v", deps["golang.org/x/indirect"])
	}
	if deps["example.test/quoted"].Version != "v2.0.0" {
		t.Fatalf("quoted requirement parsed incorrectly: %+v", deps["example.test/quoted"])
	}
}

func TestUnavailableSnapshotsSerializeEmptyArrays(t *testing.T) {
	architecture := buildArchitectureEvidence(t.TempDir(), nil)
	if architecture.Status != "unavailable" || architecture.Snapshots == nil || architecture.Comparisons == nil {
		t.Fatalf("architecture unavailable arrays/status: %+v", architecture)
	}
	security := buildSecurityEvidence(t.TempDir(), nil)
	if security.Status != "unavailable" || security.Snapshots == nil || security.Comparisons == nil {
		t.Fatalf("security unavailable arrays/status: %+v", security)
	}
	comparison := compareFunctions("HEAD", "current", functionMap{}, functionMap{})
	if comparison.Details == nil {
		t.Fatalf("empty complexity comparison details must be an array: %+v", comparison)
	}
}

func TestParseFailurePathCapsAreExplicit(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Parser")
	gitCmd(t, d, "config", "user.email", "parser@example.test")
	if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/parser\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", "go.mod")
	gitCmd(t, d, "commit", "-qm", "base")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	for i := 0; i < 51; i++ {
		path := filepath.Join(d, "broken", fmt.Sprintf("p%02d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package p\nfunc {\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, d, "add", ".")
	in := snapshotInput{label: "current_tracked_worktree", rev: head, overlay: true}
	_, complexity := loadComplexitySnapshot(d, in)
	architecture := loadArchitectureSnapshot(d, in).snapshot
	security := loadSecuritySnapshot(d, in).snap
	test, _ := loadTestSnapshot(d, in)
	documentation, _ := loadDocSnapshot(d, in)
	for name, failures := range map[string]struct {
		failures  int
		truncated bool
	}{
		"complexity":    {len(complexity.ParseFailures), complexity.ParseFailuresTruncated},
		"architecture":  {len(architecture.ParseFailures), architecture.ParseFailuresTruncated},
		"security":      {len(security.ParseFailures), security.ParseFailuresTruncated},
		"tests":         {len(test.ParseFailures), test.ParseFailuresTruncated},
		"documentation": {len(documentation.Counts.ParseFailures), documentation.Counts.ParseFailuresTruncated},
	} {
		if failures.failures != 50 || !failures.truncated {
			t.Errorf("%s parse failures: got %d paths, truncated=%t", name, failures.failures, failures.truncated)
		}
	}
}

func TestArchitectureSnapshotsGraphAndDependencyChanges(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Graph")
	gitCmd(t, d, "config", "user.email", "graph@example.test")
	write := func(path, src string) {
		t.Helper()
		p := filepath.Join(d, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/project\n\ngo 1.22\n")
	write("pkg/a/a.go", "package a\nimport (\"example.test/project/pkg/b\"; \"fmt\")\nfunc A(){ _ = fmt.Sprint(b.B) }\n")
	write("pkg/b/b.go", "package b\nimport \"example.test/project/pkg/a\"\nvar B = a.A\n")
	write("pkg/c/c.go", "package c\nimport \"example.test/project/pkg/b\"\nfunc C(){ b.B() }\n")
	write("pkg/c/c_test.go", "package c\nimport \"testing\"\nfunc TestC(t *testing.T){}\n")
	write("notes.py", "print('unsupported')\n")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "base")
	base := gitCmd(t, d, "rev-parse", "HEAD")
	write("pkg/c/c.go", "package c\nimport \"example.test/project/pkg/a\"\nfunc C(){ a.A() }\n")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "change dependency")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	write("pkg/c/c.go", "package c\nimport (\"example.test/project/pkg/b\"; \"fmt\")\nfunc C(){ fmt.Sprint(b.B) }\n")
	e := buildArchitectureEvidence(d, []snapshotInput{{label: "baseline", rev: base}, {label: "HEAD", rev: head}, {label: "current_tracked_worktree", rev: head, overlay: true}})
	if e.Status != "measured" || len(e.Snapshots) != 3 {
		t.Fatalf("architecture=%+v", e)
	}
	find := func(s architectureSnapshot, path string) packageNode {
		for _, p := range s.Packages {
			if strings.HasSuffix(p.ImportPath, path) {
				return p
			}
		}
		t.Fatalf("package %s missing from %+v", path, s.Packages)
		return packageNode{}
	}
	a, b, c := find(e.Snapshots[0], "/pkg/a"), find(e.Snapshots[0], "/pkg/b"), find(e.Snapshots[0], "/pkg/c")
	if len(a.DirectExternal) != 1 || a.DirectExternal[0] != "fmt" || c.DirectFanOut != 1 || c.TransitiveOnlyDependencyCount != 1 || b.DirectFanIn != 2 {
		t.Fatalf("graph nodes a=%+v b=%+v c=%+v", a, b, c)
	}
	if len(c.TestOnlyImports) != 1 || c.TestOnlyImports[0] != "testing" {
		t.Fatalf("test-only imports mixed/missing: %+v", c)
	}
	if e.Snapshots[0].UnsupportedLanguages[".py"] != 1 {
		t.Fatalf("unsupported source languages not reported: %+v", e.Snapshots[0].UnsupportedLanguages)
	}
	if len(e.Snapshots[0].Cycles) != 1 || len(e.Snapshots[0].Cycles[0]) != 2 {
		t.Fatalf("expected a/b cycle: %+v", e.Snapshots[0].Cycles)
	}
	if e.Comparisons[0].DirectEdgesAdded != 1 || e.Comparisons[0].DirectEdgesRemoved != 1 || e.Comparisons[1].DirectEdgesAdded != 1 || e.Comparisons[1].DirectEdgesRemoved != 1 || e.Comparisons[1].ExternalEdgesAdded != 1 {
		t.Fatalf("dependency changes=%+v", e.Comparisons)
	}
	write("bad.go", "package broken\nfunc {")
	gitCmd(t, d, "add", "bad.go")
	partial := loadArchitectureSnapshot(d, snapshotInput{label: "overlay", rev: head, overlay: true})
	if partial.snapshot.Status != "partial" {
		t.Fatalf("invalid tracked Go file should make graph partial: %+v", partial.snapshot)
	}
}

func TestStaticSecurityIndicatorsAndDependencyVersions(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Security")
	gitCmd(t, d, "config", "user.email", "sec@example.test")
	write := func(path, src string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(d, path), []byte(src), 0600); e != nil {
			t.Fatal(e)
		}
	}
	mod := func(version string) string {
		return "module example.test/sec\n\ngo 1.22\n\nrequire example.test/dep " + version + " // indirect\n"
	}
	write("go.mod", mod("v1.0.0"))
	source := `package sec
import (
 weak "crypto/md5"
 h "net/http"
 ex "os/exec"
 "syscall"
 "unsafe"
 "os"
)
func Handler(w h.ResponseWriter, r *h.Request) {}
func F(){ ex.Command("tool"); h.ListenAndServe(":0",nil); h.HandleFunc("/",Handler); syscall.Setuid(0); os.Chmod("x",0600); _=unsafe.Pointer(nil); _=weak.Size }
var authToken string
`
	write("secure.go", source)
	write("notes.py", "print('unsupported')\n")
	gitCmd(t, d, "add", ".")
	gitCmd(t, d, "commit", "-qm", "baseline")
	base := gitCmd(t, d, "rev-parse", "HEAD")
	write("go.mod", mod("v1.2.0"))
	gitCmd(t, d, "add", "go.mod")
	gitCmd(t, d, "commit", "-qm", "dependency bump")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	write("go.mod", mod("v1.3.0"))
	var extra strings.Builder
	for i := 0; i < 510; i++ {
		fmt.Fprintf(&extra, "var authToken%d int\n", i)
	}
	write("secure.go", source+extra.String())
	e := buildSecurityEvidence(d, []snapshotInput{{label: "baseline", rev: base}, {label: "HEAD", rev: head}, {label: "current_tracked_worktree", rev: head, overlay: true}})
	if e.Status != "measured" || len(e.Snapshots) != 3 {
		t.Fatalf("security evidence=%+v", e)
	}
	if e.Snapshots[0].Dependencies[0].Version != "v1.0.0" || !e.Snapshots[0].Dependencies[0].Indirect || e.Comparisons[0].DependenciesModified != 1 || e.Comparisons[1].DependenciesModified != 1 {
		t.Fatalf("dependency evidence=%+v comparisons=%+v", e.Snapshots, e.Comparisons)
	}
	if e.Snapshots[0].UnsupportedLanguages[".py"] != 1 {
		t.Fatalf("unsupported source languages not reported: %+v", e.Snapshots[0].UnsupportedLanguages)
	}
	counts := e.Snapshots[1].IndicatorCounts
	for _, kind := range []string{"weak_crypto_import", "net_http_import", "os_exec_call", "syscall_import", "unsafe_import", "http_handler_signature", "http_server_api", "http_handler_registration", "privilege_api", "auth_identifier"} {
		if counts[kind] == 0 {
			t.Errorf("missing security indicator %s: %#v", kind, counts)
		}
	}
	if !e.Snapshots[2].IndicatorsTruncated || len(e.Snapshots[2].Indicators) != securityIndicatorLimit {
		t.Fatalf("indicator cap not disclosed: %+v", e.Snapshots[2])
	}
	if e.VulnerabilityScanner.Status != "unavailable" || e.VulnerabilityScanner.RunStatus != "not_run" || !strings.Contains(e.VulnerabilityScanner.Reason, "no network") {
		t.Fatalf("scanner status=%+v", e.VulnerabilityScanner)
	}
	write("invalid.go", "package sec\nfunc {")
	gitCmd(t, d, "add", "invalid.go")
	partial := loadSecuritySnapshot(d, snapshotInput{label: "overlay", rev: head, overlay: true})
	if partial.snap.Status != "partial" || len(partial.snap.ParseFailures) == 0 {
		t.Fatalf("parse failure must be partial: %+v", partial.snap)
	}
}

func TestArchitecturePackageDetailCap(t *testing.T) {
	d := t.TempDir()
	gitCmd(t, d, "init", "-q", "-b", "main")
	gitCmd(t, d, "config", "user.name", "Graph")
	gitCmd(t, d, "config", "user.email", "graph@example.test")
	if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.test/cap\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, d, "add", "go.mod")
	gitCmd(t, d, "commit", "-qm", "base")
	head := gitCmd(t, d, "rev-parse", "HEAD")
	for i := 0; i < architectureDetailLimit+1; i++ {
		path := filepath.Join(d, "pkg", fmt.Sprintf("p%04d", i), "x.go")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package p\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, d, "add", ".")
	s := loadArchitectureSnapshot(d, snapshotInput{label: "overlay", rev: head, overlay: true}).snapshot
	if s.Status != "measured" || s.PackageCount != architectureDetailLimit+1 || len(s.Packages) != architectureDetailLimit || !s.PackagesTruncated {
		t.Fatalf("package details not capped deterministically: status=%s count=%d details=%d truncated=%t", s.Status, s.PackageCount, len(s.Packages), s.PackagesTruncated)
	}
}
