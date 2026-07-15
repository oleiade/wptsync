package wptsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeStamp(t *testing.T) {
	root := t.TempDir()

	configPath := filepath.Join(root, "wpt.json")
	if err := os.WriteFile(configPath, []byte(`{"commit":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	patchPath := filepath.Join(root, "patches", "a.js.patch")
	if err := os.MkdirAll(filepath.Dir(patchPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchPath, []byte("patch v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Commit:    "abc",
		TargetDir: "wpt",
		Files: []FileSpec{
			{Src: "a.js", Dst: "a.js", Patch: "patches/a.js.patch"},
		},
	}

	hash1, err := computeStamp(configPath, root, cfg)
	if err != nil {
		t.Fatalf("computeStamp: %v", err)
	}

	hash2, err := computeStamp(configPath, root, cfg)
	if err != nil {
		t.Fatalf("computeStamp (repeat): %v", err)
	}
	if hash1 != hash2 {
		t.Error("same inputs produced different hashes")
	}

	if err := os.WriteFile(configPath, []byte(`{"commit":"def"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hash3, err := computeStamp(configPath, root, cfg)
	if err != nil {
		t.Fatalf("computeStamp (changed config): %v", err)
	}
	if hash3 == hash1 {
		t.Error("changed config bytes produced the same hash")
	}

	// Restore the config so only the patch changes for the next check.
	if err := os.WriteFile(configPath, []byte(`{"commit":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchPath, []byte("patch v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash4, err := computeStamp(configPath, root, cfg)
	if err != nil {
		t.Fatalf("computeStamp (changed patch): %v", err)
	}
	if hash4 == hash1 {
		t.Error("changed patch bytes produced the same hash")
	}
}
