package wptsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newFixture starts an httptest.Server that serves content keyed by request
// path ("/<commit>/<src>") and returns it alongside a fresh temp directory
// and a function reporting how many requests the server has handled.
// Sub-tests that need to mutate content after Sync has run may do so
// directly on the map: the server only reads it while a Sync call is in
// flight, and tests never mutate it concurrently with one, so no extra
// locking is required for the map itself.
func newFixture(t *testing.T, content map[string]string) (server *httptest.Server, dir string, requestCount func() int) {
	t.Helper()

	var mu sync.Mutex
	count := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()

		body, ok := content[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, t.TempDir(), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// saveTestConfig writes cfg to <dir>/wpt.json and returns its path.
func saveTestConfig(t *testing.T, dir string, cfg *Config) string {
	t.Helper()
	configPath := filepath.Join(dir, "wpt.json")
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	return configPath
}

func TestSyncBasicFiles(t *testing.T) {
	content := map[string]string{
		"/c1/a/foo.js": "content A\n",
		"/c1/b/bar.js": "content B\n",
	}
	server, dir, _ := newFixture(t, content)

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files: []FileSpec{
			{Src: "a/foo.js", Dst: "renamed/foo.js"},
			{Src: "b/bar.js"}, // Dst omitted, defaults to Src.
		},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "wpt", "renamed", "foo.js"))
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(got) != content["/c1/a/foo.js"] {
		t.Errorf("renamed/foo.js = %q, want %q", got, content["/c1/a/foo.js"])
	}

	got, err = os.ReadFile(filepath.Join(dir, "wpt", "b", "bar.js"))
	if err != nil {
		t.Fatalf("read default-dst file: %v", err)
	}
	if string(got) != content["/c1/b/bar.js"] {
		t.Errorf("b/bar.js = %q, want %q", got, content["/c1/b/bar.js"])
	}
}

// newPatchFixture builds a fixture with one file entry whose patch changes
// "line2" to "line2-patched". Both the patch-application and skip-patches
// tests share it since they differ only in SyncOptions and assertions.
func newPatchFixture(t *testing.T) (server *httptest.Server, dir, configPath string) {
	t.Helper()

	const original = "line1\nline2\nline3\n"
	content := map[string]string{"/c1/patch/target.js": original}
	server, dir, _ = newFixture(t, content)

	patchRel := "patches/target.js.patch"
	patch := strings.Join([]string{
		"diff --git a/wpt/patch/target.js b/wpt/patch/target.js",
		"index 0000000..1111111 100644",
		"--- a/wpt/patch/target.js",
		"+++ b/wpt/patch/target.js",
		"@@ -1,3 +1,3 @@",
		" line1",
		"-line2",
		"+line2-patched",
		" line3",
		"",
	}, "\n")

	if err := os.MkdirAll(filepath.Join(dir, "patches"), 0o755); err != nil {
		t.Fatalf("mkdir patches: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, patchRel), []byte(patch), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files: []FileSpec{
			{Src: "patch/target.js", Dst: "patch/target.js", Patch: patchRel},
		},
	}
	configPath = saveTestConfig(t, dir, cfg)
	return server, dir, configPath
}

func TestSyncAppliesPatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	server, dir, configPath := newPatchFixture(t)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "wpt", "patch", "target.js"))
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	want := "line1\nline2-patched\nline3\n"
	if string(got) != want {
		t.Errorf("patched content = %q, want %q", got, want)
	}
}

func TestSyncSkipPatches(t *testing.T) {
	server, dir, configPath := newPatchFixture(t)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL, SkipPatches: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "wpt", "patch", "target.js"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	want := "line1\nline2\nline3\n"
	if string(got) != want {
		t.Errorf("SkipPatches: content = %q, want pristine %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(dir, "wpt", stampFileName)); !os.IsNotExist(err) {
		t.Errorf("SkipPatches: expected no stamp file, stat err = %v", err)
	}
}

func TestSyncDryRun(t *testing.T) {
	content := map[string]string{"/c1/a/foo.js": "content A\n"}
	server, dir, requestCount := newFixture(t, content)

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "a/foo.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL, DryRun: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "wpt")); !os.IsNotExist(err) {
		t.Errorf("DryRun: expected target dir to not exist, stat err = %v", err)
	}
	if requestCount() != 0 {
		t.Errorf("DryRun: expected no requests, got %d", requestCount())
	}
}

func TestSyncStampSkipsResync(t *testing.T) {
	content := map[string]string{"/c1/a/foo.js": "content A\n"}
	server, dir, requestCount := newFixture(t, content)

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "a/foo.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	firstCount := requestCount()
	if firstCount == 0 {
		t.Fatal("expected at least one request on first sync")
	}

	var log strings.Builder
	opts := &SyncOptions{
		BaseURL: server.URL,
		Logf:    func(format string, args ...any) { fmt.Fprintf(&log, format, args...) },
	}
	if err := Sync(context.Background(), configPath, opts); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	if requestCount() != firstCount {
		t.Errorf("expected no new requests on second sync, first=%d now=%d", firstCount, requestCount())
	}
	if !strings.Contains(log.String(), "up to date") {
		t.Errorf("expected up-to-date message in log, got %q", log.String())
	}
}

func TestSyncStampInvalidatesOnConfigChange(t *testing.T) {
	content := map[string]string{"/c1/a/foo.js": "content A v1\n"}
	server, dir, requestCount := newFixture(t, content)

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "a/foo.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	firstCount := requestCount()

	content["/c2/a/foo.js"] = "content A v2\n"
	cfg.Commit = "c2"
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	if requestCount() <= firstCount {
		t.Errorf("expected new downloads after commit change, first=%d now=%d", firstCount, requestCount())
	}
	got, err := os.ReadFile(filepath.Join(dir, "wpt", "a", "foo.js"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != "content A v2\n" {
		t.Errorf("content = %q, want updated content", got)
	}
}

func TestSyncForceRedownloads(t *testing.T) {
	content := map[string]string{"/c1/a/foo.js": "content A\n"}
	server, dir, requestCount := newFixture(t, content)

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "a/foo.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	firstCount := requestCount()

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL, Force: true}); err != nil {
		t.Fatalf("forced Sync: %v", err)
	}

	if requestCount() <= firstCount {
		t.Errorf("expected Force to trigger a new download, first=%d now=%d", firstCount, requestCount())
	}
}

func TestSyncSkipsDisabledEntry(t *testing.T) {
	content := map[string]string{
		"/c1/a/foo.js": "content A\n",
		"/c1/b/bar.js": "content B\n",
	}
	server, dir, requestCount := newFixture(t, content)

	disabled := false
	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files: []FileSpec{
			{Src: "a/foo.js"},
			{Src: "b/bar.js", Enabled: &disabled},
		},
	}
	configPath := saveTestConfig(t, dir, cfg)

	if err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "wpt", "b", "bar.js")); !os.IsNotExist(err) {
		t.Errorf("expected disabled file to not be synced, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wpt", "a", "foo.js")); err != nil {
		t.Errorf("expected enabled file to be synced: %v", err)
	}
	if requestCount() != 1 {
		t.Errorf("expected exactly one request, got %d", requestCount())
	}
}

func TestSyncErrorNamesFailingFile(t *testing.T) {
	server, dir, _ := newFixture(t, map[string]string{}) // every path 404s

	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "missing/file.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	err := Sync(context.Background(), configPath, &SyncOptions{BaseURL: server.URL})
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "missing/file.js") {
		t.Errorf("expected error to name the failing src, got %v", err)
	}
}

func TestLoadConfigDefaultsDst(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Commit:    "c1",
		TargetDir: "wpt",
		Files:     []FileSpec{{Src: "a/foo.js"}},
	}
	configPath := saveTestConfig(t, dir, cfg)

	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Files[0].Dst != "a/foo.js" {
		t.Errorf("Dst = %q, want %q (defaulted from Src)", loaded.Files[0].Dst, "a/foo.js")
	}
}
