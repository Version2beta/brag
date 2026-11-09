package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Metadata bounds preserve complete attribution for ordinary histories and fail explicitly rather than truncate it.
const (
	historyNumstatBatchSize      = 500
	historyAuthorLogMaxBytes     = 32 << 20
	historyCommitObjectMaxBytes  = 8 << 20
	historyMetadataTotalMaxBytes = 64 << 20
)

var errHistoryCommitMetadataLimit = errors.New("history commit metadata exceeds byte cap")

type historyAuthor struct {
	commit string
	name   string
	email  string
}

type historyTotals struct {
	people     map[string]*Person
	churn      Counts
	authored   int
	coauthored int
}

func collectHistory(root, revs string, commits []string, identities maps) (historyTotals, error) {
	totals := historyTotals{people: map[string]*Person{}}
	if len(commits) == 0 {
		return totals, nil
	}
	authorBytes, err := runGitBounded(root, historyAuthorLogMaxBytes, "log", "-z", "--reverse", "--format=%H%x00%an%x00%ae", revs)
	if err != nil {
		return collectHistoryLegacy(root, commits, identities)
	}
	authors, err := parseHistoryAuthors(authorBytes, commits)
	if err != nil {
		return collectHistoryLegacy(root, commits, identities)
	}
	authorBytes = nil
	batch, err := newGitBatchReaderForCommit(root, commits[0])
	if err != nil {
		return collectHistoryLegacy(root, commits, identities)
	}
	commitPeople := make([]*Person, len(commits))
	metadataBytes := 0
	for i, commit := range commits {
		object, size, err := readHistoryCommitObject(batch, commit, historyMetadataTotalMaxBytes-metadataBytes)
		if err != nil {
			_ = batch.Close()
			return totals, fmt.Errorf("read history commit metadata: %w", err)
		}
		metadataBytes += size
		separator := bytes.Index(object, []byte("\n\n"))
		if separator < 0 {
			_ = batch.Close()
			return totals, fmt.Errorf("commit object %s has no message boundary", commit)
		}
		author := resolve(identityInfo{Name: authors[i].name, Email: authors[i].email, Available: true}, identities)
		person := getPerson(totals.people, author)
		person.AuthorCommits++
		totals.authored++
		commitPeople[i] = person
		seen := map[string]bool{}
		for _, coauthor := range parseCoauthors(string(object[separator+2:])) {
			coauthor = resolve(coauthor, identities)
			key := idKey(coauthor)
			if seen[key] {
				continue
			}
			seen[key] = true
			getPerson(totals.people, coauthor).CoauthorCommits++
			totals.coauthored++
		}
	}
	if err := batch.Close(); err != nil {
		return totals, err
	}
	for start := 0; start < len(commits); start += historyNumstatBatchSize {
		end := start + historyNumstatBatchSize
		if end > len(commits) {
			end = len(commits)
		}
		if err := addHistoryNumstatChunk(root, commits[start:end], commitPeople[start:end], &totals); err != nil {
			return totals, err
		}
	}
	return totals, nil
}

func collectHistoryLegacy(root string, commits []string, identities maps) (historyTotals, error) {
	totals := historyTotals{people: map[string]*Person{}}
	metadataBytes := 0
	for _, commit := range commits {
		remaining := historyMetadataTotalMaxBytes - metadataBytes
		if remaining <= 0 {
			return totals, fmt.Errorf("history commit metadata exceeds %d byte cap", historyMetadataTotalMaxBytes)
		}
		limit := historyCommitObjectMaxBytes
		if remaining < limit {
			limit = remaining
		}
		metadata, err := runGitBounded(root, int64(limit), "show", "-s", "--format=%an%x00%ae%x00%B", commit)
		if err != nil {
			return totals, err
		}
		metadataBytes += len(metadata)
		fields := strings.SplitN(string(metadata), "\x00", 3)
		if len(fields) < 3 {
			continue
		}
		author := resolve(identityInfo{Name: fields[0], Email: fields[1], Available: true}, identities)
		person := getPerson(totals.people, author)
		person.AuthorCommits++
		totals.authored++
		seen := map[string]bool{}
		for _, coauthor := range parseCoauthors(fields[2]) {
			coauthor = resolve(coauthor, identities)
			key := idKey(coauthor)
			if seen[key] {
				continue
			}
			seen[key] = true
			getPerson(totals.people, coauthor).CoauthorCommits++
			totals.coauthored++
		}
		numstat, err := runGit(root, "diff-tree", "--no-ext-diff", "--no-textconv", "--root", "--no-commit-id", "--numstat", "-r", "-M", commit)
		if err != nil {
			return totals, err
		}
		change := sum(numstat)
		totals.churn = add(totals.churn, change)
		person.LinesAdded += change.LinesAdded
		person.LinesDeleted += change.LinesDeleted
		person.SourceLinesAdded += change.SourceLinesAdded
		person.SourceLinesDeleted += change.SourceLinesDeleted
	}
	return totals, nil
}

func readHistoryCommitObject(reader *gitBatchReader, commit string, remaining int) ([]byte, int, error) {
	kind, size, err := reader.Info(commit)
	if err != nil {
		return nil, 0, err
	}
	if kind != "commit" {
		return nil, size, fmt.Errorf("history object is %s, not a commit", kind)
	}
	if size > historyCommitObjectMaxBytes || size > remaining {
		return nil, size, errHistoryCommitMetadataLimit
	}
	kind, object, readSize, err := reader.Read(commit, historyCommitObjectMaxBytes)
	if err != nil {
		return nil, size, err
	}
	if kind != "commit" || readSize != size || len(object) != size {
		return nil, size, fmt.Errorf("history commit object size or type changed")
	}
	return object, size, nil
}

func addHistoryNumstatChunk(root string, commits []string, people []*Person, totals *historyTotals) error {
	if len(commits) == 0 || len(commits) != len(people) {
		return fmt.Errorf("invalid history numstat chunk")
	}
	indexes := make(map[string]int, len(commits))
	for i, commit := range commits {
		indexes[commit] = i
	}
	input := strings.NewReader(strings.Join(commits, "\n") + "\n")
	cmd := exec.Command("git", "-C", root, "diff-tree", "--no-ext-diff", "--no-textconv", "--root", "--numstat", "-r", "-M", "--stdin")
	cmd.Env = gitEnvironment(os.Environ())
	cmd.Stdin = input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(stdout)
	parseErr := func() error {
		current := -1
		lastIndex := -1
		for {
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			value := strings.TrimSuffix(line, "\n")
			if value != "" {
				if index, ok := indexes[value]; ok {
					if index <= lastIndex {
						return fmt.Errorf("history numstat order mismatch")
					}
					current = index
					lastIndex = index
				} else {
					if current < 0 || !strings.Contains(value, "\t") {
						return fmt.Errorf("invalid history numstat record")
					}
					change := sum([]byte(line))
					totals.churn = add(totals.churn, change)
					person := people[current]
					person.LinesAdded += change.LinesAdded
					person.LinesDeleted += change.LinesDeleted
					person.SourceLinesAdded += change.SourceLinesAdded
					person.SourceLinesDeleted += change.SourceLinesDeleted
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}()
	if parseErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return parseErr
	}
	if err := cmd.Wait(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("git diff-tree numstat: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("git diff-tree numstat: %w", err)
	}
	return nil
}

func parseHistoryAuthors(data []byte, commits []string) ([]historyAuthor, error) {
	fields := bytes.Split(data, []byte{0})
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields) != len(commits)*3 {
		return nil, fmt.Errorf("history author record count mismatch")
	}
	authors := make([]historyAuthor, len(commits))
	for i, commit := range commits {
		id := string(fields[i*3])
		if id != commit {
			return nil, fmt.Errorf("history author order mismatch at commit %d", i)
		}
		authors[i] = historyAuthor{commit: id, name: string(fields[i*3+1]), email: string(fields[i*3+2])}
	}
	return authors, nil
}
