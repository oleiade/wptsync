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

### 3. Configuration (`wpt.json`)

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

### 4. Sync Files

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

### 5. Getting Help

View available commands and examples:

```bash
wptsync -h
```

Get help for a specific command:

```bash
wptsync init -h
wptsync sync -h
```

## Creating Patches

To create a patch for a WPT file:

1. Make changes to the file locally.
2. Generate a diff using `git diff`.

```bash
git diff > patches/my-fix.patch
```

Ensure the patch file is referenced in your `wpt.json` configuration. `wptsync` uses `git apply` to apply these patches.

