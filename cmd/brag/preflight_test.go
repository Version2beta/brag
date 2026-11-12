package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionRootPreflightRejectsBeforeEffects(t *testing.T) {
	modes := []struct {
		name      string
		args      []string
		displayed string
	}{
		{name: "dynamic", args: []string{"--dynamic-tests"}, displayed: "dynamic-tests"},
		{name: "mutation", args: []string{"--mutation-tests", "--test-contributions", "--mutation-limit", "1"}, displayed: "mutation-tests+test-contributions"},
	}
	expectations := []struct {
		name, root, message string
	}{
		{name: "missing", message: "--expected-root PATH is required"},
		{name: "mismatch", message: "execution root mismatch"},
	}
	for _, mode := range modes {
		for _, expectation := range expectations {
			t.Run(mode.name+"/"+expectation.name, func(t *testing.T) {
				root, _ := mkRepo(t)
				marker := filepath.Join(t.TempDir(), "executed")
				fixture := fmt.Sprintf("package app\nimport (\"os\"; \"testing\")\nfunc TestMarker(t *testing.T) { if err := os.WriteFile(%q, []byte(\"ran\"), 0600); err != nil { t.Fatal(err) } }\n", marker)
				if err := os.WriteFile(filepath.Join(root, "app_test.go"), []byte(fixture), 0600); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, root, "add", "app_test.go")
				gitCmd(t, root, "commit", "-qm", "add marker test")
				artifacts := filepath.Join(t.TempDir(), "not-created")
				args := []string{"all"}
				args = append(args, mode.args...)
				args = append(args, "--dynamic-artifacts", artifacts)
				if expectation.name == "mismatch" {
					args = append(args, "--expected-root", t.TempDir())
				}
				cmd := exec.Command(binary, args...)
				cmd.Dir = root
				cmd.Env = env(root)
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatal("preflight unexpectedly accepted execution")
				}
				text := string(output)
				canonicalRoot, canonicalErr := filepath.EvalSymlinks(root)
				if canonicalErr != nil {
					t.Fatal(canonicalErr)
				}
				if !strings.Contains(text, "execution preflight: root="+canonicalRoot+" mode="+mode.displayed) {
					t.Fatalf("missing preflight output: %s", text)
				}
				if !strings.Contains(text, expectation.message) {
					t.Fatalf("missing rejection %q: %s", expectation.message, text)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("fixture executed before rejection")
				}
				if _, err := os.Stat(artifacts); !os.IsNotExist(err) {
					t.Fatal("artifact directory created before rejection")
				}
			})
		}
	}
}

func TestStaticCommandDoesNotRequireExpectedRoot(t *testing.T) {
	root, _ := mkRepo(t)
	cmd := exec.Command(binary, "all")
	cmd.Dir = root
	cmd.Env = env(root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("static command failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "execution preflight") {
		t.Fatalf("static command printed preflight: %s", output)
	}
}
