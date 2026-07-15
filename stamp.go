package wptsync

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// stampFileName is the freshness marker written under a config's target_dir
// once every tracked file has been synced.
const stampFileName = ".wptsync-stamp"

// stampPath returns the freshness stamp path for cfg rooted at root.
func stampPath(root string, cfg *Config) string {
	return filepath.Join(root, cfg.TargetDir, stampFileName)
}

// computeStamp hashes the config file bytes plus, for every enabled entry
// with a patch, the patch's path and raw bytes (in config order). Including
// the path means a patch rename invalidates the stamp even if its content
// didn't change.
//
// An error (config or patch file unreadable) means "no valid stamp can be
// computed right now"; callers should treat that as a stale stamp and fall
// through to a real sync, which will report the underlying error itself if
// it's still a problem there.
func computeStamp(configPath, root string, cfg *Config) (string, error) {
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	h.Write(configBytes)

	for _, f := range cfg.Files {
		if !f.IsEnabled() || f.Patch == "" {
			continue
		}
		h.Write([]byte(f.Patch))

		patchAbs := f.Patch
		if !filepath.IsAbs(patchAbs) {
			patchAbs = filepath.Join(root, filepath.FromSlash(f.Patch))
		}
		patchBytes, err := os.ReadFile(patchAbs)
		if err != nil {
			return "", err
		}
		h.Write(patchBytes)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// stampIsFresh reports whether the stamp file at stampFile contains hash and
// every enabled entry's Dst file is still present on disk.
func stampIsFresh(stampFile, hash, root string, cfg *Config) bool {
	got, err := os.ReadFile(stampFile)
	if err != nil || string(got) != hash {
		return false
	}

	for _, f := range cfg.Files {
		if !f.IsEnabled() {
			continue
		}
		dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(f.Dst))
		if _, err := os.Stat(dest); err != nil {
			return false
		}
	}

	return true
}

// writeStamp computes and writes the freshness stamp for cfg. Errors are
// non-fatal to callers: the stamp is an optimization, not a correctness
// requirement.
func writeStamp(configPath, root string, cfg *Config) {
	hash, err := computeStamp(configPath, root, cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(stampPath(root, cfg), []byte(hash), 0o644)
}
