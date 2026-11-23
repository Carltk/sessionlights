---
name: lights
description: Start the SessionLights viewer for this machine. On Windows that is the always-on-top strip on the virtual desktop you are on now; on Linux it is the stats bar in the top panel. USE WHEN lights, session lights, show the lights, start the strip, start the bar, traffic lights, session status strip. NOT FOR renaming a session's light (use the built-in /rename) or changing what a session reports (the plugin's hook does that).
---

# lights

Start the SessionLights viewer. The plugin puts its `bin/` folder on the
shell's PATH, so `sessionlights` (Linux) and `sessionlights.exe` (Windows) run
without a path. If the command is not found, the binaries are in
`${CLAUDE_PLUGIN_ROOT}/bin/`; use the full path instead.

Starting it twice is safe: a second copy that would duplicate a running one
exits by itself, so "nothing new appeared" is a success, not a failure.

Pick the section for the OS you are running on, run it, then report whether a
`sessionlights` process is running.

## Windows

A new strip opens on the active virtual desktop, and Claude Code runs on the
desktop the user is typing in, so the strip lands where they are working.

```
powershell.exe -NoProfile -Command "Start-Process -FilePath (Get-Command sessionlights.exe).Source"
```

To pin one strip to every desktop instead, the user needs MScholtes
VirtualDesktop.exe (see the README); add
`-ArgumentList '-vd "<path to VirtualDesktop.exe>"'` to the command. Keep the
double quotes round the path: Start-Process joins its arguments with spaces,
so a path with a space in it would otherwise split.

## Linux

The bar lives in the top panel (GNOME with the AppIndicator extension, KDE, or
any StatusNotifierItem tray). Run `install` first, every time: it is quick and
idempotent, and it refreshes the copy of the binary that the login autostart
runs, so autostart picks up plugin updates.

```
sessionlights install && setsid -f sessionlights >/dev/null 2>&1
```

Tell the user what `install` printed. The first time it enables a small GNOME
Shell extension that only takes effect after they log out and back in; until
then the bar works but double-clicking a session cannot raise its terminal.
If it suggests a kitty `listen_on` line, pass that on too.

If the process runs but the user sees no bar, the desktop has no tray host.
Plain GNOME (Fedora, Arch) needs the AppIndicator and KStatusNotifierItem
Support extension; Ubuntu ships it enabled.

## Both

The lights show sessions started after the plugin was installed. Each light is
labelled with the session's `/rename` title, else its folder name.
