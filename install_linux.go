package main

// `sessionlights install` sets the bar up for this user on Linux:
//
//   - the GNOME Shell focus helper extension (embedded in this binary),
//     copied into the user's extensions folder and enabled;
//   - an autostart entry, so the bar starts at login;
//   - a hint when kitty has no listen_on socket (clicks then raise the
//     window but cannot pick the tab).
//
// `sessionlights uninstall` removes the extension and the autostart entry.
// Everything is per-user; nothing needs root. Install is a one-off, so it may
// run gsettings; the bar itself never spawns anything.

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed gnome-extension/sessionlights@carltk.github.io
var extensionFS embed.FS

const extensionUUID = "sessionlights@carltk.github.io"

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func configHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func extensionDir() string {
	return filepath.Join(dataHome(), "gnome-shell", "extensions", extensionUUID)
}

func autostartFile() string {
	return filepath.Join(configHome(), "autostart", "sessionlights.desktop")
}

// desktopEntry is the autostart file. Exec values are quoted per the desktop
// entry spec, so a path with spaces survives.
func desktopEntry(exe, dir string) string {
	q := func(s string) string {
		r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", "$", `\\$`)
		return `"` + r.Replace(s) + `"`
	}
	execLine := q(exe)
	if dir != "" {
		execLine += " -dir " + q(dir)
	}
	return "[Desktop Entry]\nType=Application\nName=SessionLights\n" +
		"Comment=System stats and Claude Code session lights in the top bar\n" +
		"Exec=" + execLine + "\nX-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=5\nNoDisplay=true\n"
}

// withExtension adds uuid to a gsettings string-array literal, e.g.
// "['a@b', 'c@d']" or "@as []", leaving it alone when already present.
func withExtension(list, uuid string) (string, bool) {
	items := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(list, -1)
	var names []string
	for _, m := range items {
		if m[1] == uuid {
			return list, false
		}
		names = append(names, "'"+m[1]+"'")
	}
	names = append(names, "'"+uuid+"'")
	return "[" + strings.Join(names, ", ") + "]", true
}

// withoutExtension removes uuid from a gsettings string-array literal.
func withoutExtension(list, uuid string) (string, bool) {
	items := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(list, -1)
	var names []string
	removed := false
	for _, m := range items {
		if m[1] == uuid {
			removed = true
			continue
		}
		names = append(names, "'"+m[1]+"'")
	}
	return "[" + strings.Join(names, ", ") + "]", removed
}

// kittyHasListenOn reports whether a kitty.conf sets listen_on to something.
func kittyHasListenOn(conf string) bool {
	for _, line := range strings.Split(conf, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "listen_on" && f[1] != "none" {
			return true
		}
	}
	return false
}

// stableDir holds the copy of the binary that autostart runs when the
// installed binary lives in Claude Code's plugin cache.
func stableDir() string { return filepath.Join(dataHome(), "sessionlights") }

// stableExe returns the path autostart should run. A binary inside Claude
// Code's plugin cache sits in a per-version folder that an update deletes, so
// it is copied to stableDir (temp file + rename: a running bar keeps its old
// copy) and that copy is used. Any other path, e.g. ~/.local/bin, is used as is.
func stableExe(exe string) (string, error) {
	if !strings.Contains(filepath.ToSlash(exe), "/plugins/cache/") {
		return exe, nil
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(stableDir(), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(stableDir(), "sessionlights")
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func runInstall(dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if exe, err = stableExe(exe); err != nil {
		return fmt.Errorf("copy out of the plugin cache: %w", err)
	}
	// A bar started at login does not see CLAUDE_CONFIG_DIR or
	// SESSIONLIGHTS_DIR, so when either moves where the hook writes, autostart
	// is told the folder explicitly.
	if dir == "" && (os.Getenv("CLAUDE_CONFIG_DIR") != "" || os.Getenv("SESSIONLIGHTS_DIR") != "") {
		dir = lightsDir()
	}

	// 1. The shell extension.
	dst := extensionDir()
	sub, _ := fs.Sub(extensionFS, "gnome-extension/"+extensionUUID)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, p), b, 0o644)
	})
	if err != nil {
		return fmt.Errorf("extension: %w", err)
	}
	fmt.Println("installed GNOME Shell extension:", dst)

	// 2. Enable it. Wayland shells load a new extension at the next login.
	if out, err := exec.Command("gsettings", "get", "org.gnome.shell", "enabled-extensions").Output(); err == nil {
		if list, changed := withExtension(strings.TrimSpace(string(out)), extensionUUID); changed {
			if err := exec.Command("gsettings", "set", "org.gnome.shell", "enabled-extensions", list).Run(); err != nil {
				fmt.Println("could not enable the extension; run: gnome-extensions enable", extensionUUID)
			} else {
				fmt.Println("enabled the extension (takes effect at your next login)")
			}
		} else {
			fmt.Println("extension already enabled")
		}
	} else {
		fmt.Println("gsettings not found: not GNOME? The bar still works in any StatusNotifierItem tray; click-to-focus needs GNOME.")
	}

	// 3. Autostart.
	if err := os.MkdirAll(filepath.Dir(autostartFile()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(autostartFile(), []byte(desktopEntry(exe, dir)), 0o644); err != nil {
		return err
	}
	fmt.Println("autostart:", autostartFile())

	// 4. kitty hint.
	if b, err := os.ReadFile(filepath.Join(configHome(), "kitty", "kitty.conf")); err == nil && !kittyHasListenOn(string(b)) {
		fmt.Println("kitty: add `listen_on unix:@kitty` to kitty.conf (and restart kitty) so a click also picks the session's tab")
	}
	fmt.Println("start it now with:", exe, "&")
	return nil
}

func runUninstall() error {
	if out, err := exec.Command("gsettings", "get", "org.gnome.shell", "enabled-extensions").Output(); err == nil {
		if list, changed := withoutExtension(strings.TrimSpace(string(out)), extensionUUID); changed {
			_ = exec.Command("gsettings", "set", "org.gnome.shell", "enabled-extensions", list).Run()
		}
	}
	for _, p := range []string{extensionDir(), autostartFile(), stableDir()} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		fmt.Println("removed", p)
	}
	return nil
}
