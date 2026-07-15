// Command wptsync syncs files from the web-platform-tests repository into a
// local project per a wpt.json configuration.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/oleiade/wptsync"
)

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

	if err := wptsync.Init(context.Background(), *configPath); err != nil {
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
	if err := wptsync.Add(context.Background(), *configPath, wptPath); err != nil {
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

	if err := wptsync.Update(context.Background(), *configPath, *commit); err != nil {
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

	if err := wptsync.Edit(context.Background(), *configPath, editFlags.Arg(0)); err != nil {
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

	if err := wptsync.Save(context.Background(), *configPath, saveFlags.Arg(0)); err != nil {
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
	force := syncFlags.Bool("force", false, "bypass the freshness stamp and force a full sync")
	syncFlags.Parse(args)

	opts := &wptsync.SyncOptions{
		SkipPatches: *skipPatching,
		DryRun:      *dryRun,
		Force:       *force,
		Logf:        func(format string, args ...any) { fmt.Printf(format, args...) },
	}

	if err := wptsync.Sync(context.Background(), *configPath, opts); err != nil {
		fmt.Fprintf(os.Stderr, "wptsync sync: %v\n", err)
		os.Exit(1)
	}
}
