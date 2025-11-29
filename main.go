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

Examples:
  wptsync init                   Create wpt.json with the latest WPT commit
  wptsync add url/               Add all files from the url/ folder
  wptsync add encoding/          Add all files from encoding/ recursively
  wptsync                        Sync files using wpt.json
  wptsync sync -dry-run          Preview what would be synced

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

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
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
		if strings.HasSuffix(dst, ".any.js") {
			dst = strings.TrimSuffix(dst, ".any.js") + ".js"
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

	// Write updated config
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("Added %d files to %s\n", added, configPath)
	return nil
}

const wptGitHubContentsAPI = "https://api.github.com/repos/web-platform-tests/wpt/contents"

func listFilesInPath(ctx context.Context, commit, pathPrefix string) ([]string, error) {
	var files []string
	if err := listFilesRecursive(ctx, commit, pathPrefix, &files); err != nil {
		return nil, err
	}
	return files, nil
}

type githubItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

func listFilesRecursive(ctx context.Context, commit, path string, files *[]string) error {
	url := fmt.Sprintf("%s/%s?ref=%s", wptGitHubContentsAPI, path, commit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("path %q not found in repository", path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Check if response is an array (directory) or object (single file)
	// by looking at the first non-whitespace character
	trimmed := bytes.TrimLeft(body, " \t\n\r")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		// Single file object
		var singleItem githubItem
		if err := json.Unmarshal(body, &singleItem); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// Add it if it's a .js file
		if singleItem.Type == "file" && strings.HasSuffix(singleItem.Path, ".js") {
			*files = append(*files, singleItem.Path)
		}
		return nil
	}

	// Directory listing (array)
	var items []githubItem
	if err := json.Unmarshal(body, &items); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	for _, item := range items {
		if item.Type == "file" {
			// Only include .js files
			if strings.HasSuffix(item.Path, ".js") {
				*files = append(*files, item.Path)
			}
		} else if item.Type == "dir" {
			if err := listFilesRecursive(ctx, commit, item.Path, files); err != nil {
				return err
			}
		}
	}

	return nil
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
