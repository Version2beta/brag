//go:build !darwin && !linux

package main

import "os"

func openAuthorConfig(path string) (*os.File, error) {
	// The caller checks regular-file identity before and after reading. This
	// platform fallback does not promise race-proof or nonblocking file opens.
	return os.Open(path)
}
