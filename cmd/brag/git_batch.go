package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type gitObjectInfo struct {
	kind string
	size int
}

type gitBatchReader struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writer *bufio.Writer
	reader *bufio.Reader
	stderr *bytes.Buffer
	info   map[string]gitObjectInfo
	closed bool
	failed bool
}

func newGitBatchReader(root string) (*gitBatchReader, error) {
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch-command")
	cmd.Env = gitEnvironment(os.Environ())
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &gitBatchReader{cmd: cmd, stdin: stdin, writer: bufio.NewWriter(stdin), reader: bufio.NewReader(stdout), stderr: stderr, info: map[string]gitObjectInfo{}}, nil
}

func newGitBatchReaderForCommit(root, commit string) (*gitBatchReader, error) {
	reader, err := newGitBatchReader(root)
	if err != nil {
		return nil, err
	}
	kind, _, err := reader.Info(commit)
	if err == nil && kind != "commit" {
		err = fmt.Errorf("Git object %s is %s, not a commit", commit, kind)
	}
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

func (r *gitBatchReader) Info(oid string) (string, int, error) {
	if r == nil || r.closed || r.failed {
		return "", 0, fmt.Errorf("Git batch reader is unavailable")
	}
	if oid == "" || strings.ContainsAny(oid, " \t\r\n") {
		return "", 0, fmt.Errorf("invalid Git object ID")
	}
	if info, ok := r.info[oid]; ok {
		return info.kind, info.size, nil
	}
	if _, err := fmt.Fprintf(r.writer, "info %s\n", oid); err != nil {
		r.failed = true
		return "", 0, err
	}
	if err := r.writer.Flush(); err != nil {
		r.failed = true
		return "", 0, err
	}
	header, err := r.reader.ReadString('\n')
	if err != nil {
		r.failed = true
		return "", 0, err
	}
	fields := strings.Fields(strings.TrimSuffix(header, "\n"))
	if len(fields) == 2 && fields[1] == "missing" {
		return "", 0, fmt.Errorf("Git object %s is missing", oid)
	}
	if len(fields) != 3 || fields[0] != oid {
		r.failed = true
		return "", 0, fmt.Errorf("invalid Git object metadata for %s", oid)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || size < 0 {
		r.failed = true
		return "", 0, fmt.Errorf("invalid Git object size for %s", oid)
	}
	info := gitObjectInfo{kind: fields[1], size: size}
	r.info[oid] = info
	return info.kind, info.size, nil
}

// Read returns object metadata even when maxSize is exceeded; content is read only
// after its advertised size has passed the caller's bound.
func (r *gitBatchReader) Read(oid string, maxSize int) (string, []byte, int, error) {
	if maxSize < 0 {
		return "", nil, 0, fmt.Errorf("invalid Git object size limit")
	}
	kind, size, err := r.Info(oid)
	if err != nil {
		return "", nil, 0, err
	}
	if size > maxSize {
		return kind, nil, size, nil
	}
	if _, err := fmt.Fprintf(r.writer, "contents %s\n", oid); err != nil {
		r.failed = true
		return "", nil, size, err
	}
	if err := r.writer.Flush(); err != nil {
		r.failed = true
		return "", nil, size, err
	}
	header, err := r.reader.ReadString('\n')
	if err != nil {
		r.failed = true
		return "", nil, size, err
	}
	fields := strings.Fields(strings.TrimSuffix(header, "\n"))
	if len(fields) != 3 || fields[0] != oid || fields[1] != kind {
		r.failed = true
		return "", nil, size, fmt.Errorf("invalid Git object contents header for %s", oid)
	}
	contentSize, err := strconv.Atoi(fields[2])
	if err != nil || contentSize != size {
		r.failed = true
		return "", nil, size, fmt.Errorf("Git object size changed for %s", oid)
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(r.reader, content); err != nil {
		r.failed = true
		return "", nil, size, err
	}
	separator, err := r.reader.ReadByte()
	if err != nil || separator != '\n' {
		r.failed = true
		return "", nil, size, fmt.Errorf("invalid Git object contents terminator for %s", oid)
	}
	return kind, content, size, nil
}

func (r *gitBatchReader) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	if r.failed {
		_ = r.cmd.Process.Kill()
	}
	closeErr := r.stdin.Close()
	waitErr := r.cmd.Wait()
	if closeErr != nil {
		if waitErr != nil {
			return fmt.Errorf("close Git batch input: %v; wait: %w", closeErr, waitErr)
		}
		return closeErr
	}
	if waitErr != nil {
		if r.stderr.Len() > 0 {
			return fmt.Errorf("git cat-file --batch-command: %w: %s", waitErr, strings.TrimSpace(r.stderr.String()))
		}
		return fmt.Errorf("git cat-file --batch-command: %w", waitErr)
	}
	return nil
}
