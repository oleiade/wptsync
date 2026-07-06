# wptsync

`wptsync` is a lightweight tool designed to synchronize specific files from the [Web Platform Tests (WPT)](https://github.com/web-platform-tests/wpt) repository into your local project. It allows you to track a specific commit of the WPT suite, selectively download files, and apply local patches to them.

## Why `wptsync`?

As we implement Web APIs support in **Sobek** (k6's JavaScript runtime), ensuring our implementations pass standard Web Platform Tests is crucial. However, directly using the massive WPT repository can be unwieldy.

`wptsync` simplifies this workflow by:
- **Pinning Versions**: It tracks a specific commit hash of the WPT suite, ensuring your tests run against a stable, known version of the specs.
- **Selective Syncing**: You only download the tests and resources relevant to the APIs you are implementing.
- **Patching Support**: It allows applying patches to the downloaded tests. This is essential for adapting browser-centric tests to work within the Sobek/k6 context or for temporarily fixing upstream issues.

## Features

- **Version Control**: Sync files from a specific git commit hash.
- **Bulk Import**: Add entire WPT folders to your config with a single command.
- **File Mapping**: Rename or relocate files from the WPT structure to your project structure.
- **Patching**: Automatically apply `git apply` compatible patches to downloaded files.
- **Toggleable**: Enable or disable specific file syncs directly in the config.
- **Dry Run**: Preview changes before applying them.

## Usage

### 1. Installation

You can run the tool directly using Go:

```bash
go run github.com/oleiade/wptsync
```

Or build/install it:

```bash
go install github.com/oleiade/wptsync
```

### 2. Initialize a Configuration

Create a new `wpt.json` configuration file with the latest WPT commit:

```bash
wptsync init
```

This fetches the latest commit SHA from the WPT repository and creates a configuration file:

```json
{
  "commit": "3a2402822007826e89a1dc4fd5534977cccd1753",
  "target_dir": "wpt",
  "files": []
}
```

You can also specify a custom config path:

```bash
wptsync init -config=my-wpt-config.json
```

### 3. Add Files from WPT

Instead of manually listing files, you can add `.js` files directly:

```bash
wptsync add resources/testharness.js   # Add a single file
wptsync add url/                        # Add all .js files from a folder
```

When adding a folder, it recursively fetches all `.js` files. Files with `.any.js` extensions are automatically mapped to `.js` in the destination path:

```
url/url-constructor.any.js  →  url/url-constructor.js
url/resources/setters.js    →  url/resources/setters.js
```

The command skips files that are already in the configuration, making it safe to run multiple times.

### 4. Configuration (`wpt.json`)

Edit the `wpt.json` file to define which files to sync. The file specifies the commit to check out, where to put the files, and which files to download.

```json
{
  "commit": "b5e12f331494f9533ef6211367dace2c88131fd7",
  "target_dir": "tests/wpt",
  "files": [
    {
      "src": "encoding/textdecoder-arguments.any.js",
      "dst": "encoding/textdecoder-arguments.js"
    },
    {
      "src": "resources/testharness.js",
      "dst": "resources/testharness.js",
      "patch": "patches/testharness.js.patch"
    },
    {
      "src": "encoding/resources/non-standard-labels.js",
      "dst": "encoding/resources/non-standard-labels.js",
      "enabled": false
    }
  ]
}
```

- **`commit`**: The full SHA of the WPT commit to sync from.
- **`target_dir`**: The local directory where files will be saved.
- **`files`**: A list of file objects:
  - `src`: Path in the WPT repository.
  - `dst`: Path relative to `target_dir` where the file should be saved.
  - `patch`: (Optional) Path to a local patch file to apply to the downloaded file.
  - `enabled`: (Optional) Set to `false` to skip syncing this file.

### 5. Sync Files

Download files based on your configuration:

```bash
wptsync sync
```

Or simply:

```bash
wptsync
```

**Options:**

- `-config <path>`: Use a different configuration file (default: `wpt.json`).
- `-dry-run`: Print what actions would be taken without writing files.
- `-skip-patches`: Download files but do not apply the configured patches.

```bash
wptsync sync -config=my-wpt-config.json -dry-run
```

### 6. Update the Pinned Commit

Move to a newer WPT commit and re-sync everything in one step:

```bash
wptsync update
```

This fetches the latest WPT commit (or use `-commit <sha>` to pin a specific one), updates `wpt.json`, and re-syncs every enabled file. Patches that no longer apply against the new commit are reported at the end instead of aborting the run; the affected files are left pristine so you can re-add your changes and run `wptsync save <path>` to regenerate their patches.

### 7. Getting Help

View available commands and examples:

```bash
wptsync -h
```

Get help for a specific command:

```bash
wptsync init -h
wptsync add -h
wptsync sync -h
wptsync update -h
wptsync edit -h
wptsync save -h
```

## Creating and Updating Patches

After a sync, each downloaded file on disk is the pristine WPT file with its patch (if any) applied. To create a new patch or update an existing one:

1. (Optional) Restore the file to a known state first:

   ```bash
   wptsync edit common/sab.js
   ```

2. Edit the file in `target_dir` directly.
3. Save your edits as a patch:

   ```bash
   wptsync save common/sab.js
   ```

The `save` command downloads the pristine file at the pinned commit, diffs it against your on-disk file, and writes the result to the file's patch (default: `patches/<dst>.patch`), registering it in `wpt.json` if it is new. Because the on-disk file already carries the previous patch, extending an existing patch is the same flow: edit, then `save`. If the file no longer differs from pristine, `save` removes the patch and its config reference.

Patches are standard `git apply` format, so you can still craft or adjust them by hand if you prefer.

