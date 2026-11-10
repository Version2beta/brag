package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var skillBytes []byte

type skillLocation struct {
	project string
	user    string
}

var skillTargets = map[string]skillLocation{
	"agents":   {project: ".agents/skills/brag", user: ".agents/skills/brag"},
	"codex":    {project: ".agents/skills/brag", user: ".agents/skills/brag"},
	"claude":   {project: ".claude/skills/brag", user: ".claude/skills/brag"},
	"pi":       {project: ".pi/skills/brag", user: ".pi/agent/skills/brag"},
	"gemini":   {project: ".gemini/skills/brag", user: ".gemini/skills/brag"},
	"opencode": {project: ".opencode/skills/brag", user: ".config/opencode/skills/brag"},
	"cursor":   {project: ".cursor/skills/brag", user: ".cursor/skills/brag"},
	"copilot":  {project: ".github/skills/brag", user: ".copilot/skills/brag"},
}

func runSkill(args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printSkillHelp(os.Stdout)
		return nil
	}
	if args[0] != "install" {
		if hasHelpFlag(args) {
			printSkillHelp(os.Stdout)
			return nil
		}
		return fmt.Errorf("unknown skill command %q", args[0])
	}
	if hasHelpFlag(args[1:]) {
		printSkillInstallHelp(os.Stdout)
		return nil
	}
	fs := flag.NewFlagSet("brag skill install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printSkillInstallHelp(fs.Output()) }
	project := fs.Bool("project", false, "install under the current Git root")
	force := fs.Bool("force", false, "replace a different existing SKILL.md")
	var agents values
	fs.Var(&agents, "agent", "target host (repeatable): agents/codex, claude, pi, gemini, opencode, cursor, copilot, all")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	selected := agents
	if len(selected) == 0 {
		selected = []string{"agents"}
	}
	allRequested := false
	for _, agent := range selected {
		if agent == "all" {
			allRequested = true
		}
	}
	if allRequested && len(selected) != 1 {
		return errors.New("--agent all cannot be combined with other --agent values")
	}

	base := ""
	if *project {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Env = gitEnvironment(os.Environ())
		out, err := cmd.Output()
		if err != nil {
			return errors.New("--project requires a Git repository")
		}
		base = strings.TrimSpace(string(out))
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find user home directory: %w", err)
		}
		if !filepath.IsAbs(home) {
			return errors.New("user home directory must be absolute")
		}
		base = home
	}

	names := selected
	if allRequested {
		// .agents is shared by Codex/Gemini and, for project installs, Cursor/Copilot.
		// Project OpenCode also discovers .claude; global OpenCode has its own path.
		if *project {
			names = []string{"agents", "claude", "pi"}
		} else {
			names = []string{"agents", "claude", "pi", "opencode", "cursor", "copilot"}
		}
	}

	destinations := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		location, ok := skillTargets[name]
		if !ok {
			return fmt.Errorf("unknown --agent %q", name)
		}
		rel := location.user
		if *project {
			rel = location.project
		}
		path := filepath.Clean(filepath.Join(base, filepath.FromSlash(rel)))
		if !filepath.IsAbs(path) {
			return errors.New("skill install destination must be absolute")
		}
		if !seen[path] {
			seen[path] = true
			destinations = append(destinations, path)
		}
	}
	for _, path := range destinations {
		if err := checkSkillDestination(path, *force); err != nil {
			return err
		}
	}
	for _, path := range destinations {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
		target := filepath.Join(path, "SKILL.md")
		old, err := os.ReadFile(target)
		if err == nil && string(old) == string(skillBytes) {
			fmt.Printf("already installed: %s\n", target)
			continue
		}
		if err := os.WriteFile(target, skillBytes, 0644); err != nil {
			return err
		}
		fmt.Printf("installed: %s\n", target)
	}
	return nil
}

func checkSkillDestination(path string, force bool) error {
	info, err := os.Lstat(path)
	if err == nil && !info.IsDir() {
		return fmt.Errorf("destination is not a directory: %s", path)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	target := filepath.Join(path, "SKILL.md")
	info, err = os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular skill target: %s", target)
	}
	old, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if string(old) != string(skillBytes) && !force {
		return fmt.Errorf("different SKILL.md already exists at %s (use --force to replace)", target)
	}
	return nil
}

func printSkillHelp(out io.Writer) {
	fmt.Fprintln(out, "Usage: brag skill <command>")
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  install    Install the harness-agnostic BRAG Agent Skill")
	fmt.Fprintln(out, "  -h, --help Show this help")
	fmt.Fprintln(out, "Run 'brag skill install --help' for install targets and options.")
}

func printSkillInstallHelp(out io.Writer) {
	fmt.Fprintln(out, "Usage: brag skill install [--project] [--agent HOST]... [--force]")
	fmt.Fprintln(out, "Install the embedded BRAG Agent Skill. By default, installs to $HOME/.agents/skills/brag.")
	fmt.Fprintln(out, "--project installs under <git-root>/.agents/skills/brag; --agent selects documented host paths.")
	fmt.Fprintln(out, "Targets: agents/codex, claude, pi, gemini, opencode, cursor, copilot, all. Repeat --agent for explicit targets.")
	fmt.Fprintln(out, "Project --agent all uses .agents for Codex/Gemini/Cursor/Copilot, .claude for Claude/OpenCode, and .pi for Pi.")
	fmt.Fprintln(out, "Global --agent all uses .agents for Codex/Gemini plus native Claude, Pi, OpenCode, Cursor, and Copilot paths.")
	fmt.Fprintln(out, "Host discovery varies by product/version; no single directory is universal.")
	fmt.Fprintln(out, "Existing identical files are unchanged. A different existing SKILL.md is preserved unless --force is supplied.")
	fmt.Fprintln(out, "Installation only copies the skill; it does not run BRAG or contact a model provider.")
}
