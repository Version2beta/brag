package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Person struct {
	Name               string `json:"name"`
	Email              string `json:"email"`
	AuthorCommits      int    `json:"author_commits"`
	CoauthorCommits    int    `json:"coauthor_commits"`
	LinesAdded         int    `json:"lines_added"`
	LinesDeleted       int    `json:"lines_deleted"`
	SourceLinesAdded   int    `json:"source_lines_added"`
	SourceLinesDeleted int    `json:"source_lines_deleted"`
	AuthorCommitRatio  ratio  `json:"author_commit_ratio"`
}
type ratio struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value"`
}
type unavailable struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}
type Counts struct {
	LinesAdded         int `json:"lines_added"`
	LinesDeleted       int `json:"lines_deleted"`
	SourceLinesAdded   int `json:"source_lines_added"`
	SourceLinesDeleted int `json:"source_lines_deleted"`
}
type report struct {
	SchemaVersion              int                         `json:"schema_version"`
	ToolVersion                string                      `json:"tool_version"`
	ConfigVersion              int                         `json:"config_version"`
	Repository                 string                      `json:"repository"`
	Scope                      string                      `json:"scope"`
	Head                       string                      `json:"head"`
	Base                       *baseInfo                   `json:"base"`
	History                    historyInfo                 `json:"history"`
	SelectedIdentity           identityInfo                `json:"selected_identity"`
	WorkingTreeStateID         string                      `json:"working_tree_state_identity"`
	WorkingTreeStateMethod     string                      `json:"working_tree_state_method"`
	Attribution                attribution                 `json:"attribution"`
	Contributors               []Person                    `json:"contributors"`
	RepositoryMeasures         repositoryMeasures          `json:"repository_measures"`
	NetSourceChange            *netChange                  `json:"net_source_change"`
	SelectedContributor        *selected                   `json:"selected_contributor"`
	Measures                   measureInfo                 `json:"measures"`
	WorkingTree                worktree                    `json:"working_tree"`
	Unavailable                map[string]unavailable      `json:"unavailable"`
	Warnings                   []string                    `json:"warnings"`
	EngineeringEvidence        engineeringEvidence         `json:"engineering_evidence"`
	DynamicTests               *dynamicEvidence            `json:"dynamic_tests,omitempty"`
	MutationTests              *mutationReport             `json:"mutation_tests,omitempty"`
	TestContributions          *testContributionReport     `json:"test_contributions,omitempty"`
	TestContributionComparison *testContributionComparison `json:"test_contribution_comparison,omitempty"`
}
type identityInfo struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Available bool   `json:"available"`
}
type baseInfo struct {
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	MergeBase  string `json:"merge_base"`
	Provenance string `json:"provenance"`
}
type historyInfo struct {
	IncludedCommitIDs []string `json:"included_commit_ids"`
	Boundary          string   `json:"boundary"`
}
type attribution struct {
	AuthorCommits   int `json:"author_commits"`
	CoauthorCommits int `json:"coauthor_commits"`
}
type repositoryMeasures struct {
	CommitCount      int    `json:"commit_count"`
	ContributorCount int    `json:"contributor_count"`
	CumulativeChurn  Counts `json:"cumulative_churn"`
}
type netChange struct {
	Status       string `json:"status"`
	Baseline     string `json:"baseline"`
	LinesAdded   int    `json:"source_lines_added"`
	LinesDeleted int    `json:"source_lines_deleted"`
	NetLines     int    `json:"net_source_lines"`
	Method       string `json:"method"`
}
type selected struct {
	AuthorCommits     int    `json:"author_commits"`
	CoauthorCommits   int    `json:"coauthor_commits"`
	AuthorCommitRatio ratio  `json:"author_commit_ratio"`
	CumulativeChurn   Counts `json:"cumulative_churn"`
}
type measureInfo struct {
	CommitMethod     string   `json:"commit_method"`
	OverlayMethod    string   `json:"overlay_method"`
	RatioDenominator string   `json:"ratio_denominator"`
	SourceExtensions []string `json:"source_extensions"`
}
type worktree struct {
	Status                string `json:"status"`
	Counts                Counts `json:"counts"`
	TrackedPathsChanged   int    `json:"tracked_paths_changed"`
	UntrackedFilesPresent bool   `json:"untracked_files_present"`
}
type configData struct {
	Version    int               `json:"version"`
	Identities []identityInfo    `json:"identities"`
	Aliases    map[string]string `json:"aliases"`
}
type maps struct {
	exact   map[string]identityInfo
	aliases map[string]string
}

var repo string

const reportSchemaVersion = 2
const mutationReportSchemaVersion = 4
const contributionReportSchemaVersion = 5
const contributionComparisonSchemaVersion = 7

func gitEnvironment(env []string) []string {
	filtered := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_OPTIONAL_LOCKS=") || strings.HasPrefix(entry, "GIT_NO_LAZY_FETCH=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
}

func git(args ...string) ([]byte, error) {
	c := exec.Command("git", append([]string{"-C", repo}, args...)...)
	c.Env = gitEnvironment(os.Environ())
	return c.Output()
}
func text(args ...string) (string, error) {
	b, e := git(args...)
	return strings.TrimSpace(string(b)), e
}
func die(e error) { fmt.Fprintln(os.Stderr, "brag:", e); os.Exit(2) }
func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canonical, nil
}
func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "author-evidence" {
		if err := runAuthorEvidence(args[1:], os.Stdout); err != nil {
			die(err)
		}
		return
	}
	if len(args) > 0 && args[0] == "skill" {
		if err := runSkill(args[1:]); err != nil {
			die(err)
		}
		return
	}
	if len(args) > 1 && args[0] == "help" && args[1] == "skill" {
		printSkillHelp(os.Stdout)
		return
	}
	if len(args) == 1 && args[0] == "help" || hasHelpFlag(args) {
		if len(args) > 0 && args[0] == "all" {
			printAllHelp(os.Stdout)
		} else {
			printMainHelp(os.Stdout)
		}
		return
	}
	scope := "branch"
	if len(args) > 0 && args[0] == "all" {
		scope = "all"
		args = args[1:]
	}
	fs := flag.NewFlagSet("brag", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printMainHelp(fs.Output()) }
	baseArg := fs.String("base", "", "branch base ref")
	cfgArg := fs.String("config", "", "identity config file")
	whoArg := fs.String("selected", "", "selected contributor NAME=EMAIL")
	dynamicArg := fs.Bool("dynamic-tests", false, "explicitly execute Go tests in disposable snapshots")
	expectedRootArg := fs.String("expected-root", "", "required canonical repository root for dynamic or mutation execution")
	dynamicRepeat := fs.Int("dynamic-repeat", 1, "dynamic test runs per distinct snapshot (1-3)")
	dynamicTimeout := fs.Duration("dynamic-timeout", 2*time.Minute, "timeout per dynamic test run (maximum 10m)")
	dynamicTotal := fs.Duration("dynamic-total-timeout", 10*time.Minute, "total dynamic-test timeout (maximum 30m)")
	dynamicArtifacts := fs.String("dynamic-artifacts", "", "persistent dynamic artifact directory outside this repository")
	mutationArg := fs.Bool("mutation-tests", false, "explicitly execute bounded Go mutations in disposable snapshots")
	testContributionsArg := fs.Bool("test-contributions", false, "execute bounded per-test mutation analysis (requires --mutation-tests)")
	contributionRepeat := fs.Int("contribution-repeat", 1, "contribution evaluations per control/pair (1-3)")
	contributionTimeout := fs.Duration("contribution-timeout", time.Minute, "per contribution evaluation timeout (maximum 5m)")
	contributionTotal := fs.Duration("contribution-total-timeout", 10*time.Minute, "total contribution analysis timeout (maximum 30m)")
	mutationLimit := fs.Int("mutation-limit", 20, "mutants selected per snapshot (1-50)")
	var mutationPackages, mutationPaths, mutationSnapshots values
	fs.Var(&mutationPackages, "mutation-package", "mutation package import path filter (repeatable)")
	fs.Var(&mutationPaths, "mutation-path", "mutation repo-relative file/directory filter (repeatable)")
	fs.Var(&mutationSnapshots, "mutation-snapshot", "mutation snapshot baseline, HEAD, or current_tracked_worktree (repeatable)")
	var identities, aliases values
	fs.Var(&identities, "identity", "canonical NAME=EMAIL")
	fs.Var(&aliases, "alias", "alias EMAIL=CANONICAL_EMAIL")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		die(errors.New("unexpected arguments"))
	}
	top, e := text("rev-parse", "--show-toplevel")
	if e != nil {
		die(errors.New("must run inside a Git repository"))
	}
	repo = top
	if *testContributionsArg && !*mutationArg {
		die(errors.New("--test-contributions requires --mutation-tests"))
	}
	if *mutationArg && *dynamicArg {
		die(errors.New("--mutation-tests and --dynamic-tests cannot be combined"))
	}
	if *dynamicArg || *mutationArg {
		mode := "dynamic-tests"
		if *mutationArg {
			mode = "mutation-tests"
			if *testContributionsArg {
				mode += "+test-contributions"
			}
		}
		actualRoot, rootErr := canonicalDirectory(repo)
		if rootErr != nil {
			die(fmt.Errorf("resolve Git repository root: %w", rootErr))
		}
		fmt.Fprintf(os.Stderr, "brag: execution preflight: root=%s mode=%s\n", actualRoot, mode)
		if *expectedRootArg == "" {
			die(errors.New("--expected-root PATH is required with --dynamic-tests or --mutation-tests"))
		}
		expectedRoot, rootErr := canonicalDirectory(*expectedRootArg)
		if rootErr != nil {
			die(fmt.Errorf("invalid --expected-root: %w", rootErr))
		}
		actualInfo, statErr := os.Stat(actualRoot)
		if statErr != nil {
			die(fmt.Errorf("recheck canonical repository root: %w", statErr))
		}
		expectedInfo, statErr := os.Stat(expectedRoot)
		if statErr != nil {
			die(fmt.Errorf("recheck expected root: %w", statErr))
		}
		if !os.SameFile(actualInfo, expectedInfo) {
			die(fmt.Errorf("execution root mismatch: Git root %s does not match --expected-root %s", actualRoot, expectedRoot))
		}
	}
	if *testContributionsArg && *dynamicArg {
		die(errors.New("--test-contributions cannot be combined with --dynamic-tests"))
	}
	if *testContributionsArg && (*contributionRepeat < 1 || *contributionRepeat > 3 || *contributionTimeout <= 0 || *contributionTimeout > 5*time.Minute || *contributionTotal <= 0 || *contributionTotal > 30*time.Minute || *contributionTimeout%time.Millisecond != 0 || *contributionTotal%time.Millisecond != 0) {
		die(errors.New("contribution repeat must be 1-3; timeouts must be whole milliseconds, per-evaluation at most 5m and total at most 30m"))
	}
	if *testContributionsArg && len(mutationSnapshots) > 1 {
		die(errors.New("--test-contributions requires exactly one --mutation-snapshot"))
	}
	if *mutationArg {
		for _, p := range mutationPackages {
			if !validMutationPackageFilter(p) {
				die(fmt.Errorf("invalid --mutation-package %q", p))
			}
		}
		for _, p := range mutationPaths {
			if !validMutationPathFilter(p) {
				die(fmt.Errorf("invalid --mutation-path %q", p))
			}
		}
	}
	head, e := text("rev-parse", "HEAD")
	if e != nil && scope != "all" {
		die(errors.New("repository has no HEAD commit"))
	}
	if e != nil {
		head = ""
	}
	cfg := *cfgArg
	trackedConfig := cfg != ""
	if cfg == "" {
		cfg = filepath.Join(repo, ".brag.json")
		_, trackErr := git("ls-files", "--error-unmatch", ".brag.json")
		trackedConfig = trackErr == nil
	} else if !filepath.IsAbs(cfg) {
		cfg = filepath.Join(repo, cfg)
	}
	cm := readConfig(cfg, identities, aliases, trackedConfig)
	me := identityInfo{}
	if *whoArg != "" {
		me = parseIdentity(*whoArg, "--selected")
	} else {
		name, _ := text("config", "--get", "user.name")
		email, _ := text("config", "--get", "user.email")
		if name != "" && email != "" {
			me = identityInfo{Name: name, Email: email, Available: true}
		}
	}
	if me.Available {
		me = resolve(me, cm)
	}
	warns := []string{}
	var base *baseInfo
	revs := head
	boundary := "HEAD and commits reachable from HEAD"
	if scope == "branch" {
		ref := *baseArg
		prov := "explicit"
		if ref == "" {
			ref, e = text("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
			if e != nil || ref == "" {
				die(errors.New("cannot reliably select branch base; specify --base REF"))
			}
			prov = "configured upstream"
		}
		rc, e := text("rev-parse", "--verify", ref+"^{commit}")
		if e != nil {
			die(fmt.Errorf("invalid base %q", ref))
		}
		mb, e := text("merge-base", head, rc)
		if e != nil {
			die(errors.New("base has no merge-base with HEAD"))
		}
		base = &baseInfo{ref, rc, mb, prov}
		revs = mb + ".." + head
		boundary = "commits reachable from HEAD but not merge-base"
		warns = append(warns, "merge-base is a comparison boundary, not necessarily the original branch creation point")
	}
	hs := ""
	if revs != "" {
		hs, e = text("rev-list", "--reverse", revs)
		if e != nil {
			die(e)
		}
	}
	commits := []string{}
	if hs != "" {
		commits = strings.Split(hs, "\n")
	}
	history, e := collectHistory(repo, revs, commits, cm)
	if e != nil {
		die(e)
	}
	by := history.people
	all := history.churn
	authored, coauthored := history.authored, history.coauthored
	list := make([]Person, 0, len(by))
	for _, p := range by {
		list = append(list, *p)
	}
	sort.Slice(list, func(i, j int) bool {
		return idKey(identityInfo{Name: list[i].Name, Email: list[i].Email}) < idKey(identityInfo{Name: list[j].Name, Email: list[j].Email})
	})
	for i := range list {
		list[i].AuthorCommitRatio = makeRatio(list[i].AuthorCommits, len(commits))
	}
	ov, e := overlayRead(head)
	if e != nil {
		die(e)
	}
	stateID, stateMethod := stateIdentity(head)
	var sel *selected
	if me.Available {
		sel = &selected{}
		if p := by[idKey(me)]; p != nil {
			sel.AuthorCommits = p.AuthorCommits
			sel.CoauthorCommits = p.CoauthorCommits
			sel.AuthorCommitRatio = makeRatio(p.AuthorCommits, len(commits))
			sel.CumulativeChurn = Counts{p.LinesAdded, p.LinesDeleted, p.SourceLinesAdded, p.SourceLinesDeleted}
		} else {
			sel.AuthorCommitRatio = makeRatio(0, len(commits))
		}
	}
	un := map[string]unavailable{}
	if !me.Available {
		un["selected_contributor_metrics"] = unavailable{"unavailable", "selected identity is unavailable; use Git user.name/user.email or --selected"}
	}
	if head == "" {
		un["working_tree_state_identity"] = unavailable{"unavailable", "repository has no HEAD snapshot"}
	}
	for _, k := range []string{"pull_requests", "code_reviews", "test_coverage", "test_effectiveness", "reliability", "maintainability", "duplication"} {
		un[k] = unavailable{"unavailable", "not measured by the current implementation"}
	}
	var net *netChange
	if base == nil {
		un["net_source_change"] = unavailable{"unavailable", "no selected snapshot baseline for all scope"}
	} else {
		net = branchNet(base.MergeBase, head)
	}
	if me.Available {
		delete(un, "selected_contributor_metrics")
	}
	r := report{SchemaVersion: reportSchemaVersion, ToolVersion: "v0.1.0-rc.1", ConfigVersion: 1, Repository: ".", Scope: scope, Head: head, Base: base, History: historyInfo{commits, boundary}, SelectedIdentity: me, WorkingTreeStateID: stateID, WorkingTreeStateMethod: stateMethod, Attribution: attribution{authored, coauthored}, Contributors: list, RepositoryMeasures: repositoryMeasures{CommitCount: len(commits), ContributorCount: len(list), CumulativeChurn: all}, NetSourceChange: net, SelectedContributor: sel, Measures: measureInfo{"Git per-commit numstat; line churn belongs to commit author; coauthor counts are separate and can overlap author counts; merges use combined-diff default and may omit merge changes; binaries excluded", "tracked final index/worktree diff against HEAD exactly once; staged and unstaged changes coalesce; untracked contents excluded", "author commits / all included commits; coauthor counts are not added to this denominator", sourceExts()}, WorkingTree: ov, Unavailable: un, Warnings: warns}
	r.EngineeringEvidence = buildEngineeringEvidence(scope, head, base, repo, commits, me, cm)
	if *mutationArg && *dynamicArg {
		die(errors.New("--mutation-tests and --dynamic-tests cannot be combined"))
	}
	if *mutationArg {
		if *mutationLimit < 1 || *mutationLimit > 50 || *dynamicTimeout <= 0 || *dynamicTimeout > 10*time.Minute || *dynamicTotal <= 0 || *dynamicTotal > 30*time.Minute || *dynamicTimeout%time.Millisecond != 0 || *dynamicTotal%time.Millisecond != 0 {
			die(errors.New("mutation limit must be 1-50; timeouts must be whole milliseconds, per-run at most 10m and total at most 30m"))
		}
		valid := map[string]bool{"baseline": scope == "branch", "HEAD": head != "", "current_tracked_worktree": head != ""}
		for _, snapshot := range mutationSnapshots {
			if !valid[snapshot] {
				die(fmt.Errorf("mutation snapshot %q unavailable in selected scope", snapshot))
			}
		}
		r.SchemaVersion = mutationReportSchemaVersion
		if *testContributionsArg {
			r.SchemaVersion = contributionReportSchemaVersion
			if scope == "all" {
				r.SchemaVersion = contributionComparisonSchemaVersion
			} else {
				r.TestContributions = &testContributionReport{Status: "unavailable", Limitations: []string{"test-level contribution evaluation is unavailable in the selected mode"}}
			}
		}
		signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		r.MutationTests = runMutationTests(signalCtx, scope, head, base, repo, *mutationLimit, *dynamicTimeout, *dynamicTotal, *dynamicArtifacts, mutationPackages, mutationPaths, mutationSnapshots)
		if *testContributionsArg {
			if scope == "all" {
				r.TestContributionComparison = collectTestContributionComparison(signalCtx, repo, head, *dynamicArtifacts, mutationSnapshots, r.MutationTests, *contributionTimeout, *contributionTotal, *contributionRepeat, commits, me, cm)
			} else {
				r.TestContributions = collectContributionPopulation(signalCtx, repo, scope, head, base, *dynamicArtifacts, mutationSnapshots, r.MutationTests, *contributionTimeout, *contributionTotal, *contributionRepeat, commits, me, cm)
			}
		}
		dynamicCancelled := signalCtx.Err() != nil
		stopSignals()
		if dynamicCancelled {
			defer os.Exit(130)
		}
	}
	if *dynamicArg {
		if *dynamicRepeat < 1 || *dynamicRepeat > 3 || *dynamicTimeout <= 0 || *dynamicTimeout > 10*time.Minute || *dynamicTotal <= 0 || *dynamicTotal > 30*time.Minute || *dynamicTimeout%time.Millisecond != 0 || *dynamicTotal%time.Millisecond != 0 {
			die(errors.New("dynamic repeat must be 1-3; timeouts must be whole milliseconds, per-run at most 10m and total at most 30m"))
		}
		r.SchemaVersion = 3
		r.Unavailable["test_coverage"] = unavailable{"unavailable", "static snapshot coverage is not measured here; see dynamic_tests for execution coverage"}
		r.Unavailable["reliability"] = unavailable{"unavailable", "general reliability and flakiness are not established; repeated outcomes only report observed instability in dynamic_tests"}
		signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		r.DynamicTests = runDynamicTests(signalCtx, scope, head, base, repo, *dynamicRepeat, *dynamicTimeout, *dynamicTotal, *dynamicArtifacts)
		dynamicCancelled := signalCtx.Err() != nil
		stopSignals()
		if r.DynamicTests.Status == "measured" || r.DynamicTests.Status == "partial" {
			for _, run := range r.DynamicTests.Runs {
				if run.Coverage.Status == "measured" || run.Coverage.Status == "measured_zero" {
					delete(r.Unavailable, "test_coverage")
					break
				}
			}
		}
		if dynamicCancelled {
			r.DynamicTests.Status = "cancelled"
			r.Unavailable["test_coverage"] = unavailable{"unavailable", "static coverage unavailable; dynamic execution cancelled; see dynamic_tests"}
		}
		defer func() {
			if dynamicCancelled {
				os.Exit(130)
			}
		}()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if e := enc.Encode(r); e != nil {
		die(e)
	}
}

type values []string

func (v *values) String() string     { return strings.Join(*v, ",") }
func (v *values) Set(s string) error { *v = append(*v, s); return nil }
func parseIdentity(s, flag string) identityInfo {
	p := strings.SplitN(s, "=", 2)
	if len(p) != 2 || p[0] == "" || p[1] == "" {
		die(fmt.Errorf("%s requires NAME=EMAIL", flag))
	}
	return identityInfo{p[0], p[1], true}
}
func readConfig(path string, ids, als []string, enabled bool) maps {
	c := configData{Version: 1}
	var b []byte
	var e error
	if enabled {
		b, e = os.ReadFile(path)
	} else {
		e = os.ErrNotExist
	}
	if e == nil {
		if json.Unmarshal(b, &c) != nil || c.Version != 1 {
			die(fmt.Errorf("invalid identity config version or JSON: %s", path))
		}
	} else if !os.IsNotExist(e) {
		die(e)
	}
	m := maps{map[string]identityInfo{}, map[string]string{}}
	for _, i := range c.Identities {
		m.exact[strings.ToLower(i.Email)] = i
	}
	for k, v := range c.Aliases {
		m.aliases[strings.ToLower(k)] = v
	}
	for _, v := range ids {
		i := parseIdentity(v, "--identity")
		m.exact[strings.ToLower(i.Email)] = i
	}
	for _, v := range als {
		p := strings.SplitN(v, "=", 2)
		if len(p) != 2 {
			die(errors.New("--alias requires EMAIL=CANONICAL_EMAIL"))
		}
		m.aliases[strings.ToLower(p[0])] = p[1]
	}
	return m
}
func resolve(i identityInfo, m maps) identityInfo {
	if x, ok := m.exact[strings.ToLower(i.Email)]; ok {
		x.Available = true
		return x
	}
	if e, ok := m.aliases[strings.ToLower(i.Email)]; ok {
		if x, yes := m.exact[strings.ToLower(e)]; yes {
			x.Available = true
			return x
		}
		i.Email = e
	}
	return i
}
func idKey(i identityInfo) string { return strings.ToLower(i.Email) }
func getPerson(m map[string]*Person, i identityInfo) *Person {
	k := idKey(i)
	if m[k] == nil {
		m[k] = &Person{Name: i.Name, Email: i.Email}
	} else if i.Name < m[k].Name {
		m[k].Name = i.Name
	}
	return m[k]
}
func parseCoauthors(b string) []identityInfo {
	out := []identityInfo{}
	s := bufio.NewScanner(strings.NewReader(b))
	for s.Scan() {
		l := strings.TrimSpace(s.Text())
		if strings.HasPrefix(strings.ToLower(l), "co-authored-by:") {
			v := strings.TrimSpace(l[len("co-authored-by:"):])
			a := strings.LastIndex(v, "<")
			z := strings.LastIndex(v, ">")
			if a >= 0 && z > a {
				out = append(out, identityInfo{Name: strings.TrimSpace(v[:a]), Email: strings.TrimSpace(v[a+1 : z]), Available: true})
			}
		}
	}
	return out
}

var exts = []string{".c", ".cc", ".cpp", ".cs", ".go", ".h", ".hpp", ".java", ".js", ".jsx", ".kt", ".php", ".py", ".rb", ".rs", ".scala", ".sh", ".sql", ".swift", ".ts", ".tsx", ".vue", ".svelte"}

func source(p string) bool {
	e := strings.ToLower(filepath.Ext(p))
	for _, x := range exts {
		if e == x {
			return true
		}
	}
	return false
}
func sourceExts() []string { return exts }
func sum(b []byte) (o Counts) {
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		p := strings.Split(s.Text(), "\t")
		if len(p) < 3 || p[0] == "-" || p[1] == "-" {
			continue
		}
		var a, d int
		fmt.Sscanf(p[0], "%d", &a)
		fmt.Sscanf(p[1], "%d", &d)
		o.LinesAdded += a
		o.LinesDeleted += d
		if source(p[len(p)-1]) {
			o.SourceLinesAdded += a
			o.SourceLinesDeleted += d
		}
	}
	return
}
func add(a, b Counts) Counts {
	return Counts{a.LinesAdded + b.LinesAdded, a.LinesDeleted + b.LinesDeleted, a.SourceLinesAdded + b.SourceLinesAdded, a.SourceLinesDeleted + b.SourceLinesDeleted}
}
func makeRatio(n, d int) ratio {
	r := ratio{Numerator: n, Denominator: d}
	if d > 0 {
		v := float64(n) / float64(d)
		r.Value = &v
	}
	return r
}
func stateIdentity(head string) (string, string) {
	if head == "" {
		return "", "unavailable without a HEAD snapshot"
	}
	b, e := git("diff", "--raw", "-z", "--no-ext-diff", "--no-renames", "HEAD", "--")
	if e != nil {
		die(e)
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", h), "SHA-256 of git diff --raw -z --no-ext-diff --no-renames HEAD --; tracked changes only"
}
func branchNet(base, head string) *netChange {
	b, e := git("diff", "--numstat", "--no-ext-diff", base, head, "--")
	if e != nil {
		die(e)
	}
	c := sum(b)
	return &netChange{Status: "available", Baseline: "pinned merge-base " + base, LinesAdded: c.SourceLinesAdded, LinesDeleted: c.SourceLinesDeleted, NetLines: c.SourceLinesAdded - c.SourceLinesDeleted, Method: "git diff --numstat between pinned merge-base and HEAD, restricted to recognized source extensions; snapshot net change, not per-person attribution"}
}
func overlayRead(head string) (worktree, error) {
	if head == "" {
		un, e := text("ls-files", "--others", "--exclude-standard")
		if e != nil {
			return worktree{}, e
		}
		return worktree{Status: "unavailable", UntrackedFilesPresent: un != ""}, nil
	}
	b, e := git("diff", "--numstat", "--no-ext-diff", "-M", "HEAD", "--")
	if e != nil {
		return worktree{}, e
	}
	names, e := git("diff", "--name-only", "--no-ext-diff", "-M", "HEAD", "--")
	if e != nil {
		return worktree{}, e
	}
	un, e := text("ls-files", "--others", "--exclude-standard")
	if e != nil {
		return worktree{}, e
	}
	n := 0
	if len(bytes.TrimSpace(names)) > 0 {
		n = len(strings.Split(strings.TrimSpace(string(names)), "\n"))
	}
	return worktree{Status: "available", Counts: sum(b), TrackedPathsChanged: n, UntrackedFilesPresent: un != ""}, nil
}
