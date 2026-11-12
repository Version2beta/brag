//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAuthorEvidenceSpoolFlushFailureAccountsForEnumeratedMetadata(t *testing.T) {
	root, _ := mkRepo(t)
	g, scope, selected, identities := authorMetadataTestScope(t, root, defaultAuthorMetadataLimits())
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	// Exercise the real final buffered write without a production injection seam.
	// This process-local soft limit and signal subscription are restored; tests
	// using the shared CLI are serial and fixture setup precedes the limit.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGXFSZ)
	defer signal.Stop(signals)
	limited := original
	limited.Cur = 0
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("restore file-size limit: %v", err)
		}
	}()
	r := collectAuthorMetadata(context.Background(), g, scope, selected, authorIdentityConfiguration{}, identities)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if r.Metadata.Status != "partial" || r.Metadata.ScopedCommits != nil || r.Metadata.SelectedCommits != nil || r.Omissions["history_storage_write_failed"] != 1 {
		t.Fatalf("expected final spool flush failure: %+v omissions=%v", r.Metadata, r.Omissions)
	}
	if r.Metadata.EnumeratedCommits != 1 || r.Metadata.InspectedCommits != 0 || r.Metadata.MetadataOmittedCommits != 1 {
		t.Fatalf("enumerated metadata missing from omission denominator: %+v", r.Metadata)
	}
}

func TestAuthorEvidenceHistoryDeadlineExcludesChunkProcessing(t *testing.T) {
	root := t.TempDir()
	gitCmd(t, root, "init", "-q")
	gitCmd(t, root, "config", "gc.auto", "0")
	var input bytes.Buffer
	const commits = 2500
	for i := 1; i <= commits; i++ {
		fmt.Fprintf(&input, "commit refs/heads/main\nmark :%d\nauthor Ada <ada@example.test> %d +0000\ncommitter Ada <ada@example.test> %d +0000\ndata 1\nx\n", i, i, i)
		if i > 1 {
			fmt.Fprintf(&input, "from :%d\n", i-1)
		}
		fmt.Fprintln(&input)
	}
	cmd := exec.Command("git", "-C", root, "fast-import", "--quiet")
	cmd.Env = env(root)
	cmd.Stdin = &input
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v %s", err, out)
	}
	gitCmd(t, root, "symbolic-ref", "HEAD", "refs/heads/main")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *\"cat-file --batch-command\"*) sleep 0.2;; esac\nexec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	limits := defaultAuthorMetadataLimits()
	limits.ChildTimeoutMillis = 500
	g, scope, selected, identities := authorMetadataTestScope(t, root, limits)
	r := collectAuthorMetadata(context.Background(), g, scope, selected, authorIdentityConfiguration{}, identities)
	if r.Metadata.Status != "complete" || r.Metadata.SelectedCommits == nil || *r.Metadata.SelectedCommits != commits {
		t.Fatalf("metadata chunk processing consumed history child deadline: %+v omissions=%v", r.Metadata, r.Omissions)
	}
}
