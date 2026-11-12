package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// This first method collects commit metadata only, not author test evidence.
const authorMetadataMethod = "brag-author-metadata-v1"

type authorMetadataLimits struct {
	ChunkCommits       int   `json:"chunk_commits"`
	ObjectBytes        int   `json:"object_bytes"`
	MessageBytes       int   `json:"message_bytes"`
	MetadataBytes      int   `json:"metadata_bytes"`
	RetainedCommits    int   `json:"retained_commits"`
	RetainedBytes      int   `json:"retained_bytes"`
	ChildTimeoutMillis int64 `json:"child_timeout_millis"`
	TotalTimeoutMillis int64 `json:"total_timeout_millis"`
	HistoryIDBytes     int   `json:"history_id_bytes"`
}

func defaultAuthorMetadataLimits() authorMetadataLimits {
	return authorMetadataLimits{500, 1 << 20, 64 << 10, 256 << 20, 500, 8 << 20, 5000, 300000, 64 << 20}
}

type authorText struct {
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
	SHA256   string `json:"sha256"`
}

func authorDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func encodeAuthorText(b []byte) authorText {
	v := authorText{Encoding: "utf-8", Data: string(b), SHA256: authorDigest(b)}
	if !utf8.Valid(b) {
		v.Encoding = "base64"
		v.Data = base64.StdEncoding.EncodeToString(b)
	}
	return v
}

type authorRecordedIdentity struct {
	Raw       identityInfo `json:"raw"`
	Canonical identityInfo `json:"canonical"`
}

type authorCommitRecord struct {
	OID                        string                   `json:"oid"`
	Parents                    []string                 `json:"parents"`
	ObjectSHA256               string                   `json:"object_sha256"`
	Author                     authorRecordedIdentity   `json:"author"`
	Committer                  authorRecordedIdentity   `json:"committer"`
	Coauthors                  []authorRecordedIdentity `json:"coauthors"`
	CoauthorStatus             string                   `json:"coauthor_status"`
	MatchBasis                 string                   `json:"match_basis"`
	Message                    authorText               `json:"message"`
	Subject                    authorText               `json:"subject"`
	Body                       authorText               `json:"body"`
	UnavailableFields          map[string]unavailable   `json:"unavailable_fields,omitempty"`
	UnavailableIdentityHeaders map[string][]authorText  `json:"unavailable_identity_headers,omitempty"`
}

type authorScope struct {
	Kind                string    `json:"kind"`
	Head                string    `json:"head"`
	ObjectFormat        string    `json:"object_format"`
	Shallow             bool      `json:"shallow"`
	Base                *baseInfo `json:"base"`
	SelectionSource     string    `json:"selection_source"`
	ConfigurationOrigin string    `json:"configuration_origin"`
	Boundary            string    `json:"boundary"`
}

type authorIdentityConfiguration struct {
	Source            string                  `json:"source"`
	IdentityOverrides []string                `json:"identity_overrides"`
	AliasOverrides    []string                `json:"alias_overrides"`
	Identities        map[string]identityInfo `json:"identities"`
	Aliases           map[string]string       `json:"aliases"`
	SHA256            string                  `json:"sha256"`
}

type authorMetadataPopulation struct {
	Status                     string `json:"status"`
	ScopedCommits              *int   `json:"scoped_commits"`
	SelectedCommits            *int   `json:"selected_commits"`
	EnumeratedCommits          int    `json:"enumerated_commits"`
	InspectedCommits           int    `json:"inspected_commits"`
	MetadataOmittedCommits     int    `json:"metadata_omitted_commits"`
	ObservedSelectedCommits    int    `json:"observed_selected_commits"`
	RetainedSelectedCommits    int    `json:"retained_selected_commits"`
	DetailOmittedCommits       int    `json:"detail_omitted_commits"`
	DetailsTruncated           bool   `json:"details_truncated"`
	MetadataBytes              int    `json:"metadata_bytes"`
	TraversalOrder             string `json:"traversal_order"`
	EnumeratedIDsSHA256        string `json:"enumerated_ids_sha256"`
	FirstEnumeratedCommit      string `json:"first_enumerated_commit"`
	LastEnumeratedCommit       string `json:"last_enumerated_commit"`
	LastObservedSelectedCommit string `json:"last_observed_selected_commit"`
	RetentionPolicy            string `json:"retention_policy"`
}

type authorBuildInfo struct {
	Revision *string `json:"revision"`
	Modified *bool   `json:"modified"`
}

func currentAuthorBuildInfo() authorBuildInfo {
	out := authorBuildInfo{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				value := setting.Value
				out.Revision = &value
			case "vcs.modified":
				if value, err := strconv.ParseBool(setting.Value); err == nil {
					out.Modified = &value
				}
			}
		}
	}
	return out
}

type authorEvidenceReport struct {
	ContractVersion string          `json:"contract_version"`
	MethodVersion   string          `json:"method_version"`
	ToolVersion     string          `json:"tool_version"`
	ToolBuild       authorBuildInfo `json:"tool_build"`
	Status          string          `json:"status"`
	Execution       string          `json:"execution"`
	Scope           authorScope     `json:"scope"`
	Target          struct {
		Kind   string `json:"kind"`
		Commit string `json:"commit"`
	} `json:"target"`
	Selected              authorRecordedIdentity      `json:"selected_identity"`
	IdentityConfiguration authorIdentityConfiguration `json:"identity_configuration"`
	InclusionPolicy       string                      `json:"inclusion_policy"`
	Metadata              authorMetadataPopulation    `json:"metadata"`
	Records               []authorCommitRecord        `json:"records"`
	Limits                authorMetadataLimits        `json:"limits"`
	Omissions             map[string]int              `json:"omissions"`
	Unavailable           map[string]unavailable      `json:"unavailable"`
}

type authorGit struct {
	root   string
	limits authorMetadataLimits
}

// Ignore inherited Git repository/command overrides; preserve only explicit config
// isolation used by callers. Disable repository executable hooks and lazy fetching.
func authorGitEnvironment() []string {
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") || strings.HasPrefix(e, "GIT_CONFIG_GLOBAL=") || strings.HasPrefix(e, "GIT_CONFIG_NOSYSTEM=") || strings.HasPrefix(e, "GIT_CONFIG_SYSTEM=") {
			env = append(env, e)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0")
}

func (g authorGit) command(ctx context.Context, args ...string) *exec.Cmd {
	argv := []string{"-C", g.root, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull}
	cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
	cmd.Env = authorGitEnvironment()
	configureDynamicProcess(cmd) // Shared process cleanup primitive; no test runner is invoked.
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
func (g authorGit) output(ctx context.Context, limit int, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, time.Duration(g.limits.ChildTimeoutMillis)*time.Millisecond)
	defer cancel()
	cmd := g.command(child, args...)
	out := &limitedBuffer{limit: limit + 1}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.Len() > limit {
		return nil, errors.New("Git output exceeds byte limit")
	}
	return out.Bytes(), nil
}
func (g authorGit) batch(ctx context.Context) (*gitBatchReader, context.CancelFunc, error) {
	child, cancel := context.WithTimeout(ctx, time.Duration(g.limits.ChildTimeoutMillis)*time.Millisecond)
	cmd := g.command(child, "cat-file", "--batch-command")
	cmd.Stderr = &limitedBuffer{limit: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, nil, err
	}
	return &gitBatchReader{cmd: cmd, stdin: in, writer: bufio.NewWriter(in), reader: bufio.NewReader(out), stderr: &bytes.Buffer{}, info: map[string]gitObjectInfo{}}, cancel, nil
}

func validAuthorOID(s, format string) bool {
	n := 40
	if format == "sha256" {
		n = 64
	}
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validateAuthorIdentityFields(name, email string) (identityInfo, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || len(name)+len(email)+1 > 1024 || strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.IndexFunc(email, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return identityInfo{}, errors.New("identity requires non-empty name/email without control characters or email whitespace")
	}
	return identityInfo{Name: name, Email: email, Available: true}, nil
}

func authorIdentityValue(s string) (identityInfo, error) {
	p := strings.SplitN(s, "=", 2)
	if len(p) == 2 {
		if value, err := validateAuthorIdentityFields(p[0], p[1]); err == nil {
			return value, nil
		}
	}
	return identityInfo{}, errors.New("--selected/--identity requires NAME=EMAIL without control characters")
}
func parseAuthorGitIdentity(s string) (identityInfo, error) {
	left, right := strings.LastIndex(s, " <"), strings.LastIndex(s, ">")
	if left < 1 || right <= left+2 || !utf8.ValidString(s) {
		return identityInfo{}, errors.New("malformed identity")
	}
	stamp := strings.Fields(s[right+1:])
	if len(stamp) != 2 {
		return identityInfo{}, errors.New("malformed identity timestamp")
	}
	if _, err := strconv.ParseInt(stamp[0], 10, 64); err != nil {
		return identityInfo{}, err
	}
	if len(stamp[1]) != 5 || (stamp[1][0] != '+' && stamp[1][0] != '-') {
		return identityInfo{}, errors.New("malformed identity timezone")
	}
	if _, err := strconv.Atoi(stamp[1][1:]); err != nil {
		return identityInfo{}, err
	}
	return validateAuthorIdentityFields(s[:left], s[left+2:right])
}

func parseAuthorCommit(oid string, data []byte, format string, selected identityInfo, identities maps, maxMessage int) (authorCommitRecord, error) {
	r := authorCommitRecord{OID: oid, Parents: []string{}, Coauthors: []authorRecordedIdentity{}, ObjectSHA256: authorDigest(data), CoauthorStatus: "complete", MatchBasis: "none", UnavailableFields: map[string]unavailable{}, UnavailableIdentityHeaders: map[string][]authorText{}}
	parts := bytes.SplitN(data, []byte("\n\n"), 2)
	if len(parts) != 2 {
		return r, errors.New("malformed commit metadata")
	}
	headers := map[string][]string{}
	treeSeen := false
	for _, line := range strings.Split(string(parts[0]), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		switch key {
		case "tree":
			if treeSeen || !validAuthorOID(value, format) {
				return r, errors.New("malformed tree")
			}
			treeSeen = true
		case "parent":
			if !validAuthorOID(value, format) {
				return r, errors.New("malformed parent")
			}
			r.Parents = append(r.Parents, value)
		case "author", "committer":
			headers[key] = append(headers[key], value)
		}
	}
	if !treeSeen {
		return r, errors.New("missing commit tree")
	}
	// Identity fields are independent: a bad committer must not erase a known
	// author, and a bad author must not erase a provable selected coauthor.
	parseIdentityHeader := func(key string) authorRecordedIdentity {
		values := headers[key]
		reason := "missing_identity"
		if len(values) == 1 {
			if value, err := parseAuthorGitIdentity(values[0]); err == nil {
				return authorRecordedIdentity{value, resolve(value, identities)}
			}
			reason = "malformed_identity"
		} else if len(values) > 1 {
			reason = "duplicate_identity"
		}
		r.UnavailableFields[key] = unavailable{"unavailable", reason}
		r.UnavailableIdentityHeaders[key] = []authorText{}
		for _, value := range values {
			r.UnavailableIdentityHeaders[key] = append(r.UnavailableIdentityHeaders[key], encodeAuthorText([]byte(value)))
		}
		return authorRecordedIdentity{}
	}
	r.Author = parseIdentityHeader("author")
	r.Committer = parseIdentityHeader("committer")
	if r.Author.Canonical.Available && idKey(r.Author.Canonical) == idKey(selected) {
		r.MatchBasis = "authored"
	}
	if len(parts[1]) > maxMessage {
		r.CoauthorStatus = "unavailable"
		r.Message = authorText{Encoding: "not_retained", SHA256: authorDigest(parts[1])}
		r.UnavailableFields["message"] = unavailable{"unavailable", "message_byte_limit"}
		r.UnavailableFields["coauthors"] = unavailable{"unavailable", "message_byte_limit"}
		return r, errors.New("message byte limit")
	}
	r.Message = encodeAuthorText(parts[1])
	subject, body, _ := bytes.Cut(parts[1], []byte("\n"))
	r.Subject = encodeAuthorText(subject)
	r.Body = encodeAuthorText(body)
	coMatch, malformed := false, false
	coSeen := map[string]bool{}
	for _, line := range strings.Split(string(parts[1]), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "co-authored-by:") {
			continue
		}
		value := strings.TrimSpace(line[len("co-authored-by:"):])
		l, z := strings.LastIndex(value, "<"), strings.LastIndex(value, ">")
		if l < 1 || z <= l+1 || strings.TrimSpace(value[z+1:]) != "" {
			malformed = true
			continue
		}
		i, err := validateAuthorIdentityFields(strings.TrimSpace(value[:l]), value[l+1:z])
		if err != nil || !utf8.ValidString(value) {
			malformed = true
			continue
		}
		if coSeen[idKey(i)] {
			continue
		}
		coSeen[idKey(i)] = true
		canonical := resolve(i, identities)
		r.Coauthors = append(r.Coauthors, authorRecordedIdentity{i, canonical})
		if idKey(canonical) == idKey(selected) {
			coMatch = true
		}
	}
	if coMatch {
		if r.MatchBasis == "authored" {
			r.MatchBasis = "both"
		} else {
			r.MatchBasis = "coauthored"
		}
	}
	if malformed {
		r.CoauthorStatus = "partial"
		r.UnavailableFields["coauthors"] = unavailable{"unavailable", "malformed_coauthor_trailer"}
	}
	if len(r.UnavailableFields) > 0 {
		return r, errors.New("commit metadata has unavailable fields")
	}
	return r, nil
}

func collectAuthorMetadata(ctx context.Context, g authorGit, scope authorScope, selected authorRecordedIdentity, config authorIdentityConfiguration, identities maps) (r authorEvidenceReport) {
	r = authorEvidenceReport{ContractVersion: "brag-author-evidence-v1", MethodVersion: authorMetadataMethod, ToolVersion: "v0.1.0-rc.1", Status: "partial", Execution: "not_run", Scope: scope, Selected: selected, IdentityConfiguration: config, InclusionPolicy: "authored_or_coauthored_once", Records: []authorCommitRecord{}, Limits: g.limits, Omissions: map[string]int{}, Unavailable: map[string]unavailable{}}
	defer func() {
		// Count locally enumerated commits lacking complete inspection, including
		// storage failures on early returns; error reasons are not extra commits.
		r.Metadata.MetadataOmittedCommits = r.Metadata.EnumeratedCommits - r.Metadata.InspectedCommits
	}()
	r.ToolBuild = currentAuthorBuildInfo()
	r.Target.Kind = "HEAD"
	r.Target.Commit = scope.Head
	for _, k := range []string{"source_changes", "test_changes", "current_test_lineage", "helper_context", "authority", "model_analysis"} {
		r.Unavailable[k] = unavailable{"unavailable", "not_collected"}
	}
	r.Metadata.Status = "complete"
	r.Metadata.TraversalOrder = "git rev-list --topo-order --reverse"
	r.Metadata.RetentionPolicy = "last selected whole records in traversal order within count/serialized-byte limits"
	complete := true
	retainedBytes := 0
	recordSizes := []int{}
	digest := sha256.New()
	omit := func(reason string) { complete = false; r.Metadata.Status = "partial"; r.Omissions[reason]++ }
	if scope.Shallow {
		omit("shallow_boundary")
	}
	retain := func(record authorCommitRecord) {
		if record.MatchBasis == "none" {
			return
		}
		r.Metadata.ObservedSelectedCommits++
		r.Metadata.LastObservedSelectedCommit = record.OID
		b, _ := json.Marshal(record)
		n := len(b)
		if g.limits.RetainedCommits <= 0 || n > g.limits.RetainedBytes {
			return
		}
		for len(r.Records) > 0 && (len(r.Records) >= g.limits.RetainedCommits || retainedBytes+n > g.limits.RetainedBytes) {
			retainedBytes -= recordSizes[0]
			recordSizes = recordSizes[1:]
			r.Records[0] = authorCommitRecord{}
			r.Records = r.Records[1:]
		}
		r.Records = append(r.Records, record)
		recordSizes = append(recordSizes, n)
		retainedBytes += n
	}
	process := func(ids []string) {
		if len(ids) == 0 {
			return
		}
		if ctx.Err() != nil || r.Metadata.MetadataBytes >= g.limits.MetadataBytes {
			omit("metadata_budget_or_deadline")
			return
		}
		reader, cancel, err := g.batch(ctx)
		if err != nil {
			omit("metadata_reader_unavailable")
			return
		}
		defer cancel()
		for _, oid := range ids {
			kind, size, err := reader.Info(oid)
			if err != nil || kind != "commit" {
				omit("commit_metadata_unreadable")
				continue
			}
			if size > g.limits.ObjectBytes || size > g.limits.MetadataBytes-r.Metadata.MetadataBytes {
				omit("commit_metadata_byte_limit")
				continue
			}
			_, data, _, err := reader.Read(oid, g.limits.ObjectBytes)
			if err != nil {
				omit("commit_metadata_unreadable")
				continue
			}
			r.Metadata.MetadataBytes += len(data)
			record, err := parseAuthorCommit(oid, data, scope.ObjectFormat, selected.Canonical, identities, g.limits.MessageBytes)
			if err != nil {
				omit("commit_inclusion_metadata_incomplete")
			} else {
				r.Metadata.InspectedCommits++
			}
			// Keep any provable selected match, even if another field is unavailable.
			if record.MatchBasis != "none" && record.Message.Encoding != "" {
				retain(record)
			}
		}
		if err := reader.Close(); err != nil {
			omit("metadata_reader_close_failed")
		}
	}
	// Drain rev-list before processing metadata chunks. A private, byte-bounded
	// ID spool separates Git enumeration time from synchronous consumer work.
	if pathWithin(g.root, os.TempDir()) {
		omit("history_storage_unsafe")
		return r
	}
	ids, err := os.CreateTemp("", "brag-author-ids-")
	if err != nil {
		omit("history_storage_unavailable")
		return r
	}
	defer os.Remove(ids.Name())
	defer ids.Close()
	child, cancel := context.WithTimeout(ctx, time.Duration(g.limits.ChildTimeoutMillis)*time.Millisecond)
	defer cancel()
	revs := scope.Head
	if scope.Base != nil {
		revs = scope.Base.MergeBase + ".." + scope.Head
	}
	cmd := g.command(child, "rev-list", "--topo-order", "--reverse", revs, "--")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		omit("history_unavailable")
		return r
	}
	if err = cmd.Start(); err != nil {
		omit("history_unavailable")
		return r
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256), 256)
	writer := bufio.NewWriter(ids)
	idBytes := 0
	enumerationComplete := true
	for scanner.Scan() {
		oid := scanner.Text()
		if !validAuthorOID(oid, scope.ObjectFormat) {
			enumerationComplete = false
			omit("invalid_history_id")
			break
		}
		if len(oid)+1 > g.limits.HistoryIDBytes-idBytes {
			enumerationComplete = false
			omit("history_id_byte_limit")
			break
		}
		if _, err := fmt.Fprintln(writer, oid); err != nil {
			enumerationComplete = false
			omit("history_storage_write_failed")
			break
		}
		idBytes += len(oid) + 1
		r.Metadata.EnumeratedCommits++
		digest.Write([]byte(oid + "\n"))
		if r.Metadata.FirstEnumeratedCommit == "" {
			r.Metadata.FirstEnumeratedCommit = oid
		}
		r.Metadata.LastEnumeratedCommit = oid
	}
	if scanner.Err() != nil {
		enumerationComplete = false
		omit("history_stream_incomplete")
	}
	if !enumerationComplete {
		cancel()
	}
	if err = cmd.Wait(); err != nil {
		enumerationComplete = false
		omit("history_stream_incomplete")
	}
	cancel()
	if err = writer.Flush(); err != nil {
		enumerationComplete = false
		omit("history_storage_write_failed")
	}
	if _, err = ids.Seek(0, io.SeekStart); err != nil {
		omit("history_storage_read_failed")
		return r
	}
	stored := bufio.NewScanner(ids)
	stored.Buffer(make([]byte, 256), 256)
	chunk := []string{}
	for stored.Scan() {
		oid := stored.Text()
		if !validAuthorOID(oid, scope.ObjectFormat) {
			omit("history_storage_read_failed")
			break
		}
		chunk = append(chunk, oid)
		if len(chunk) >= g.limits.ChunkCommits {
			process(chunk)
			chunk = nil
		}
	}
	if stored.Err() != nil {
		omit("history_storage_read_failed")
	}
	process(chunk)
	r.Metadata.EnumeratedIDsSHA256 = hex.EncodeToString(digest.Sum(nil))
	if enumerationComplete && !scope.Shallow {
		n := r.Metadata.EnumeratedCommits
		r.Metadata.ScopedCommits = &n
	}
	if complete && enumerationComplete {
		n := r.Metadata.ObservedSelectedCommits
		r.Metadata.SelectedCommits = &n
	} else {
		r.Metadata.Status = "partial"
	}
	r.Metadata.RetainedSelectedCommits = len(r.Records)
	r.Metadata.DetailOmittedCommits = r.Metadata.ObservedSelectedCommits - len(r.Records)
	r.Metadata.DetailsTruncated = r.Metadata.DetailOmittedCommits > 0
	return r
}

func runAuthorEvidence(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("brag author-evidence", flag.ContinueOnError)
	fs.SetOutput(out)
	all := fs.Bool("all", false, "all commits reachable from pinned HEAD")
	base := fs.String("base", "", "comparison ref; otherwise read git config brag.base")
	who := fs.String("selected", "", "required explicit NAME=EMAIL")
	configArg := fs.String("config", "", "identity configuration file")
	var ids, aliases values
	fs.Var(&ids, "identity", "canonical NAME=EMAIL (repeatable)")
	fs.Var(&aliases, "alias", "EMAIL=CANONICAL_EMAIL (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: brag author-evidence --selected NAME=EMAIL [--all | --base REF] [--config FILE] [--identity NAME=EMAIL] [--alias EMAIL=CANONICAL_EMAIL]")
		fmt.Fprintln(out, "Without --all/--base, configure brag.base. No upstream/main/master inference. Target HEAD; commit metadata only, tests/source evidence not collected.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected author-evidence arguments")
	}
	baseSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "base" {
			baseSet = true
		}
	})
	if *all && baseSet {
		return errors.New("--all and --base are mutually exclusive")
	}
	if *who == "" {
		return errors.New("--selected NAME=EMAIL is required for author-evidence")
	}
	raw, err := authorIdentityValue(*who)
	if err != nil {
		return err
	}
	limits := defaultAuthorMetadataLimits()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(limits.TotalTimeoutMillis)*time.Millisecond)
	defer cancel()
	g := authorGit{root: ".", limits: limits}
	b, err := g.output(ctx, 1<<20, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("author-evidence requires a Git repository")
	}
	g.root = strings.TrimSpace(string(b))
	head, err := g.output(ctx, 128, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return errors.New("author-evidence requires a committed HEAD")
	}
	format, err := g.output(ctx, 32, "rev-parse", "--show-object-format")
	if err != nil {
		return errors.New("cannot determine Git object format")
	}
	scope := authorScope{Head: strings.TrimSpace(string(head)), ObjectFormat: strings.TrimSpace(string(format)), Kind: "head_reachable", SelectionSource: "--all", Boundary: "HEAD and commits reachable from HEAD"}
	if (scope.ObjectFormat != "sha1" && scope.ObjectFormat != "sha256") || !validAuthorOID(scope.Head, scope.ObjectFormat) {
		return errors.New("unsupported Git object format/HEAD")
	}
	shallow, err := g.output(ctx, 16, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return errors.New("cannot establish shallow-history status")
	}
	switch strings.TrimSpace(string(shallow)) {
	case "true":
		scope.Shallow = true
	case "false":
	default:
		return errors.New("invalid shallow-history status")
	}
	if !*all {
		source, origin := "--base", ""
		if !baseSet {
			value, e := g.output(ctx, 4096, "config", "--show-origin", "--get-all", "brag.base")
			if e != nil {
				return errors.New("select a comparison boundary: pass --base REF or configure brag.base; use --all for historical author scope")
			}
			lines := strings.Split(strings.TrimSuffix(string(value), "\n"), "\n")
			if len(lines) != 1 {
				return errors.New("multiple brag.base values; pass one explicit --base REF")
			}
			var ok bool
			origin, *base, ok = strings.Cut(lines[0], "\t")
			if !ok {
				return errors.New("invalid brag.base configuration; pass --base REF")
			}
			source = "git_config:brag.base"
		}
		if *base == "" || strings.HasPrefix(*base, "-") || strings.IndexFunc(*base, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return errors.New("invalid base; pass --base REF or correct brag.base")
		}
		if _, e := g.output(ctx, 64, "check-ref-format", "--allow-onelevel", *base); e != nil {
			return errors.New("invalid base; pass --base REF or correct brag.base")
		}
		pinned, e := g.output(ctx, 128, "rev-parse", "--verify", "--end-of-options", *base+"^{commit}")
		if e != nil {
			return errors.New("cannot resolve base; pass --base REF naming an available local commit")
		}
		refCommit := strings.TrimSpace(string(pinned))
		if !validAuthorOID(refCommit, scope.ObjectFormat) {
			return errors.New("invalid resolved base object ID")
		}
		merge, e := g.output(ctx, 256, "merge-base", "--all", scope.Head, refCommit)
		if e != nil {
			return errors.New("base has no merge-base with HEAD or cannot be read; pass a related --base REF")
		}
		bases := strings.Fields(string(merge))
		if len(bases) != 1 {
			return errors.New("multiple merge-bases are unresolved; select a different explicit --base REF")
		}
		if !validAuthorOID(bases[0], scope.ObjectFormat) {
			return errors.New("invalid merge-base object ID")
		}
		scope.Kind = "branch_range"
		scope.SelectionSource = source
		scope.ConfigurationOrigin = origin
		scope.Base = &baseInfo{Ref: *base, Commit: refCommit, MergeBase: bases[0], Provenance: source}
		scope.Boundary = "merge-base..HEAD; comparison boundary, not proven branch creation point"
	}
	config := *configArg
	enabled := config != ""
	configSource := "none"
	if config == "" {
		config = filepath.Join(g.root, ".brag.json")
		_, e := g.output(ctx, 256, "ls-files", "--error-unmatch", ".brag.json")
		enabled = e == nil
		if enabled {
			configSource = "tracked .brag.json"
		}
	} else {
		configSource = "explicit --config"
	}
	identities, err := readAuthorIdentityMaps(g.root, config, ids, aliases, enabled)
	if err != nil {
		return err
	}
	canonical := resolve(raw, identities)
	if _, err := validateAuthorIdentityFields(canonical.Name, canonical.Email); err != nil {
		return errors.New("configured selected identity is invalid; correct the identity/alias mapping")
	}
	captured := authorIdentityConfiguration{Source: configSource, IdentityOverrides: append([]string{}, ids...), AliasOverrides: append([]string{}, aliases...), Identities: identities.exact, Aliases: identities.aliases}
	data, _ := json.Marshal(captured)
	captured.SHA256 = authorDigest(data)
	r := collectAuthorMetadata(ctx, g, scope, authorRecordedIdentity{raw, canonical}, captured, identities)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
