package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
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

const usage = `wptsync - Sync files from the web-platform-tests repository

Usage:
  wptsync <command> [options]

Commands:
  init    Create a new wpt.json configuration file
  add     Add files from a WPT folder to the configuration
  sync    Download WPT files according to the configuration (default)
  update  Bump the pinned commit and re-sync, reporting broken patches
  edit    Restore one file to its synced state (pristine + patch) for editing
  save    Regenerate a file's patch from its on-disk edits

Examples:
  wptsync init                   Create wpt.json with the latest WPT commit
  wptsync add url/               Add all files from the url/ folder
  wptsync add encoding/          Add all files from encoding/ recursively
  wptsync                        Sync files using wpt.json
  wptsync sync -dry-run          Preview what would be synced
  wptsync update                 Bump to the latest WPT commit and re-sync
  wptsync edit common/sab.js     Restore a file before editing it
  wptsync save common/sab.js     Save on-disk edits as the file's patch

Run 'wptsync <command> -h' for more information on a command.
`

func main() {
	if len(os.Args) < 2 {
		runSyncCommand(os.Args[1:])
		return
	}

	switch os.Args[1] {
	case "init":
		runInitCommand(os.Args[2:])
	case "add":
		runAddCommand(os.Args[2:])
	case "sync":
		runSyncCommand(os.Args[2:])
	case "update":
		runUpdateCommand(os.Args[2:])
	case "edit":
		runEditCommand(os.Args[2:])
	case "save":
		runSaveCommand(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		// If the first argument looks like a flag, treat it as sync command
		if strings.HasPrefix(os.Args[1], "-") {
			runSyncCommand(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "wptsync: unknown command %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func runInitCommand(args []string) {
	initFlags := flag.NewFlagSet("init", flag.ExitOnError)
	initFlags.Usage = func() {
		fmt.Fprintln(initFlags.Output(), `Create a new wpt.json configuration file

Usage:
  wptsync init [options]

The init command fetches the latest commit SHA from the web-platform-tests
repository and creates a configuration file with an empty files list.

Options:`)
		initFlags.PrintDefaults()
	}
	configPath := initFlags.String("config", "wpt.json", "path to the configuration file to create")
	initFlags.Parse(args)

	if err := runInit(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync init: %v\n", err)
		os.Exit(1)
	}
}

func runAddCommand(args []string) {
	addFlags := flag.NewFlagSet("add", flag.ExitOnError)
	addFlags.Usage = func() {
		fmt.Fprintln(addFlags.Output(), `Add files from a WPT path to the configuration

Usage:
  wptsync add <path> [options]

The add command fetches files from the web-platform-tests repository and adds
entries to the configuration. You can specify a single .js file or a folder
(which will be scanned recursively for .js files). Files ending in .any.js
are mapped to .js in the destination path.

Arguments:
  <path>    Path in the WPT repository (e.g., url/, resources/testharness.js)

Options:`)
		addFlags.PrintDefaults()
	}
	configPath := addFlags.String("config", "wpt.json", "path to the configuration file")
	addFlags.Parse(args)

	if addFlags.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wptsync add: missing required path argument")
		addFlags.Usage()
		os.Exit(1)
	}

	wptPath := addFlags.Arg(0)
	if err := runAdd(*configPath, wptPath); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync add: %v\n", err)
		os.Exit(1)
	}
}

func runUpdateCommand(args []string) {
	updateFlags := flag.NewFlagSet("update", flag.ExitOnError)
	updateFlags.Usage = func() {
		fmt.Fprintln(updateFlags.Output(), `Bump the pinned commit and re-sync all files

Usage:
  wptsync update [options]

The update command fetches the latest WPT commit (or uses -commit), updates
the configuration, and re-syncs every enabled file. Patches that no longer
apply are reported at the end instead of aborting the run; fix those files
and run 'wptsync save <path>' to regenerate their patches.

Options:`)
		updateFlags.PrintDefaults()
	}
	configPath := updateFlags.String("config", "wpt.json", "path to the configuration file")
	commit := updateFlags.String("commit", "", "update to this commit SHA instead of the latest")
	updateFlags.Parse(args)

	if err := runUpdate(*configPath, *commit); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync update: %v\n", err)
		os.Exit(1)
	}
}

func runEditCommand(args []string) {
	editFlags := flag.NewFlagSet("edit", flag.ExitOnError)
	editFlags.Usage = func() {
		fmt.Fprintln(editFlags.Output(), `Restore one file to its synced state (pristine + patch) for editing

Usage:
  wptsync edit <path> [options]

The edit command re-downloads a single configured file at the pinned commit
and re-applies its patch, so you start editing from a known state. Edit the
file in place, then run 'wptsync save <path>' to update its patch.

Arguments:
  <path>    The file's dst (or src) path as listed in the configuration

Options:`)
		editFlags.PrintDefaults()
	}
	configPath := editFlags.String("config", "wpt.json", "path to the configuration file")
	editFlags.Parse(args)

	if editFlags.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wptsync edit: missing required path argument")
		editFlags.Usage()
		os.Exit(1)
	}

	if err := runEdit(*configPath, editFlags.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync edit: %v\n", err)
		os.Exit(1)
	}
}

func runSaveCommand(args []string) {
	saveFlags := flag.NewFlagSet("save", flag.ExitOnError)
	saveFlags.Usage = func() {
		fmt.Fprintln(saveFlags.Output(), `Regenerate a file's patch from its on-disk edits

Usage:
  wptsync save <path> [options]

The save command downloads the pristine file at the pinned commit, diffs it
against the file on disk, and writes the result to the file's patch (default:
patches/<dst>.patch), registering it in the configuration if needed. If the
file no longer differs from pristine, the patch is removed instead.

Arguments:
  <path>    The file's dst (or src) path as listed in the configuration

Options:`)
		saveFlags.PrintDefaults()
	}
	configPath := saveFlags.String("config", "wpt.json", "path to the configuration file")
	saveFlags.Parse(args)

	if saveFlags.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "wptsync save: missing required path argument")
		saveFlags.Usage()
		os.Exit(1)
	}

	if err := runSave(*configPath, saveFlags.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync save: %v\n", err)
		os.Exit(1)
	}
}

func runSyncCommand(args []string) {
	syncFlags := flag.NewFlagSet("sync", flag.ExitOnError)
	syncFlags.Usage = func() {
		fmt.Fprintln(syncFlags.Output(), `Download WPT files according to the configuration

Usage:
  wptsync sync [options]
  wptsync [options]

The sync command downloads files from the web-platform-tests repository
at the commit specified in the configuration file, and optionally applies
patches to customize them.

Options:`)
		syncFlags.PrintDefaults()
	}
	configPath := syncFlags.String("config", "wpt.json", "path to the WPT sync configuration file")
	skipPatching := syncFlags.Bool("skip-patches", false, "download files but do not apply any configured patches")
	dryRun := syncFlags.Bool("dry-run", false, "print the actions that would be taken without writing files")
	syncFlags.Parse(args)

	if err := runSync(*configPath, *skipPatching, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync sync: %v\n", err)
		os.Exit(1)
	}
}

const wptGitHubAPIURL = "https://api.github.com/repos/web-platform-tests/wpt/commits/master"

func runInit(configPath string) error {
	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file %q already exists", configPath)
	}

	fmt.Printf("Fetching latest WPT commit...\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	commit, err := fetchLatestCommit(ctx)
	if err != nil {
		return fmt.Errorf("fetch latest commit: %w", err)
	}

	cfg := config{
		Commit:    commit,
		TargetDir: "wpt",
		Files:     []fileSpec{},
	}

	if err := saveConfig(configPath, &cfg); err != nil {
		return err
	}

	fmt.Printf("Created %s with commit %s\n", configPath, commit)
	return nil
}

func saveConfig(path string, cfg *config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

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

func runAdd(configPath, wptPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Normalize the path: remove leading/trailing slashes
	wptPath = strings.Trim(wptPath, "/")

	fmt.Printf("Fetching file list from %s...\n", wptPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

		cfg.Files = append(cfg.Files, fileSpec{
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

	if err := saveConfig(configPath, cfg); err != nil {
		return err
	}

	fmt.Printf("Added %d files to %s\n", added, configPath)
	return nil
}

const wptGitHubTreesAPI = "https://api.github.com/repos/web-platform-tests/wpt/git/trees"

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

func runSync(configPath string, skipPatching, dryRun bool) error {
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

	if len(cfg.Files) == 0 {
		fmt.Println("No files configured to sync.")
		return nil
	}

	fmt.Printf("Syncing %d WPT files from %s at commit %s\n", len(cfg.Files), wptRepoRawURL, cfg.Commit)

	for _, file := range cfg.Files {
		if !file.isEnabled() {
			fmt.Printf(" - skipping %s (disabled)\n", file.Src)
			continue
		}
		if err := processFile(root, cfg, file, skipPatching, dryRun); err != nil {
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
	seen := make(map[string]string, len(c.Files))
	for _, f := range c.Files {
		if f.Src == "" || f.Dst == "" {
			return fmt.Errorf("config: file entries must set src and dst (src=%q dst=%q)", f.Src, f.Dst)
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

func processFile(root string, cfg *config, file fileSpec, skipPatching, dryRun bool) error {
	// Per-file timeout so a long file list never starves later downloads.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

// errPatchFailed marks git apply failures so update can keep going and report
// them all at the end instead of aborting on the first one.
var errPatchFailed = errors.New("git apply failed")

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
		return fmt.Errorf("%w: %v", errPatchFailed, err)
	}

	return nil
}

func runUpdate(configPath, commit string) error {
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

	if commit == "" {
		fmt.Println("Fetching latest WPT commit...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		commit, err = fetchLatestCommit(ctx)
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
	if err := saveConfig(configPath, cfg); err != nil {
		return err
	}

	var failed []string
	for _, file := range cfg.Files {
		if !file.isEnabled() {
			fmt.Printf(" - skipping %s (disabled)\n", file.Src)
			continue
		}
		err := processFile(root, cfg, file, false, false)
		if errors.Is(err, errPatchFailed) {
			fmt.Fprintf(os.Stderr, "   %v\n", err)
			failed = append(failed, file.Dst)
			continue
		}
		if err != nil {
			return err
		}
	}

	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\nPatches that no longer apply (files left pristine):\n")
		for _, dst := range failed {
			fmt.Fprintf(os.Stderr, " - %s\n", dst)
		}
		return fmt.Errorf("%d patch(es) failed to apply; edit the file(s) and run `wptsync save <path>` to regenerate them", len(failed))
	}

	fmt.Printf("Updated to commit %s\n", commit)
	return nil
}

func findFileSpec(cfg *config, filePath string) (*fileSpec, error) {
	p := strings.Trim(filePath, "/")
	for i := range cfg.Files {
		if cfg.Files[i].Dst == p || cfg.Files[i].Src == p {
			return &cfg.Files[i], nil
		}
	}
	return nil, fmt.Errorf("no config entry matches %q (compared against src and dst)", p)
}

func runEdit(configPath, filePath string) error {
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

	file, err := findFileSpec(cfg, filePath)
	if err != nil {
		return err
	}

	if err := processFile(root, cfg, *file, false, false); err != nil {
		return err
	}

	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))
	fmt.Printf("Restored %s to its synced state.\nEdit it, then run `wptsync save %s` to update its patch.\n", dest, file.Dst)
	return nil
}

func runSave(configPath, filePath string) error {
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

	file, err := findFileSpec(cfg, filePath)
	if err != nil {
		return err
	}

	dest := filepath.Join(root, cfg.TargetDir, filepath.FromSlash(file.Dst))
	if _, err := os.Stat(dest); err != nil {
		return fmt.Errorf("%s not found on disk; run `wptsync sync` first", dest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "wptsync-save-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pristine := filepath.Join(tmpDir, "pristine")
	src := strings.TrimLeft(file.Src, "/")
	url := fmt.Sprintf("%s/%s/%s", wptRepoRawURL, cfg.Commit, src)
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
		if err := saveConfig(configPath, cfg); err != nil {
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
		if err := saveConfig(configPath, cfg); err != nil {
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
