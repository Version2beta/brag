package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorEvidenceConfigBoundedSnapshotAndDecode(t *testing.T) {
	reader := bytes.NewReader([]byte("123456789"))
	if _, err := readAuthorConfigBytes(reader, 4); err == nil || reader.Len() != 4 {
		t.Fatal("config byte cap did not stop after cap+1")
	}
	if data, err := readAuthorConfigBytes(strings.NewReader("1234"), 4); err != nil || string(data) != "1234" {
		t.Fatal("exact config read boundary rejected")
	}
	root := t.TempDir()
	path := filepath.Join(root, "identity.json")
	valid := []byte(`{"version":1,"identities":[{"name":"A=B","email":"ada@example.test"}],"aliases":{"old@example.test":"ada@example.test"}}`)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := readAuthorConfigSnapshot(path, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed after capture"), 0600); err != nil {
		t.Fatal(err)
	}
	var decoded configData
	if json.Unmarshal(captured, &decoded) != nil || decoded.Identities[0].Name != "A=B" {
		t.Fatal("captured config bytes did not remain independent of later file writes")
	}
	cases := []struct {
		data  []byte
		valid bool
	}{
		{append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), authorConfigMaxBytes-len(valid))...), true},
		{bytes.Repeat([]byte(" "), authorConfigMaxBytes+1), false},
		{[]byte("{"), false},
		{[]byte(`{"version":2}`), false},
	}
	for _, tc := range cases {
		if err := os.WriteFile(path, tc.data, 0600); err != nil {
			t.Fatal(err)
		}
		mapping, err := readAuthorIdentityMaps(root, path, nil, nil, true)
		if (err == nil) != tc.valid {
			t.Fatalf("config length=%d valid=%v err=%v", len(tc.data), tc.valid, err)
		}
		if tc.valid && resolve(identityInfo{Name: "Old", Email: "old@example.test", Available: true}, mapping).Email != "ada@example.test" {
			t.Fatal("alias semantics changed")
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), authorConfigMaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorConfigSnapshot(path, before); err == nil {
		t.Fatal("config growth after initial size observation accepted")
	}
}

func TestAuthorEvidenceConfigPathComponentsAndIdentityChanges(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	valid := []byte(`{"version":1}`)
	path := filepath.Join(external, "identity.json")
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorIdentityMaps(root, path, nil, nil, true); err != nil {
		t.Fatalf("explicit external regular path: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorIdentityMaps(root, "link/identity.json", nil, nil, true); err == nil {
		t.Fatal("relative symlink escape accepted")
	}
	if _, err := readAuthorIdentityMaps(root, "../identity.json", nil, nil, true); err == nil {
		t.Fatal("lexical parent escape accepted")
	}
	if err := os.Symlink(path, filepath.Join(root, "file-link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorIdentityMaps(root, "file-link.json", nil, nil, true); err == nil {
		t.Fatal("final file symlink accepted")
	}
	local := filepath.Join(root, "local.json")
	if err := os.WriteFile(local, valid, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, local); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorConfigSnapshot(local, before); err == nil {
		t.Fatal("file swapped to symlink after observation accepted")
	}
	// Repository-prefix aliases are trusted anchors, not aliases beneath the root.
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "safe.json"), valid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorIdentityMaps(alias, filepath.Join(alias, "safe.json"), nil, nil, true); err != nil {
		t.Fatalf("trusted repository root alias rejected: %v", err)
	}
}
