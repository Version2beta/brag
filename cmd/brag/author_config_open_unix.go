//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func openAuthorConfig(path string) (*os.File, error) {
	// A swapped final symlink is refused; a swapped FIFO cannot block the open.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
