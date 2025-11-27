package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const wptRepoRawURL = "https://raw.githubusercontent.com/web-platform-tests/wpt"

type config struct {
	Commit    string     `json:"commit"`
	TargetDir string     `json:"target_dir"`
	Files     []fileSpec `json:"files"`
}

type fileSpec struct {
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	Enabled *bool  `json:"enabled,omitempty"`
	Patch   string `json:"patch,omitempty"`
}

func (f fileSpec) isEnabled() bool {
	return f.Enabled == nil || *f.Enabled
}

func main() {
	var (
		configPath   = flag.String("config", "wpt.json", "path to the WPT sync configuration file")
		skipPatching = flag.Bool("skip-patches", false, "download files but do not apply any configured patches")
		dryRun       = flag.Bool("dry-run", false, "print the actions that would be taken without writing files")
	)
	flag.Parse()

	if err := run(*configPath, *skipPatching, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "sync-wpt: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string, skipPatching, dryRun bool) error {
	root, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return fmt.Errorf("determine repo root from config: %w", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	if err := cfg.validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Syncing %d WPT files from %s at commit %s\n", len(cfg.Files), wptRepoRawURL, cfg.Commit)

	for _, file := range cfg.Files {
		if !file.isEnabled() {
			fmt.Printf(" - skipping %s (disabled)\n", file.Src)
			continue
		}
		if err := processFile(ctx, root, cfg, file, skipPatching, dryRun); err != nil {
			return err
		}
	}

	return nil
}

func loadConfig(path string) (*config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var cfg config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %q: %w", path, err)
	}

	return &cfg, nil
}

func (c *config) validate() error {
	if c.Commit == "" {
		return errors.New("config: commit hash must be provided")
	}
	if c.TargetDir == "" {
		return errors.New("config: target_dir must be provided")
	}
	if len(c.Files) == 0 {
		return errors.New("config: files list cannot be empty")
	}
	return nil
}

func processFile(ctx context.Context, root string, cfg *config, file fileSpec, skipPatching, dryRun bool) error {
	src := strings.TrimLeft(file.Src, "/")
	url := fmt.Sprintf("%s/%s/%s", wptRepoRawURL, cfg.Commit, src)
	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))

	fmt.Printf(" - %s -> %s\n", src, dest)
	if dryRun {
		return nil
	}

	if err := download(ctx, url, dest); err != nil {
		return fmt.Errorf("download %s: %w", src, err)
	}

	if skipPatching || file.Patch == "" {
		return nil
	}

	if err := applyPatch(ctx, root, file.Patch); err != nil {
		return fmt.Errorf("apply patch %s: %w", file.Patch, err)
	}

	return nil
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(dest), ".wpt-download-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), dest); err != nil {
		return fmt.Errorf("move file into place: %w", err)
	}

	return nil
}

func applyPatch(ctx context.Context, root, patchPath string) error {
	absPatch := patchPath
	if !filepath.IsAbs(patchPath) {
		absPatch = filepath.Join(root, patchPath)
	}

	if _, err := os.Stat(absPatch); err != nil {
		return fmt.Errorf("stat patch: %w", err)
	}

	if err := ensureSupportedPatchFormat(absPatch); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "git", "apply", "--allow-empty", "--whitespace=nowarn", absPatch)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply failed: %w", err)
	}

	return nil
}

func ensureSupportedPatchFormat(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open patch %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "*** Begin Patch") {
			return fmt.Errorf("patch %s looks like apply_patch format; regenerate it with `git diff > %s` so git apply can read it", path, path)
		}
		break
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read patch %s: %w", path, err)
	}

	return nil
}
