package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const dynamicFilesLimit = 5000
const dynamicFileBytes = 16 << 20
const dynamicSourceBytes = 128 << 20
const dynamicOutputBytes = 1 << 20
const dynamicArtifactBytes = 8 << 20
const dynamicArtifactTotalBytes = 96 << 20

type dynamicEvidence struct {
	Status              string                      `json:"status"`
	Toolchain           string                      `json:"go_version"`
	GOOS                string                      `json:"goos"`
	GOARCH              string                      `json:"goarch"`
	TestConfiguration   map[string]string           `json:"test_configuration"`
	Environment         map[string]string           `json:"environment"`
	TimeoutMillis       int64                       `json:"per_run_timeout_millis"`
	TotalTimeoutMillis  int64                       `json:"total_timeout_millis"`
	ArtifactDirectory   string                      `json:"artifact_directory"`
	Runs                []dynamicRun                `json:"runs"`
	CoverageComparisons []dynamicCoverageComparison `json:"coverage_comparisons"`
	Limitations         []string                    `json:"limitations"`
}
type dynamicRun struct {
	Snapshot           string           `json:"snapshot"`
	RunIndex           int              `json:"run_index"`
	RunOrder           int              `json:"run_order"`
	Identity           string           `json:"snapshot_identity"`
	Status             string           `json:"status"`
	Classification     string           `json:"classification"`
	ElapsedMillis      int64            `json:"elapsed_millis"`
	Tests              []dynamicTest    `json:"-"`
	TestCounts         map[string]int   `json:"test_counts"`
	PackageCount       int              `json:"package_count"`
	PackageCounts      map[string]int   `json:"package_counts"`
	Packages           []dynamicPackage `json:"-"`
	DetailsArtifact    string           `json:"details_artifact,omitempty"`
	Coverage           dynamicCoverage  `json:"coverage"`
	CoverageArtifact   string           `json:"coverage_artifact,omitempty"`
	LogArtifact        string           `json:"log_artifact,omitempty"`
	OutputTruncated    bool             `json:"output_truncated"`
	ArtifactIncomplete bool             `json:"artifact_incomplete"`
	Diagnostic         string           `json:"diagnostic,omitempty"`
	Instability        string           `json:"observed_instability,omitempty"`
}
type dynamicTest struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Outcome string  `json:"outcome"`
	Elapsed float64 `json:"elapsed_seconds,omitempty"`
}
type dynamicPackage struct {
	Package         string   `json:"package"`
	Outcome         string   `json:"outcome"`
	Elapsed         float64  `json:"elapsed_seconds,omitempty"`
	CoveragePercent *float64 `json:"coverage_percent,omitempty"`
	Covered         int      `json:"covered_statements"`
	Total           int      `json:"total_statements"`
	CoverageStatus  string   `json:"coverage_status"`
	UnitIdentities  []string `json:"coverable_unit_identities,omitempty"`
}
type dynamicCoverageComparison struct {
	From                       string                             `json:"from"`
	FromRunOrder               int                                `json:"from_run_order"`
	ToRunOrder                 int                                `json:"to_run_order"`
	To                         string                             `json:"to"`
	Packages                   []dynamicPackageCoverageComparison `json:"-"`
	PackageComparisonsArtifact string                             `json:"package_comparisons_artifact,omitempty"`
	Status                     string                             `json:"status"`
	CompatibleDenominator      bool                               `json:"compatible_denominator"`
	FromCovered                int                                `json:"from_covered_statements"`
	FromTotal                  int                                `json:"from_total_statements"`
	FromPercent                *float64                           `json:"from_percent,omitempty"`
	ToCovered                  int                                `json:"to_covered_statements"`
	ToTotal                    int                                `json:"to_total_statements"`
	ToPercent                  *float64                           `json:"to_percent,omitempty"`
	CoveredDelta               *int                               `json:"covered_delta,omitempty"`
}
type dynamicPackageCoverageComparison struct {
	Package     string   `json:"package"`
	Status      string   `json:"status"`
	FromCovered int      `json:"from_covered_statements"`
	FromTotal   int      `json:"from_total_statements"`
	FromPercent *float64 `json:"from_percent,omitempty"`
	ToCovered   int      `json:"to_covered_statements"`
	ToTotal     int      `json:"to_total_statements"`
	ToPercent   *float64 `json:"to_percent,omitempty"`
}
type dynamicRunDetailsArtifact struct {
	FormatVersion          int              `json:"format_version"`
	Kind                   string           `json:"kind"`
	Snapshot               string           `json:"snapshot"`
	RunIndex               int              `json:"run_index"`
	SnapshotIdentity       string           `json:"snapshot_identity"`
	Tests                  []dynamicTest    `json:"tests"`
	Packages               []dynamicPackage `json:"packages"`
	CoverageUnitIdentities []string         `json:"coverage_unit_identities"`
}
type dynamicPackageComparisonArtifact struct {
	FormatVersion int                                `json:"format_version"`
	Kind          string                             `json:"kind"`
	From          string                             `json:"from"`
	To            string                             `json:"to"`
	Packages      []dynamicPackageCoverageComparison `json:"packages"`
}
type dynamicCoverage struct {
	Covered           int      `json:"covered_statements"`
	Total             int      `json:"total_statements"`
	Percent           *float64 `json:"percent,omitempty"`
	Status            string   `json:"status"`
	AttributionStatus string   `json:"package_attribution_status"`
	UnitIdentities    []string `json:"-"`
}
type snapshotEntry struct {
	path, mode string
	data       []byte
}
type snapshotSpec struct {
	label, rev string
	overlay    bool
}

func runDynamicTests(parent context.Context, scope, head string, base *baseInfo, root string, repeats int, perRun, total time.Duration, artifactPath string) *dynamicEvidence {
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	started := time.Now()
	e := &dynamicEvidence{Status: "unavailable", Environment: map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GONOSUMDB": "*", "GONOPROXY": "*", "GOFLAGS": "-mod=readonly", "GOWORK": "off", "GIT_TERMINAL_PROMPT": "0"}, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, TestConfiguration: map[string]string{"count": "1", "shuffle": "off", "packages": "./..."}, TimeoutMillis: perRun.Milliseconds(), TotalTimeoutMillis: total.Milliseconds(), Runs: []dynamicRun{}, CoverageComparisons: []dynamicCoverageComparison{}, Limitations: []string{"Tests execute arbitrary repository code with current-user host privileges and host API access; this is not sandboxed", "Go lookup settings do not prevent test code from making arbitrary network requests", "Darwin/Linux process groups are terminated on cancellation; other platforms cannot guarantee descendant cleanup", "Elapsed times and outcomes are volatile execution evidence, separate from static measurements"}}
	abs, err := canonicalCandidate(root, artifactPath)
	if !supportsProcessGroupCleanup() {
		e.Limitations = append(e.Limitations, "process-group cleanup unsupported; dynamic execution not started")
		return e
	}
	if parent.Err() != nil {
		e.Status = "cancelled"
		e.Limitations = append(e.Limitations, "caller cancelled before dynamic execution")
		return e
	}
	if artifactPath == "" {
		e.Limitations = append(e.Limitations, "artifact directory not configured; durable logs and coverage are unavailable")
		return e
	}
	if err != nil || pathWithin(root, abs) {
		e.Limitations = append(e.Limitations, "unsafe or invalid artifact directory")
		return e
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		e.Limitations = append(e.Limitations, "artifact directory unavailable")
		return e
	}
	resolved, resolveErr := filepath.EvalSymlinks(abs)
	if resolveErr != nil || pathWithin(root, resolved) {
		e.Limitations = append(e.Limitations, "artifact directory resolves inside analyzed repository")
		return e
	}
	abs = resolved
	fi, statErr := os.Stat(abs)
	if statErr != nil || !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		e.Limitations = append(e.Limitations, "artifact directory permissions are not private")
		return e
	}
	e.ArtifactDirectory = abs
	gv, err := goVersionContext(ctx)
	if err != nil {
		if parent.Err() != nil {
			e.Status = "cancelled"
			e.Limitations = append(e.Limitations, "caller cancellation before execution")
		} else if ctx.Err() != nil {
			e.Status = "partial"
			e.Limitations = append(e.Limitations, "total timeout before execution")
		} else {
			e.Limitations = append(e.Limitations, "local Go toolchain unavailable")
		}
		return e
	}
	e.Toolchain = gv
	specs := []snapshotSpec{}
	if scope == "all" && head == "" {
		e.Limitations = append(e.Limitations, "repository has no HEAD/current tracked snapshot")
		return e
	}
	if scope == "branch" && base != nil {
		specs = append(specs, snapshotSpec{"baseline", base.MergeBase, false})
		specs = append(specs, snapshotSpec{"HEAD", head, false})
		specs = append(specs, snapshotSpec{"current_tracked_worktree", head, true})
	} else if scope == "branch" {
		e.Limitations = append(e.Limitations, "branch baseline unavailable")
		return e
	} else if scope == "all" {
		specs = append(specs, snapshotSpec{"current_tracked_worktree", head, true})
	}
	seen := map[string]int{}
	identityRuns := map[string][]int{}
	budget := &artifactBudget{}
	for _, sp := range specs {
		if ctx.Err() != nil {
			status, class := "unavailable", "total_timeout"
			if parent.Err() != nil {
				status, class = "cancelled", "caller_cancellation"
			}
			e.Runs = append(e.Runs, newDynamicRun(sp.label, status, class))
			continue
		}
		dir, identity, er := materializeDynamicSnapshotContext(ctx, root, sp)
		if er != nil {
			class, status := "snapshot_error", "unavailable"
			if ctx.Err() != nil {
				class = "total_timeout"
				if parent.Err() != nil {
					class = "caller_cancellation"
					status = "cancelled"
				}
			}
			e.Runs = append(e.Runs, newDynamicRun(sp.label, status, class))
			continue
		}
		if prior, ok := seen[identity]; ok {
			e.Runs = append(e.Runs, newDynamicRunWithIdentity(sp.label, identity, "deduplicated", fmt.Sprintf("same source snapshot as run %d", prior)))
			os.RemoveAll(dir)
			continue
		}
		first := len(e.Runs) + 1
		for i := 0; i < repeats; i++ {
			if ctx.Err() != nil {
				e.Runs = append(e.Runs, newDynamicRunWithIdentity(sp.label, identity, map[bool]string{true: "cancelled", false: "unavailable"}[parent.Err() != nil], map[bool]string{true: "caller_cancellation", false: "total_timeout"}[parent.Err() != nil]))
				break
			}
			r := executeDynamic(ctx, dir, abs, sp.label, identity, perRun, i, budget)
			r.RunIndex = i + 1
			r.RunOrder = len(e.Runs) + 1
			e.Runs = append(e.Runs, r)
			identityRuns[identity] = append(identityRuns[identity], len(e.Runs)-1)
		}
		seen[identity] = first
		os.RemoveAll(dir)
	}
	for _, idxs := range identityRuns {
		if len(idxs) > 1 {
			first := runOutcomeFingerprint(e.Runs[idxs[0]])
			unstable := false
			for _, i := range idxs[1:] {
				if runOutcomeFingerprint(e.Runs[i]) != first {
					unstable = true
				}
			}
			if unstable {
				for _, i := range idxs {
					e.Runs[i].Instability = "repeated observed outcomes differ; this is not proof of flakiness"
				}
			}
		}
	}
	for i := range e.Runs {
		e.Runs[i].RunOrder = i + 1
	}
	comparisonArtifactsIncomplete := false
	for i := 0; i < len(e.Runs); i++ {
		for j := i + 1; j < len(e.Runs); j++ {
			a, b := e.Runs[i], e.Runs[j]
			if a.Status == "deduplicated" || b.Status == "deduplicated" {
				continue
			}
			c := dynamicCoverageComparison{From: a.Snapshot, To: b.Snapshot, FromRunOrder: a.RunOrder, ToRunOrder: b.RunOrder, Status: "unavailable", Packages: comparePackageCoverage(a, b)}
			c.FromCovered = a.Coverage.Covered
			c.FromTotal = a.Coverage.Total
			c.FromPercent = a.Coverage.Percent
			c.ToCovered = b.Coverage.Covered
			c.ToTotal = b.Coverage.Total
			c.ToPercent = b.Coverage.Percent
			if (a.Coverage.Status == "measured" || a.Coverage.Status == "measured_zero") && (b.Coverage.Status == "measured" || b.Coverage.Status == "measured_zero") {
				if len(a.Coverage.UnitIdentities) > 0 && sameStrings(a.Coverage.UnitIdentities, b.Coverage.UnitIdentities) {
					delta := b.Coverage.Covered - a.Coverage.Covered
					c.Status = "compared"
					c.CompatibleDenominator = true
					c.CoveredDelta = &delta
				} else {
					c.Status = "incompatible_denominator"
				}
			}
			if len(c.Packages) > 0 {
				data, err := json.Marshal(dynamicPackageComparisonArtifact{FormatVersion: 1, Kind: "brag-dynamic-package-comparison", From: c.From, To: c.To, Packages: c.Packages})
				if err == nil {
					c.PackageComparisonsArtifact = writeDynamicArtifact(abs, budget, fmt.Sprintf("package-comparison-%d-%d-", c.FromRunOrder, c.ToRunOrder), ".json", data)
				}
				if c.PackageComparisonsArtifact == "" {
					comparisonArtifactsIncomplete = true
				}
			}
			e.CoverageComparisons = append(e.CoverageComparisons, c)
		}
	}
	e.Status = "measured"
	if parent.Err() != nil {
		e.Status = "cancelled"
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(started) >= total {
		e.Status = "partial"
	}
	for _, r := range e.Runs {
		if r.Status == "unavailable" || r.Status == "timeout" || r.Status == "error" || r.OutputTruncated || r.ArtifactIncomplete {
			e.Status = "partial"
		}
	}
	if comparisonArtifactsIncomplete {
		e.Status = "partial"
		e.Limitations = append(e.Limitations, "one or more per-package comparison artifacts could not be persisted within artifact limits")
	}
	if parent.Err() != nil {
		e.Status = "cancelled"
	}
	for i := range e.Runs {
		e.Runs[i].RunOrder = i + 1
		sort.Slice(e.Runs[i].Tests, func(a, b int) bool {
			if e.Runs[i].Tests[a].Package == e.Runs[i].Tests[b].Package {
				return e.Runs[i].Tests[a].Name < e.Runs[i].Tests[b].Name
			}
			return e.Runs[i].Tests[a].Package < e.Runs[i].Tests[b].Package
		})
		sort.Slice(e.Runs[i].Packages, func(a, b int) bool { return e.Runs[i].Packages[a].Package < e.Runs[i].Packages[b].Package })
	}
	return e
}
func runOutcomeFingerprint(r dynamicRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v", r.TestCounts)
	tests := append([]dynamicTest(nil), r.Tests...)
	sort.Slice(tests, func(i, j int) bool {
		if tests[i].Package == tests[j].Package {
			return tests[i].Name < tests[j].Name
		}
		return tests[i].Package < tests[j].Package
	})
	for _, t := range tests {
		fmt.Fprintf(&b, "\x00%s\x00%s\x00%s", t.Package, t.Name, t.Outcome)
	}
	return b.String()
}
func comparePackageCoverage(a, b dynamicRun) []dynamicPackageCoverageComparison {
	left := map[string]dynamicPackage{}
	right := map[string]dynamicPackage{}
	for _, p := range a.Packages {
		left[p.Package] = p
	}
	for _, p := range b.Packages {
		right[p.Package] = p
	}
	keys := map[string]bool{}
	for k := range left {
		keys[k] = true
	}
	for k := range right {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	out := make([]dynamicPackageCoverageComparison, 0, len(ordered))
	for _, k := range ordered {
		x, xok := left[k]
		y, yok := right[k]
		c := dynamicPackageCoverageComparison{Package: k, Status: "unavailable"}
		if !xok {
			c.Status = "added"
			c.ToCovered = y.Covered
			c.ToTotal = y.Total
			c.ToPercent = y.CoveragePercent
		} else if !yok {
			c.Status = "removed"
			c.FromCovered = x.Covered
			c.FromTotal = x.Total
			c.FromPercent = x.CoveragePercent
		} else {
			c.FromCovered = x.Covered
			c.FromTotal = x.Total
			c.FromPercent = x.CoveragePercent
			c.ToCovered = y.Covered
			c.ToTotal = y.Total
			c.ToPercent = y.CoveragePercent
			if x.CoverageStatus == "measured" && y.CoverageStatus == "measured" && len(x.UnitIdentities) > 0 && len(y.UnitIdentities) > 0 {
				if sameStrings(x.UnitIdentities, y.UnitIdentities) {
					if x.Covered == y.Covered {
						c.Status = "same"
					} else {
						c.Status = "compared"
					}
				} else {
					c.Status = "incompatible"
				}
			}
		}
		out = append(out, c)
	}
	return out
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func newDynamicRun(label, status, class string) dynamicRun {
	return newDynamicRunWithIdentity(label, "", status, class)
}
func newDynamicRunWithIdentity(label, identity, status, class string) dynamicRun {
	return dynamicRun{Snapshot: label, Identity: identity, Status: status, Classification: class, Tests: []dynamicTest{}, TestCounts: map[string]int{"run": 0, "pass": 0, "fail": 0, "skip": 0, "discovered": 0}, PackageCounts: map[string]int{"pass": 0, "fail": 0, "skip": 0}, Packages: []dynamicPackage{}, Coverage: dynamicCoverage{Status: "unavailable", AttributionStatus: "unavailable"}}
}

type artifactBudget struct{ used int64 }

func canonicalCandidate(root, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	cur := abs
	tail := []string{}
	for {
		if _, err = os.Lstat(cur); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
	cur, err = filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		cur = filepath.Join(cur, tail[i])
	}
	if pathWithin(rootAbs, cur) {
		return cur, nil
	}
	return filepath.Clean(cur), nil
}
func pathWithin(root, path string) bool {
	r, e := filepath.EvalSymlinks(root)
	if e != nil {
		r, e = filepath.Abs(root)
	}
	if e != nil {
		return true
	}
	p, pe := filepath.EvalSymlinks(path)
	if pe != nil {
		p, pe = filepath.Abs(path)
	}
	if pe != nil {
		return true
	}
	rel, e := filepath.Rel(r, p)
	return e != nil || rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}
func goEnvironment(home, tmp string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp, "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GONOSUMDB=*", "GONOPROXY=*", "GOFLAGS=-mod=readonly", "GOWORK=off", "GIT_TERMINAL_PROMPT=0", "CI=1"}
}
func goVersion() (string, error) { return goVersionContext(context.Background()) }
func goVersionContext(ctx context.Context) (string, error) {
	base, e := os.MkdirTemp("", "brag-go-version-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(base)
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	os.Mkdir(home, 0700)
	os.Mkdir(tmp, 0700)
	c := exec.CommandContext(ctx, "go", "version")
	configureDynamicProcess(c)
	c.Env = goEnvironment(home, tmp)
	b, e := c.Output()
	return strings.TrimSpace(string(b)), e
}
func materializeDynamicSnapshot(root string, sp snapshotSpec) (string, string, error) {
	return materializeDynamicSnapshotContext(context.Background(), root, sp)
}
func materializeDynamicSnapshotContext(ctx context.Context, root string, sp snapshotSpec) (string, string, error) {
	var entries []snapshotEntry
	var err error
	if sp.overlay {
		entries, err = workingEntriesContext(ctx, root)
	} else {
		entries, err = treeEntriesContext(ctx, root, sp.rev)
	}
	if err != nil {
		return "", "", err
	}
	if len(entries) > dynamicFilesLimit {
		return "", "", fmt.Errorf("snapshot file cap exceeded")
	}
	d, err := os.MkdirTemp("", "brag-snapshot-")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	total := 0
	fail := func(e error) (string, string, error) { os.RemoveAll(d); return "", "", e }
	for _, x := range entries {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if !safeRelative(x.path) {
			return fail(fmt.Errorf("unsafe snapshot path"))
		}
		if len(x.data) > dynamicFileBytes || total+len(x.data) > dynamicSourceBytes {
			return fail(fmt.Errorf("snapshot byte cap exceeded"))
		}
		total += len(x.data)
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", x.path, x.mode, len(x.data))
		h.Write(x.data)
		p := filepath.Join(d, filepath.FromSlash(x.path))
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return fail(err)
		}
		mode := os.FileMode(0600)
		if x.mode == "100755" {
			mode = 0700
		} else if x.mode != "100644" {
			return fail(fmt.Errorf("unsupported mode %s", x.mode))
		}
		if err = os.WriteFile(p, x.data, mode); err != nil {
			return fail(err)
		}
	}
	return d, hex.EncodeToString(h.Sum(nil)), nil
}
func safeRelative(p string) bool {
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func treeEntries(root, rev string) ([]snapshotEntry, error) {
	return treeEntriesContext(context.Background(), root, rev)
}
func treeEntriesContext(ctx context.Context, root, rev string) ([]snapshotEntry, error) {
	b, e := runGitBoundedContext(ctx, root, 8<<20, "ls-tree", "-r", "-z", "--full-tree", rev)
	if e != nil {
		return nil, e
	}
	out := []snapshotEntry{}
	total := 0
	for _, ent := range bytes.Split(b, []byte{0}) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(ent) == 0 {
			continue
		}
		p := bytes.SplitN(ent, []byte("\t"), 2)
		if len(p) != 2 {
			return nil, errors.New("malformed git tree")
		}
		f := strings.Fields(string(p[0]))
		if len(f) != 3 || f[1] != "blob" || (f[0] != "100644" && f[0] != "100755") {
			return nil, fmt.Errorf("unsupported tracked tree entry")
		}
		path := string(p[1])
		if !safeRelative(path) {
			return nil, errors.New("unsafe Git tree path")
		}
		sz, e := runGitBoundedContext(ctx, root, 64, "cat-file", "-s", f[2])
		if e != nil {
			return nil, e
		}
		n, e := strconv.Atoi(strings.TrimSpace(string(sz)))
		if e != nil || n < 0 || n > dynamicFileBytes || total+n > dynamicSourceBytes || len(out) >= dynamicFilesLimit {
			return nil, errors.New("tracked blob size or snapshot limit exceeded")
		}
		total += n
		data, e := gitBoundedContext(ctx, root, int64(n), "cat-file", "blob", f[2])
		if e != nil || len(data) != n {
			return nil, errors.New("tracked blob read failed or size mismatch")
		}
		out = append(out, snapshotEntry{path, f[0], data})
	}
	return out, nil
}
func workingEntries(root string) ([]snapshotEntry, error) {
	return workingEntriesContext(context.Background(), root)
}
func workingEntriesContext(ctx context.Context, root string) ([]snapshotEntry, error) {
	b, e := runGitBoundedContext(ctx, root, 8<<20, "ls-files", "--stage", "-z")
	if e != nil {
		return nil, e
	}
	out := []snapshotEntry{}
	total := 0
	for _, ent := range bytes.Split(b, []byte{0}) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(ent) == 0 {
			continue
		}
		p := bytes.SplitN(ent, []byte("\t"), 2)
		if len(p) != 2 {
			return nil, errors.New("malformed index entry")
		}
		f := strings.Fields(string(p[0]))
		if len(f) != 3 || f[2] != "0" || (f[0] != "100644" && f[0] != "100755") {
			return nil, errors.New("unsupported index mode or conflict")
		}
		path := string(p[1])
		if !safeRelative(path) {
			return nil, errors.New("unsafe index path")
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		fi, er := os.Lstat(full)
		if os.IsNotExist(er) {
			continue
		}
		if er != nil || !fi.Mode().IsRegular() || hasSymlinkParent(root, path) {
			return nil, fmt.Errorf("unreliable tracked worktree path %s", path)
		}
		if fi.Size() > dynamicFileBytes || total+int(fi.Size()) > dynamicSourceBytes || len(out) >= dynamicFilesLimit {
			return nil, errors.New("tracked worktree snapshot limit exceeded")
		}
		total += int(fi.Size())
		data, er := os.ReadFile(full)
		if er != nil {
			return nil, er
		}
		mode := f[0]
		if fi.Mode().Perm()&0111 != 0 {
			mode = "100755"
		}
		out = append(out, snapshotEntry{path, mode, data})
	}
	return out, nil
}
func runGitBoundedContext(ctx context.Context, root string, limit int64, args ...string) ([]byte, error) {
	return gitBoundedContext(ctx, root, limit, args...)
}
func gitBounded(root string, limit int64, args ...string) ([]byte, error) {
	return gitBoundedContext(context.Background(), root, limit, args...)
}
func gitBoundedContext(ctx context.Context, root string, limit int64, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	configureDynamicProcess(c)
	c.Env = gitEnvironment(os.Environ())
	var b limitedBuffer
	b.limit = int(limit) + 1
	c.Stdout = &b
	if e := c.Run(); e != nil {
		return nil, e
	}
	if b.Len() > int(limit) {
		return nil, errors.New("Git output cap exceeded")
	}
	return b.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		take := b.limit - b.Len()
		if take > len(p) {
			take = len(p)
		}
		b.Buffer.Write(p[:take])
	}
	if b.Len() >= b.limit {
		b.truncated = true
	}
	return n, nil
}

type cappedOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		take := remaining
		if take > len(p) {
			take = len(p)
		}
		b.Buffer.Write(p[:take])
	}
	if n > remaining {
		b.truncated = true
	}
	return n, nil
}
func executeDynamic(total context.Context, dir, artifactDir, label, identity string, per time.Duration, iteration int, budget *artifactBudget) dynamicRun {
	r := newDynamicRunWithIdentity(label, identity, "error", "setup_error")
	work, e := os.MkdirTemp("", "brag-run-")
	if e != nil {
		r.Classification = "setup_error"
		return r
	}
	defer os.RemoveAll(work)
	home := filepath.Join(work, "home")
	tmp := filepath.Join(work, "tmp")
	if e = os.Mkdir(home, 0700); e != nil {
		r.Status = "error"
		r.Classification = "setup_error"
		r.Diagnostic = "could not create isolated Go home"
		return r
	}
	if e = os.Mkdir(tmp, 0700); e != nil {
		r.Status = "error"
		r.Classification = "setup_error"
		r.Diagnostic = "could not create isolated temporary directory"
		return r
	}
	ctx, cancel := context.WithTimeout(total, per)
	defer cancel()
	profile := filepath.Join(work, "coverage.out")
	cmd := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-shuffle=off", "-coverprofile", profile, "./...")
	configureDynamicProcess(cmd)
	cmd.Dir = dir
	cmd.Env = goEnvironment(home, tmp)
	cmd.WaitDelay = 2 * time.Second
	out := &cappedOutput{limit: dynamicOutputBytes}
	stderr := &cappedOutput{limit: dynamicOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	r.ElapsedMillis = time.Since(start).Milliseconds()
	r.OutputTruncated = out.truncated || stderr.truncated
	if total.Err() != nil {
		if errors.Is(total.Err(), context.Canceled) {
			r.Status = "cancelled"
			r.Classification = "caller_cancellation"
		} else {
			r.Status = "timeout"
			r.Classification = "total_timeout"
		}
	} else if ctx.Err() != nil {
		r.Status = "timeout"
		r.Classification = "run_timeout"
	} else if err != nil {
		r.Status = "fail"
		r.Classification = "execution_error"
	} else {
		r.Status = "pass"
		r.Classification = "completed"
	}
	logPath := writeDynamicArtifact(artifactDir, budget, fmt.Sprintf("run-%s-%d-", identity[:12], iteration), ".jsonl", out.Bytes())
	r.LogArtifact = logPath
	if r.LogArtifact == "" {
		r.ArtifactIncomplete = true
	}
	if stderr.Len() > 0 && writeDynamicArtifact(artifactDir, budget, fmt.Sprintf("stderr-%s-%d-", identity[:12], iteration), ".log", stderr.Bytes()) == "" {
		r.ArtifactIncomplete = true
	}
	parseGoEvents(out.Bytes(), &r)
	if err != nil && ctx.Err() == nil && total.Err() == nil {
		if r.TestCounts["fail"] > 0 {
			r.Classification = "test_failure"
		} else {
			r.Classification = classifyOutput(r.Diagnostic + out.String() + stderr.String())
		}
	}
	if fi, er := os.Stat(profile); er == nil && fi.Size() <= dynamicArtifactBytes {
		b, er := os.ReadFile(profile)
		if er != nil {
			r.Coverage.Status = "unavailable"
			r.ArtifactIncomplete = true
			writeDynamicRunDetails(artifactDir, budget, iteration, &r)
			return r
		}
		covPath := writeDynamicArtifact(artifactDir, budget, fmt.Sprintf("coverage-%s-%d-", identity[:12], iteration), ".out", b)
		r.CoverageArtifact = covPath
		if covPath == "" {
			r.ArtifactIncomplete = true
		}
		parseCoverage(b, &r)
		if r.Coverage.Status != "measured" && r.Coverage.Status != "measured_zero" {
			r.ArtifactIncomplete = true
		}
	} else if er == nil {
		r.Coverage.Status = "partial"
		r.ArtifactIncomplete = true
	} else {
		r.Coverage.Status = "unavailable"
		r.ArtifactIncomplete = true
		if err != nil {
			if r.TestCounts["fail"] > 0 {
				r.Classification = "test_failure"
			} else {
				r.Classification = classifyOutput(r.Diagnostic + out.String() + stderr.String())
			}
		}
	}
	writeDynamicRunDetails(artifactDir, budget, iteration, &r)
	return r
}
func writeDynamicRunDetails(dir string, budget *artifactBudget, iteration int, r *dynamicRun) {
	r.RunIndex = iteration + 1
	sort.Slice(r.Tests, func(i, j int) bool {
		if r.Tests[i].Package == r.Tests[j].Package {
			return r.Tests[i].Name < r.Tests[j].Name
		}
		return r.Tests[i].Package < r.Tests[j].Package
	})
	sort.Slice(r.Packages, func(i, j int) bool { return r.Packages[i].Package < r.Packages[j].Package })
	r.PackageCount = len(r.Packages)
	r.PackageCounts = map[string]int{"pass": 0, "fail": 0, "skip": 0}
	for _, p := range r.Packages {
		r.PackageCounts[p.Outcome]++
	}
	unitIdentities := r.Coverage.UnitIdentities
	if unitIdentities == nil {
		unitIdentities = []string{}
	}
	data, err := json.Marshal(dynamicRunDetailsArtifact{FormatVersion: 1, Kind: "brag-dynamic-run-details", Snapshot: r.Snapshot, RunIndex: r.RunIndex, SnapshotIdentity: r.Identity, Tests: r.Tests, Packages: r.Packages, CoverageUnitIdentities: unitIdentities})
	if err != nil {
		r.ArtifactIncomplete = true
		return
	}
	r.DetailsArtifact = writeDynamicArtifact(dir, budget, fmt.Sprintf("details-%s-%d-", r.Identity[:12], iteration), ".json", data)
	if r.DetailsArtifact == "" {
		r.ArtifactIncomplete = true
	}
}
func writeDynamicArtifact(dir string, budget *artifactBudget, prefix, suffix string, data []byte) string {
	if len(data) > dynamicArtifactBytes || budget == nil || budget.used+int64(len(data)) > dynamicArtifactTotalBytes {
		return ""
	}
	f, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return ""
	}
	name := f.Name() + suffix
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		return ""
	}
	if err = os.Rename(f.Name(), name); err != nil {
		os.Remove(f.Name())
		return ""
	}
	budget.used += int64(len(data))
	return name
}
func parseGoEvents(b []byte, r *dynamicRun) {
	dec := json.NewDecoder(bytes.NewReader(b))
	discovered := map[string]bool{}
	for {
		var ev struct {
			Action   string  `json:"Action"`
			Package  string  `json:"Package"`
			Test     string  `json:"Test"`
			Elapsed  float64 `json:"Elapsed"`
			Coverage string  `json:"Coverage"`
			Output   string  `json:"Output"`
		}
		if dec.Decode(&ev) != nil {
			break
		}
		if ev.Output != "" {
			r.Diagnostic = bounded(r.Diagnostic+ev.Output, 8192)
		}
		if ev.Test != "" {
			key := ev.Package + "\x00" + ev.Test
			if !discovered[key] {
				discovered[key] = true
				r.TestCounts["discovered"]++
			}
			if ev.Action == "run" {
				r.TestCounts["run"]++
			}
			if ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip" {
				r.TestCounts[ev.Action]++
				r.Tests = append(r.Tests, dynamicTest{ev.Package, ev.Test, ev.Action, ev.Elapsed})
			}
		} else if ev.Package != "" && (ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip") {
			p := dynamicPackage{Package: ev.Package, Outcome: ev.Action, Elapsed: ev.Elapsed, CoverageStatus: "unavailable"}
			if ev.Coverage != "" {
				if n, e := strconv.ParseFloat(strings.TrimSuffix(ev.Coverage, "%"), 64); e == nil {
					p.CoveragePercent = &n
				}
			}
			r.Packages = append(r.Packages, p)
		}
	}
}
func parseCoverage(b []byte, r *dynamicRun) {
	r.Coverage.AttributionStatus = "unavailable"
	for i := range r.Packages {
		r.Packages[i].CoveragePercent = nil
		r.Packages[i].Covered = 0
		r.Packages[i].Total = 0
		r.Packages[i].CoverageStatus = "unavailable"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "mode: ") {
		return
	}
	if len(lines) == 1 {
		r.Coverage.Status = "measured_zero"
		return
	}
	invalid := false
	allMapped := true
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) != 3 {
			invalid = true
			continue
		}
		statements, e := strconv.Atoi(f[1])
		covered, ce := strconv.Atoi(f[2])
		if e != nil || ce != nil || statements < 0 || covered < 0 {
			invalid = true
			continue
		}
		r.Coverage.Total += statements
		unit := f[0] + "#statements=" + f[1]
		r.Coverage.UnitIdentities = append(r.Coverage.UnitIdentities, unit)
		pkg := filepath.ToSlash(filepath.Dir(strings.SplitN(f[0], ":", 2)[0]))
		mapped := false
		for i := range r.Packages {
			if r.Packages[i].Package == pkg {
				mapped = true
				r.Packages[i].UnitIdentities = append(r.Packages[i].UnitIdentities, unit)
				r.Packages[i].CoverageStatus = "measured"
				r.Packages[i].Total += statements
				if covered > 0 {
					r.Packages[i].Covered += statements
				}
			}
		}
		if !mapped {
			allMapped = false
		}
		if covered > 0 {
			r.Coverage.Covered += statements
		}
	}
	for i := range r.Packages {
		if r.Packages[i].Total > 0 {
			p := 100 * float64(r.Packages[i].Covered) / float64(r.Packages[i].Total)
			r.Packages[i].CoveragePercent = &p
		} else {
			r.Packages[i].CoverageStatus = "unavailable"
			r.Packages[i].CoveragePercent = nil
		}
	}
	sort.Strings(r.Coverage.UnitIdentities)
	for i := range r.Packages {
		sort.Strings(r.Packages[i].UnitIdentities)
	}
	if invalid {
		r.Coverage.Status = "partial"
		for i := range r.Packages {
			if r.Packages[i].Total > 0 {
				r.Packages[i].CoverageStatus = "partial"
			}
		}
		return
	}
	for i := range r.Packages {
		if r.Packages[i].Total == 0 {
			r.Packages[i].CoverageStatus = "unavailable"
		}
	}
	if r.Coverage.Total > 0 {
		if allMapped {
			r.Coverage.AttributionStatus = "measured"
		}
		v := 100 * float64(r.Coverage.Covered) / float64(r.Coverage.Total)
		r.Coverage.Percent = &v
		r.Coverage.Status = "measured"
	} else {
		r.Coverage.Status = "measured_zero"
	}
}
func bounded(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func classifyOutput(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "no required module provides") || strings.Contains(l, "module lookup disabled") || strings.Contains(l, "updates to go.mod needed") || strings.Contains(l, "cannot find module providing package") || strings.Contains(l, "missing go.sum entry"):
		return "dependency_error"
	case strings.Contains(l, "build failed") || strings.Contains(l, "undefined:") || strings.Contains(l, "syntax error"):
		return "compile_error"
	case strings.Contains(l, "setup failed"):
		return "setup_error"
	case strings.Contains(l, "--- fail:"):
		return "test_failure"
	default:
		return "infrastructure_or_test_error"
	}
}
