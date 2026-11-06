//go:build !darwin && !linux

package main

import "os/exec"

func supportsProcessGroupCleanup() bool     { return false }
func configureDynamicProcess(cmd *exec.Cmd) {}
