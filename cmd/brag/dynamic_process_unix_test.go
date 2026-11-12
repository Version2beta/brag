//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSignalCancellationReportsAndExits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping signal cancellation test-suite integration in short mode")
	}
	root, _ := mkRepo(t)
	ready := filepath.Join(t.TempDir(), "ready")
	source := fmt.Sprintf(`package app
import ("os"; "testing"; "time")
func TestWait(t *testing.T) {
 if err := os.WriteFile(%q, []byte("ready"), 0600); err != nil { t.Fatal(err) }
 time.Sleep(10*time.Second)
}
`, ready)
	if err := os.WriteFile(filepath.Join(root, "app_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "add", "app_test.go")
	artifacts := privateTempDir(t)
	cmd := exec.Command(binary, "all", "--dynamic-tests", "--expected-root", root, "--dynamic-artifacts", artifacts)
	cmd.Dir = root
	cmd.Env = env(root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect signal-test marker: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("dynamic CLI test did not start before deadline")
		case <-time.After(25 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	waited = true
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("signal cancellation exit=%v stderr=%s", err, stderr.String())
	}
	var report struct {
		SchemaVersion int                    `json:"schema_version"`
		Dynamic       *dynamicEvidence       `json:"dynamic_tests"`
		Unavailable   map[string]unavailable `json:"unavailable"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.SchemaVersion != 3 || report.Dynamic == nil || report.Dynamic.Status != "cancelled" || report.Unavailable["test_coverage"].Status != "unavailable" {
		t.Fatalf("signal cancellation report missing or inconsistent: err=%v report=%+v stdout=%s stderr=%s", err, report, stdout.String(), stderr.String())
	}
}

func TestDynamicCancellationKillsDescendantProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping descendant-process test-suite integration in short mode")
	}
	root, _ := mkRepo(t)
	ready := filepath.Join(t.TempDir(), "ready")
	survived := filepath.Join(t.TempDir(), "survived")
	source := fmt.Sprintf(`package app
import ("os"; "os/exec"; "testing"; "time")
func TestSpawn(t *testing.T) {
 child := exec.Command("sh", "-c", "sleep 2; printf survived > \"$1\"", "sh", %q)
 if err := child.Start(); err != nil { t.Fatal(err) }
 if err := os.WriteFile(%q, []byte("ready"), 0600); err != nil { t.Fatal(err) }
 time.Sleep(10*time.Second)
}
`, survived, ready)
	if err := os.WriteFile(filepath.Join(root, "app_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "add", "app_test.go")
	head := gitCmd(t, root, "rev-parse", "HEAD")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	artifacts := privateTempDir(t)
	done := make(chan *dynamicEvidence, 1)
	go func() {
		done <- runDynamicTests(ctx, "all", head, nil, root, 1, 25*time.Second, 30*time.Second, artifacts)
	}()

	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			cancel()
			<-done
			t.Fatalf("inspect ready marker: %v", err)
		}
		select {
		case result := <-done:
			t.Fatalf("dynamic run finished before test process was ready: %+v", result)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("dynamic test did not start before deadline")
		case <-time.After(25 * time.Millisecond):
		}
	}

	cancel()
	var result *dynamicEvidence
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled dynamic run did not return promptly")
	}
	if result.Status != "cancelled" {
		t.Fatalf("caller cancellation was not reported: %+v", result)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(survived); err == nil {
		t.Fatal("descendant process survived cancellation")
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect descendant marker: %v", err)
	}
}
