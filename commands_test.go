package wptsync

import (
	"strings"
	"testing"
)

func TestRewritePatchPaths(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/tmp/wptsync-save-123/pristine b/wpt/common/sab.js",
		"index a3ea610..4cc21fb 100644",
		"--- a/tmp/wptsync-save-123/pristine",
		"+++ b/wpt/common/sab.js",
		"@@ -1,3 +1,3 @@",
		" context line",
		"--- removed line that looks like a header",
		"+++ added line that looks like a header",
	}, "\n")

	got := string(rewritePatchPaths([]byte(diff), "wpt/common/sab.js"))

	want := strings.Join([]string{
		"diff --git a/wpt/common/sab.js b/wpt/common/sab.js",
		"index a3ea610..4cc21fb 100644",
		"--- a/wpt/common/sab.js",
		"+++ b/wpt/common/sab.js",
		"@@ -1,3 +1,3 @@",
		" context line",
		"--- removed line that looks like a header",
		"+++ added line that looks like a header",
	}, "\n")

	if got != want {
		t.Errorf("rewritePatchPaths mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestValidate(t *testing.T) {
	base := Config{Commit: "abc", TargetDir: "wpt"}

	ok := base
	ok.Files = []FileSpec{{Src: "a.js", Dst: "a.js"}, {Src: "b.js", Dst: "sub/b.js"}}
	if err := ok.validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	traversal := base
	traversal.Files = []FileSpec{{Src: "a.js", Dst: "../evil.js"}}
	if err := traversal.validate(); err == nil {
		t.Error("expected error for dst escaping target_dir")
	}

	dup := base
	dup.Files = []FileSpec{{Src: "a.any.js", Dst: "a.js"}, {Src: "a.js", Dst: "a.js"}}
	if err := dup.validate(); err == nil {
		t.Error("expected error for duplicate dst")
	}

	empty := base
	empty.Files = []FileSpec{{Src: "", Dst: "a.js"}}
	if err := empty.validate(); err == nil {
		t.Error("expected error for empty src")
	}
}

func TestFindFileSpec(t *testing.T) {
	cfg := &Config{
		Files: []FileSpec{
			{Src: "common/sab.any.js", Dst: "common/sab.js"},
		},
	}

	if _, err := findFileSpec(cfg, "common/sab.js"); err != nil {
		t.Errorf("lookup by dst: %v", err)
	}
	if _, err := findFileSpec(cfg, "common/sab.any.js"); err != nil {
		t.Errorf("lookup by src: %v", err)
	}
	if _, err := findFileSpec(cfg, "/common/sab.js/"); err != nil {
		t.Errorf("lookup with surrounding slashes: %v", err)
	}
	if _, err := findFileSpec(cfg, "nope.js"); err == nil {
		t.Error("expected error for unknown path")
	}

	// The returned pointer must alias the config so mutations stick.
	spec, _ := findFileSpec(cfg, "common/sab.js")
	spec.Patch = "patches/common/sab.js.patch"
	if cfg.Files[0].Patch != "patches/common/sab.js.patch" {
		t.Error("findFileSpec did not return a pointer into cfg.Files")
	}
}
