package main

import (
	"fmt"
	"io"
)

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func printMainHelp(w io.Writer) {
	fmt.Fprintln(w, "BRAG — evidence-backed summaries of bounded Git history and static repository measures")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  brag [options]                 Analyze branch commits against --base or configured upstream")
	fmt.Fprintln(w, "  brag all [options]             Analyze commits reachable from HEAD; no net-change baseline")
	fmt.Fprintln(w, "  brag skill install [options]   Install the companion Agent Skill")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common options:")
	fmt.Fprintln(w, "  --base REF                     Branch comparison boundary (required without an upstream)")
	fmt.Fprintln(w, "  --config FILE                  Read identity/alias configuration")
	fmt.Fprintln(w, "  --selected NAME=EMAIL          Select one contributor explicitly")
	fmt.Fprintln(w, "  --identity NAME=EMAIL          Add a canonical identity (repeatable)")
	fmt.Fprintln(w, "  --alias EMAIL=CANONICAL_EMAIL  Add an identity alias (repeatable)")
	fmt.Fprintln(w, "  -h, --help                     Show help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Default analysis is static, read-only, and offline. JSON is written to stdout. Tests are inventoried, not run.")
	fmt.Fprintln(w, "Optional execution modes (Go tests run unsandboxed with host privileges):")
	fmt.Fprintln(w, "  --expected-root PATH              Required with dynamic or mutation execution; guards against accidental targets")
	fmt.Fprintln(w, "  --dynamic-tests [--dynamic-repeat 1..3] [--dynamic-timeout D] [--dynamic-total-timeout D] [--dynamic-artifacts DIR]")
	fmt.Fprintln(w, "  --mutation-tests [--mutation-limit 1..50] [--mutation-package PATH] [--mutation-path PATH] [--mutation-snapshot NAME]")
	fmt.Fprintln(w, "  --test-contributions             Requires --mutation-tests; experimental, bounded execution")
	fmt.Fprintln(w, "  --contribution-repeat 1..3       Repeats for test-contribution evaluations")
	fmt.Fprintln(w, "  --contribution-timeout D         Per-evaluation limit (maximum 5m)")
	fmt.Fprintln(w, "  --contribution-total-timeout D   Total limit (maximum 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  brag --base origin/main")
	fmt.Fprintln(w, "  brag all --selected 'Name=EMAIL'")
	fmt.Fprintln(w, "  brag skill install")
	fmt.Fprintln(w, "  brag skill install --project --agent all")
}

func printAllHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: brag all [options]")
	fmt.Fprintln(w, "Analyze commits reachable from HEAD across the repository's recorded history.")
	fmt.Fprintln(w, "This scope has no branch comparison base or net-change baseline.")
	fmt.Fprintln(w, "Default analysis is static and does not run repository tests. Use 'brag --help' for options and execution warnings.")
}
