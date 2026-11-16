---
name: lights
description: Start the SessionLights strip on the Windows virtual desktop you are on now. USE WHEN lights, session lights, show the lights, start the strip, traffic lights, session status strip. NOT FOR renaming a session's light (use the built-in /rename) or changing what a session reports (the plugin's hook does that).
---

# lights

Start the SessionLights strip. A new window opens on the active virtual
desktop, and Claude Code runs on the desktop the user is typing in, so the
strip lands where they are working.

The plugin puts `sessionlights.exe` on the shell's PATH, so no path needs
quoting. Run this, then report whether a `sessionlights` process is running:

```
powershell.exe -NoProfile -Command "Start-Process -FilePath (Get-Command sessionlights.exe).Source"
```

If PowerShell says the command is not found, the exe is at
`${CLAUDE_PLUGIN_ROOT}/bin/sessionlights.exe`; start that file instead.

It is safe to run twice: a second strip on a desktop that already shows one
exits by itself, so "nothing new appeared" is a success, not a failure.

To pin one strip to every desktop instead, the user needs MScholtes
VirtualDesktop.exe (see the README); add
`-ArgumentList '-vd "<path to VirtualDesktop.exe>"'` to the command. Keep the
double quotes round the path: Start-Process joins its arguments with spaces,
so a path with a space in it would otherwise split.

The lights show sessions started after the plugin was installed. Each box is
labelled with the session's `/rename` title, else its folder name.
