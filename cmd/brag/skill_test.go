package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runWithEnv(t *testing.T, dir string, overrides []string, args ...string) (string, error) {
	t.Helper()
	keys := make(map[string]bool, len(overrides))
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		keys[key] = true
	}
	childEnv := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			if keys[key] {
				continue
			}
		}
		childEnv = append(childEnv, item)
	}
	childEnv = append(childEnv, overrides...)
	c := exec.Command(binary, args...)
	c.Dir = dir
	c.Env = childEnv
	b, err := c.CombinedOutput()
	return string(b), err
}

func homeOverrides(home string) []string {
	values := []string{"HOME=" + home}
	if runtime.GOOS == "windows" {
		values = append(values, "USERPROFILE="+home)
	}
	return values
}

func TestEmbeddedSkillHasPortableAgentSkillsMetadata(t *testing.T) {
	content := string(skillBytes)
	if !strings.HasPrefix(content, "---\n") || !strings.Contains(content, "\nname: brag\n") || !strings.Contains(content, "\ndescription:") {
		t.Fatalf("embedded skill lacks required Agent Skills frontmatter")
	}
	if strings.Contains(content, "/brag all") || strings.Contains(content, "pi.getActiveTools") {
		t.Fatalf("embedded skill contains Pi-specific integration instructions")
	}
}

func TestHelpWorksOutsideGitAndAfterOptions(t *testing.T) {
	cases := []struct {
		args  []string
		match string
	}{
		{[]string{"--help"}, "Usage:"},
		{[]string{"-h"}, "Common options:"},
		{[]string{"--base", "main", "--help"}, "JSON is written to stdout"},
		{[]string{"all", "--help"}, "no branch comparison base"},
		{[]string{"all", "--base", "main", "-h"}, "no branch comparison base"},
		{[]string{"skill", "--help"}, "Usage: brag skill"},
		{[]string{"skill", "install", "--help"}, "$HOME/.agents/skills/brag"},
		{[]string{"skill", "install", "--project", "--help"}, "--project"},
	}
	for _, tc := range cases {
		out, err := runWithEnv(t, t.TempDir(), nil, tc.args...)
		if err != nil || !strings.Contains(out, tc.match) {
			t.Fatalf("%v: err=%v output=%q, want %q", tc.args, err, out, tc.match)
		}
	}
}

func TestGlobalSkillInstallTargetsIdempotenceAndForce(t *testing.T) {
	home := t.TempDir()
	output, err := runWithEnv(t, t.TempDir(), homeOverrides(home), "skill", "install", "--agent", "all")
	if err != nil {
		t.Fatalf("install all: %v %s", err, output)
	}
	want := []string{
		".agents/skills/brag/SKILL.md",
		".claude/skills/brag/SKILL.md",
		".pi/agent/skills/brag/SKILL.md",
		".config/opencode/skills/brag/SKILL.md",
		".cursor/skills/brag/SKILL.md",
		".copilot/skills/brag/SKILL.md",
	}
	for _, rel := range want {
		data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil || string(data) != string(skillBytes) {
			t.Fatalf("skill missing at %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "skills", "brag", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("--agent all should use the shared .agents path for Gemini: %v", err)
	}
	output, err = runWithEnv(t, t.TempDir(), homeOverrides(home), "skill", "install", "--agent", "codex")
	if err != nil || !strings.Contains(output, "already installed:") {
		t.Fatalf("idempotent install: %v %s", err, output)
	}

	shared := filepath.Join(home, ".agents", "skills", "brag", "SKILL.md")
	if err := os.WriteFile(shared, []byte("user skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err = runWithEnv(t, t.TempDir(), homeOverrides(home), "skill", "install"); err == nil || !strings.Contains(output, "--force") {
		t.Fatalf("conflicting skill was overwritten: %v %s", err, output)
	}
	data, err := os.ReadFile(shared)
	if err != nil || string(data) != "user skill" {
		t.Fatalf("conflicting user file changed: %v", err)
	}
	if _, err = runWithEnv(t, t.TempDir(), homeOverrides(home), "skill", "install", "--force"); err != nil {
		t.Fatalf("force install: %v", err)
	}
	data, err = os.ReadFile(shared)
	if err != nil || string(data) != string(skillBytes) {
		t.Fatalf("force install did not replace skill: %v", err)
	}
}

func TestProjectSkillInstallUsesDocumentedHostPaths(t *testing.T) {
	outside := t.TempDir()
	if _, err := runWithEnv(t, outside, nil, "skill", "install", "--project"); err == nil {
		t.Fatal("project install succeeded outside Git")
	}
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-q")
	out, err := runWithEnv(t, repo, homeOverrides(t.TempDir()), "skill", "install", "--project", "--agent", "all")
	if err != nil || !strings.Contains(out, "installed:") {
		t.Fatalf("project all install: %v %s", err, out)
	}
	for _, rel := range []string{".agents/skills/brag/SKILL.md", ".claude/skills/brag/SKILL.md", ".pi/skills/brag/SKILL.md"} {
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil || string(data) != string(skillBytes) {
			t.Fatalf("project skill missing at %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".opencode", "skills", "brag", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("project --agent all should reuse .claude for OpenCode discovery: %v", err)
	}
	for _, target := range []struct{ name, rel string }{
		{"pi", ".pi/skills/brag/SKILL.md"},
		{"opencode", ".opencode/skills/brag/SKILL.md"},
		{"copilot", ".github/skills/brag/SKILL.md"},
		{"gemini", ".gemini/skills/brag/SKILL.md"},
		{"cursor", ".cursor/skills/brag/SKILL.md"},
	} {
		out, err := runWithEnv(t, repo, homeOverrides(t.TempDir()), "skill", "install", "--project", "--agent", target.name)
		if err != nil || !strings.Contains(out, "installed:") {
			t.Fatalf("target %s: %v %s", target.name, err, out)
		}
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(target.rel)))
		if err != nil || string(data) != string(skillBytes) {
			t.Fatalf("target %s installed at wrong path: %v", target.name, err)
		}
	}
}

func TestSkillInstallRejectsSymlinkAndPreservesTarget(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "brag"), 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".agents", "skills", "brag", "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if output, err := runWithEnv(t, t.TempDir(), homeOverrides(home), "skill", "install"); err == nil || !strings.Contains(output, "refusing non-regular") {
		t.Fatalf("symlink skill target was accepted: %v %s", err, output)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("symlink target changed: %v", err)
	}
}
