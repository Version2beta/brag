package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

const authorConfigMaxBytes = 1 << 20

func authorConfigWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Trust the resolved repository anchor, not symlinks beneath it. Absolute
// external configs are explicit opt-ins, but their path components are checked too.
func authorConfigPath(root, path string) (anchor, full string, err error) {
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return "", "", errors.New("identity config path must not contain parent traversal")
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(path) {
		return canonicalRoot, filepath.Join(canonicalRoot, path), nil
	}
	if authorConfigWithin(root, path) {
		rel, _ := filepath.Rel(root, path)
		return canonicalRoot, filepath.Join(canonicalRoot, rel), nil
	}
	// These are OS-owned macOS prefix aliases, not permission to resolve an
	// arbitrary config-parent symlink. Verify their expected targets before use.
	if runtime.GOOS == "darwin" {
		for _, prefix := range []string{"/var", "/tmp"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				if target, e := filepath.EvalSymlinks(prefix); e == nil && target == "/private"+prefix {
					path = target + strings.TrimPrefix(path, prefix)
				}
			}
		}
	}
	if authorConfigWithin(canonicalRoot, path) {
		return canonicalRoot, filepath.Clean(path), nil
	}
	return filepath.VolumeName(path) + string(os.PathSeparator), filepath.Clean(path), nil
}

func checkAuthorConfigParents(anchor, path string) error {
	rel, err := filepath.Rel(anchor, path)
	if err != nil || !authorConfigWithin(anchor, path) {
		return errors.New("identity config path escapes its anchor")
	}
	current := anchor
	for _, part := range strings.Split(filepath.Dir(rel), string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("identity config parent unavailable: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("identity config must not traverse symlink parents")
		}
		if !info.IsDir() {
			return errors.New("identity config parent must be a directory")
		}
	}
	return nil
}

func sameAuthorConfigObservation(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func readAuthorConfigBytes(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("identity config exceeds byte limit")
	}
	return data, nil
}

func readAuthorConfigSnapshot(path string, before os.FileInfo) ([]byte, error) {
	if !before.Mode().IsRegular() || before.Size() > authorConfigMaxBytes {
		return nil, errors.New("identity config must be a regular file at most 1 MiB (no symlink)")
	}
	file, err := openAuthorConfig(path)
	if err != nil {
		return nil, fmt.Errorf("open identity config without symlink traversal: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !sameAuthorConfigObservation(before, opened) {
		return nil, errors.New("identity config changed before reading or is not regular")
	}
	data, err := readAuthorConfigBytes(file, authorConfigMaxBytes)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !sameAuthorConfigObservation(opened, after) {
		return nil, errors.New("identity config changed while reading")
	}
	atPath, err := os.Lstat(path)
	if err != nil || !sameAuthorConfigObservation(after, atPath) {
		return nil, errors.New("identity config path changed while reading")
	}
	return data, nil
}

func readAuthorIdentityMaps(root, path string, ids, aliases []string, enabled bool) (maps, error) {
	config := configData{Version: 1}
	if enabled {
		anchor, full, err := authorConfigPath(root, path)
		if err != nil {
			return maps{}, err
		}
		if err = checkAuthorConfigParents(anchor, full); err != nil {
			return maps{}, err
		}
		before, err := os.Lstat(full)
		if err != nil {
			return maps{}, fmt.Errorf("identity config unavailable: %w", err)
		}
		data, err := readAuthorConfigSnapshot(full, before)
		if err != nil {
			return maps{}, err
		}
		if err = checkAuthorConfigParents(anchor, full); err != nil {
			return maps{}, err
		}
		if json.Unmarshal(data, &config) != nil || config.Version != 1 {
			return maps{}, errors.New("invalid identity config version or JSON")
		}
	}
	result := maps{exact: map[string]identityInfo{}, aliases: map[string]string{}}
	for _, identity := range config.Identities {
		result.exact[strings.ToLower(identity.Email)] = identity
	}
	for from, to := range config.Aliases {
		result.aliases[strings.ToLower(from)] = to
	}
	for _, value := range ids {
		identity, err := authorIdentityValue(value)
		if err != nil {
			return maps{}, err
		}
		result.exact[strings.ToLower(identity.Email)] = identity
	}
	for _, value := range aliases {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return maps{}, errors.New("--alias requires EMAIL=CANONICAL_EMAIL")
		}
		result.aliases[strings.ToLower(parts[0])] = parts[1]
	}
	return result, nil
}
