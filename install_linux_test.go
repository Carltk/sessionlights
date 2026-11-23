package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithExtension(t *testing.T) {
	for in, want := range map[string]string{
		"@as []":         "['x@y']",
		"['a@b']":        "['a@b', 'x@y']",
		"['a@b', 'c@d']": "['a@b', 'c@d', 'x@y']",
	} {
		if got, changed := withExtension(in, "x@y"); got != want || !changed {
			t.Errorf("withExtension(%q) = %q %v", in, got, changed)
		}
	}
	if _, changed := withExtension("['x@y']", "x@y"); changed {
		t.Error("re-added an enabled extension")
	}
	if got, removed := withoutExtension("['a@b', 'x@y']", "x@y"); got != "['a@b']" || !removed {
		t.Errorf("withoutExtension = %q %v", got, removed)
	}
}

func TestKittyHasListenOn(t *testing.T) {
	if kittyHasListenOn("# listen_on none\nfont_size 11\n") || kittyHasListenOn("listen_on none\n") {
		t.Error("commented or none counted as set")
	}
	if !kittyHasListenOn("listen_on unix:@kitty\n") {
		t.Error("real listen_on missed")
	}
}

func TestDesktopEntryQuoting(t *testing.T) {
	e := desktopEntry("/opt/my apps/sessionlights", "/home/u/$lights")
	if !strings.Contains(e, `Exec="/opt/my apps/sessionlights" -dir "/home/u/\\$lights"`) {
		t.Fatalf("Exec line:\n%s", e)
	}
}

func TestInstallUninstallInTempHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("PATH", "") // no gsettings: the install must still lay down files
	if err := runInstall(""); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"extension.js", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(extensionDir(), f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(autostartFile()); err != nil {
		t.Errorf("no autostart: %v", err)
	}
	if err := runUninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extensionDir()); !os.IsNotExist(err) {
		t.Error("extension left behind")
	}
}

// A binary in Claude Code's plugin cache is copied to a stable path, because
// an update deletes its per-version folder; any other binary is used in place.
func TestStableExe(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	cached := filepath.Join(tmp, ".claude", "plugins", "cache", "m", "sessionlights", "1.1.0", "bin", "sessionlights")
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := stableExe(cached)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "data", "sessionlights", "sessionlights")
	if got != want {
		t.Fatalf("stableExe = %q, want %q", got, want)
	}
	st, err := os.Stat(got)
	if err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("copy not executable: %v %v", st, err)
	}
	// A later version replaces the copy.
	if err := os.WriteFile(cached, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := stableExe(cached); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(want); string(b) != "v2" {
		t.Errorf("copy = %q, want v2", b)
	}
	if p, _ := stableExe("/home/u/.local/bin/sessionlights"); p != "/home/u/.local/bin/sessionlights" {
		t.Errorf("non-cache path moved to %q", p)
	}
}

// With CLAUDE_CONFIG_DIR set the hook writes under it, so the autostarted bar
// (which will not see the variable) is given the folder.
func TestInstallPassesConfigDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("PATH", "")
	t.Setenv("SESSIONLIGHTS_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, "cc"))
	if err := runInstall(""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(autostartFile())
	if err != nil {
		t.Fatal(err)
	}
	if want := `-dir "` + filepath.Join(tmp, "cc", "session-lights") + `"`; !strings.Contains(string(b), want) {
		t.Errorf("autostart lacks %s:\n%s", want, b)
	}
}
