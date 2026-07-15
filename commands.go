package wptsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const wptGitHubAPIURL = "https://api.github.com/repos/web-platform-tests/wpt/commits/master"

const wptGitHubTreesAPI = "https://api.github.com/repos/web-platform-tests/wpt/git/trees"

// Init fetches the latest WPT commit and creates a new configuration file at
// configPath with an empty file list. It returns an error if configPath
// already exists.
func Init(ctx context.Context, configPath string) error {
	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file %q already exists", configPath)
	}

	fmt.Printf("Fetching latest WPT commit...\n")

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	commit, err := fetchLatestCommit(ctx)
	if err != nil {
		return fmt.Errorf("fetch latest commit: %w", err)
	}

	cfg := Config{
		Commit:    commit,
		TargetDir: "wpt",
		Files:     []FileSpec{},
	}

	if err := SaveConfig(configPath, &cfg); err != nil {
		return err
	}

	fmt.Printf("Created %s with commit %s\n", configPath, commit)
	return nil
}

func fetchLatestCommit(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wptGitHubAPIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.SHA == "" {
		return "", errors.New("empty commit SHA in response")
	}

	return result.SHA, nil
}

// Add fetches the list of .js files under wptPath in the WPT repository (at
// the commit pinned in configPath) and registers any not already tracked.
func Add(ctx context.Context, configPath, wptPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Normalize the path: remove leading/trailing slashes
	wptPath = strings.Trim(wptPath, "/")

	fmt.Printf("Fetching file list from %s...\n", wptPath)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	files, err := listFilesInPath(ctx, cfg.Commit, wptPath)
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	if len(files) == 0 {
		fmt.Printf("No .js files found in %s\n", wptPath)
		return nil
	}

	// Build a set of existing src paths for deduplication
	existing := make(map[string]bool)
	for _, f := range cfg.Files {
		existing[f.Src] = true
	}

	// Add new files
	added := 0
	for _, src := range files {
		if existing[src] {
			continue
		}

		dst := src
		// Strip .any.js suffix to .js
		if base, ok := strings.CutSuffix(dst, ".any.js"); ok {
			dst = base + ".js"
		}

		cfg.Files = append(cfg.Files, FileSpec{
			Src: src,
			Dst: dst,
		})
		added++
		fmt.Printf(" + %s\n", src)
	}

	if added == 0 {
		fmt.Println("No new files to add (all files already in config).")
		return nil
	}

	if err := SaveConfig(configPath, cfg); err != nil {
		return err
	}

	fmt.Printf("Added %d files to %s\n", added, configPath)
	return nil
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type treeResponse struct {
	Tree      []treeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

func fetchTree(ctx context.Context, sha string, recursive bool) (*treeResponse, error) {
	url := wptGitHubTreesAPI + "/" + sha
	if recursive {
		url += "?recursive=1"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("GitHub API returned 403 (rate limit likely exceeded, try again later)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var tree treeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tree, nil
}

func listFilesInPath(ctx context.Context, commit, pathPrefix string) ([]string, error) {
	// Walk the path segments to the subtree (or single blob), then list that
	// subtree with one recursive request instead of one request per directory.
	sha := commit
	var segments []string
	if pathPrefix != "" {
		segments = strings.Split(pathPrefix, "/")
	}
	for i, segment := range segments {
		tree, err := fetchTree(ctx, sha, false)
		if err != nil {
			return nil, err
		}
		var entry *treeEntry
		for j := range tree.Tree {
			if tree.Tree[j].Path == segment {
				entry = &tree.Tree[j]
				break
			}
		}
		if entry == nil {
			return nil, fmt.Errorf("path %q not found in repository", pathPrefix)
		}
		if entry.Type == "blob" {
			if i != len(segments)-1 {
				return nil, fmt.Errorf("%q is a file, not a directory", strings.Join(segments[:i+1], "/"))
			}
			if strings.HasSuffix(pathPrefix, ".js") {
				return []string{pathPrefix}, nil
			}
			return nil, nil
		}
		sha = entry.SHA
	}

	tree, err := fetchTree(ctx, sha, true)
	if err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, fmt.Errorf("GitHub truncated the tree listing for %q; add a more specific path instead", pathPrefix)
	}

	var files []string
	for _, entry := range tree.Tree {
		if entry.Type == "blob" && strings.HasSuffix(entry.Path, ".js") {
			files = append(files, path.Join(pathPrefix, entry.Path))
		}
	}
	return files, nil
}

// Update bumps the pinned commit (to commit, or the latest WPT commit when
// commit is empty) and re-syncs every enabled file. Patches that no longer
// apply are reported at the end instead of aborting the run; the returned
// error wraps ErrPatchFailed information in its message when any patches
// failed.
func Update(ctx context.Context, configPath, commit string) error {
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

	if commit == "" {
		fmt.Println("Fetching latest WPT commit...")
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		commit, err = fetchLatestCommit(fetchCtx)
		if err != nil {
			return fmt.Errorf("fetch latest commit: %w", err)
		}
	}

	if commit == cfg.Commit {
		fmt.Printf("Already at commit %s; nothing to update.\n", commit)
		return nil
	}

	fmt.Printf("Updating commit %s -> %s\n", cfg.Commit, commit)
	cfg.Commit = commit
	// Save before syncing so an aborted run can resume with a plain `sync`.
	if err := SaveConfig(configPath, cfg); err != nil {
		return err
	}

	logf := func(format string, args ...any) { fmt.Printf(format, args...) }

	var failed []string
	for _, file := range cfg.Files {
		if !file.IsEnabled() {
			fmt.Printf(" - skipping %s (disabled)\n", file.Src)
			continue
		}
		err := processFile(ctx, root, cfg, file, false, false, DefaultBaseURL, logf)
		if errors.Is(err, ErrPatchFailed) {
			fmt.Fprintf(os.Stderr, "   %v\n", err)
			failed = append(failed, file.Dst)
			continue
		}
		if err != nil {
			return err
		}
	}

	if len(failed) > 0 {
		if err := os.Remove(stampPath(root, cfg)); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "   warning: remove stale freshness stamp: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "\nPatches that no longer apply (files left pristine):\n")
		for _, dst := range failed {
			fmt.Fprintf(os.Stderr, " - %s\n", dst)
		}
		return fmt.Errorf("%d patch(es) failed to apply; edit the file(s) and run `wptsync save <path>` to regenerate them", len(failed))
	}

	writeStamp(configPath, root, cfg)

	fmt.Printf("Updated to commit %s\n", commit)
	return nil
}

// Edit re-downloads a single configured file at the pinned commit and
// re-applies its patch, restoring it to its synced state so it is ready for
// editing. filePath is matched against each entry's src or dst.
func Edit(ctx context.Context, configPath, filePath string) error {
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

	file, err := findFileSpec(cfg, filePath)
	if err != nil {
		return err
	}

	logf := func(format string, args ...any) { fmt.Printf(format, args...) }
	if err := processFile(ctx, root, cfg, *file, false, false, DefaultBaseURL, logf); err != nil {
		return err
	}

	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))
	fmt.Printf("Restored %s to its synced state.\nEdit it, then run `wptsync save %s` to update its patch.\n", dest, file.Dst)
	return nil
}

// Save downloads the pristine file at the pinned commit, diffs it against
// the on-disk file at filePath, and writes the result to the file's patch
// (default: patches/<dst>.patch), registering it in the configuration if
// needed. If the file no longer differs from pristine, the patch is removed
// instead. filePath is matched against each entry's src or dst.
func Save(ctx context.Context, configPath, filePath string) error {
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

	file, err := findFileSpec(cfg, filePath)
	if err != nil {
		return err
	}

	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))
	if _, err := os.Stat(dest); err != nil {
		return fmt.Errorf("%s not found on disk; run `wptsync sync` first", dest)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "wptsync-save-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pristine := filepath.Join(tmpDir, "pristine")
	src := strings.TrimLeft(file.Src, "/")
	url := fmt.Sprintf("%s/%s/%s", DefaultBaseURL, cfg.Commit, src)
	if err := download(ctx, url, pristine); err != nil {
		return fmt.Errorf("download pristine %s: %w", src, err)
	}

	diff, err := gitDiffNoIndex(ctx, pristine, dest)
	if err != nil {
		return err
	}

	patchRel := file.Patch
	if patchRel == "" {
		patchRel = path.Join("patches", file.Dst+".patch")
	}
	patchAbs := patchRel
	if !filepath.IsAbs(patchAbs) {
		patchAbs = filepath.Join(root, filepath.FromSlash(patchRel))
	}

	if len(diff) == 0 {
		if file.Patch == "" {
			fmt.Printf("%s matches pristine; nothing to save.\n", file.Dst)
			return nil
		}
		if err := os.Remove(patchAbs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove patch: %w", err)
		}
		file.Patch = ""
		if err := SaveConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("%s matches pristine; removed patch %s\n", file.Dst, patchRel)
		return nil
	}

	rel := path.Join(cfg.TargetDir, file.Dst)
	patched := rewritePatchPaths(diff, rel)

	if err := os.MkdirAll(filepath.Dir(patchAbs), 0o755); err != nil {
		return fmt.Errorf("create patch directory: %w", err)
	}
	if err := os.WriteFile(patchAbs, patched, 0o644); err != nil {
		return fmt.Errorf("write patch: %w", err)
	}

	if file.Patch == "" {
		file.Patch = patchRel
		if err := SaveConfig(configPath, cfg); err != nil {
			return err
		}
	}

	fmt.Printf("Saved patch %s for %s\n", patchRel, file.Dst)
	return nil
}

// gitDiffNoIndex diffs two files outside any git index. It returns nil output
// when the files are identical.
func gitDiffNoIndex(ctx context.Context, a, b string) ([]byte, error) {
	// --no-ext-diff and --no-color keep the output a plain unified diff even
	// when the user's git config sets an external diff tool or forced colors.
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--no-color", "--no-index", "--", a, b)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return nil, nil
	}
	// git diff exits 1 when the files differ; anything else is a real error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return out.Bytes(), nil
	}
	return nil, fmt.Errorf("git diff --no-index: %w", err)
}

// rewritePatchPaths replaces the temp-file paths in the diff headers with the
// config-relative file path, so `git apply` run from the config root finds it.
func rewritePatchPaths(diff []byte, rel string) []byte {
	lines := strings.Split(string(diff), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			break
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			lines[i] = fmt.Sprintf("diff --git a/%s b/%s", rel, rel)
		case strings.HasPrefix(line, "--- "):
			lines[i] = "--- a/" + rel
		case strings.HasPrefix(line, "+++ "):
			lines[i] = "+++ b/" + rel
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
