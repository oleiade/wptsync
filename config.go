package wptsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the on-disk wpt.json configuration: the pinned WPT commit, the
// local directory files are synced into, and the list of tracked files.
type Config struct {
	Commit    string     `json:"commit"`
	TargetDir string     `json:"target_dir"`
	Files     []FileSpec `json:"files"`
}

// FileSpec describes a single file tracked from the WPT repository.
type FileSpec struct {
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	Enabled *bool  `json:"enabled,omitempty"`
	Patch   string `json:"patch,omitempty"`
}

// IsEnabled reports whether the file should be synced. Files are enabled by
// default; they are only skipped when Enabled is explicitly set to false.
func (f FileSpec) IsEnabled() bool {
	return f.Enabled == nil || *f.Enabled
}

// LoadConfig reads and decodes the configuration file at path. Any FileSpec
// with an empty Dst is normalized to use Src as its destination.
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var cfg Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}

	for i := range cfg.Files {
		if cfg.Files[i].Dst == "" {
			cfg.Files[i].Dst = cfg.Files[i].Src
		}
	}

	return &cfg, nil
}

// SaveConfig writes cfg to path as indented JSON.
func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func (c *Config) validate() error {
	if c.Commit == "" {
		return errors.New("config: commit hash must be provided")
	}
	if c.TargetDir == "" {
		return errors.New("config: target_dir must be provided")
	}
	seen := make(map[string]string, len(c.Files))
	for _, f := range c.Files {
		if f.Src == "" {
			return fmt.Errorf("config: file entries must set src (src=%q)", f.Src)
		}
		if !filepath.IsLocal(filepath.FromSlash(f.Dst)) {
			return fmt.Errorf("config: dst %q escapes the target directory", f.Dst)
		}
		if prev, ok := seen[f.Dst]; ok {
			return fmt.Errorf("config: dst %q used by both %q and %q", f.Dst, prev, f.Src)
		}
		seen[f.Dst] = f.Src
	}
	return nil
}

// findFileSpec returns a pointer into cfg.Files for the entry whose Src or
// Dst matches filePath (after trimming surrounding slashes).
func findFileSpec(cfg *Config, filePath string) (*FileSpec, error) {
	p := strings.Trim(filePath, "/")
	for i := range cfg.Files {
		if cfg.Files[i].Dst == p || cfg.Files[i].Src == p {
			return &cfg.Files[i], nil
		}
	}
	return nil, fmt.Errorf("no config entry matches %q (compared against src and dst)", p)
}
