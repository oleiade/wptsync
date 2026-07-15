package wptsync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the default base URL files are downloaded from: the raw
// content host for the web-platform-tests repository.
const DefaultBaseURL = "https://raw.githubusercontent.com/web-platform-tests/wpt"

// SyncOptions configures a Sync run. A nil *SyncOptions is equivalent to its
// zero value.
type SyncOptions struct {
	// SkipPatches downloads files but does not apply any configured patches.
	SkipPatches bool
	// DryRun prints the actions that would be taken without writing files.
	DryRun bool
	// Force bypasses the freshness stamp, forcing a full sync even when the
	// stamp indicates the local files are already up to date.
	Force bool
	// BaseURL is the raw file base URL. Empty means DefaultBaseURL.
	BaseURL string
	// Logf receives progress messages. Nil means no output.
	Logf func(format string, args ...any)
}

func (o *SyncOptions) logf(format string, args ...any) {
	if o == nil || o.Logf == nil {
		return
	}
	o.Logf(format, args...)
}

func (o *SyncOptions) baseURL() string {
	if o == nil || o.BaseURL == "" {
		return DefaultBaseURL
	}
	return o.BaseURL
}

// Sync downloads the files listed in the configuration at configPath (at the
// commit pinned in that configuration) and applies their configured patches.
func Sync(ctx context.Context, configPath string, opts *SyncOptions) error {
	root, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return fmt.Errorf("determine repo root from config: %w", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}

	if err := cfg.validate(); err != nil {
		return err
	}

	logf := opts.logf
	baseURL := opts.baseURL()
	skipPatching := opts != nil && opts.SkipPatches
	dryRun := opts != nil && opts.DryRun
	force := opts != nil && opts.Force

	if len(cfg.Files) == 0 {
		logf("No files configured to sync.\n")
		return nil
	}

	// ponytail: no cross-process locking; two packages syncing the same config concurrently can race on first population. Add a lock file if that ever happens.
	if !dryRun && !force && !skipPatching {
		stampFile := stampPath(root, cfg)
		if hash, err := computeStamp(configPath, root, cfg); err == nil && stampIsFresh(stampFile, hash, root, cfg) {
			logf("wpt files up to date (stamp match); skipping sync\n")
			return nil
		}
	}

	logf("Syncing %d WPT files from %s at commit %s\n", len(cfg.Files), baseURL, cfg.Commit)

	for _, file := range cfg.Files {
		if !file.IsEnabled() {
			logf(" - skipping %s (disabled)\n", file.Src)
			continue
		}
		if err := processFile(ctx, root, cfg, file, skipPatching, dryRun, baseURL, logf); err != nil {
			return err
		}
	}

	if !dryRun && !skipPatching {
		writeStamp(configPath, root, cfg)
	}

	return nil
}

// processFile downloads a single configured file and applies its patch (if
// any). It is the shared per-file step used by Sync, Update, and Edit.
func processFile(ctx context.Context, root string, cfg *Config, file FileSpec, skipPatching, dryRun bool, baseURL string, logf func(format string, args ...any)) error {
	// Per-file timeout so a long file list never starves later downloads.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	src := strings.TrimLeft(file.Src, "/")
	url := fmt.Sprintf("%s/%s/%s", baseURL, cfg.Commit, src)
	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))

	logf(" - %s -> %s\n", src, dest)
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

// ErrPatchFailed marks git apply failures so update can keep going and report
// them all at the end instead of aborting on the first one.
var ErrPatchFailed = errors.New("git apply failed")

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

	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimRight(string(output), " \t\r\n")
		if out == "" {
			return fmt.Errorf("%w: %v", ErrPatchFailed, err)
		}
		return fmt.Errorf("%w: %v: %s", ErrPatchFailed, err, out)
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
