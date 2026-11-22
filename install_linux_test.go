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
