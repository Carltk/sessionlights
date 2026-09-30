---
name: lights
description: Start the SessionLights strip on the Windows virtual desktop you are on now. USE WHEN lights, session lights, show the lights, start the strip, traffic lights, session status strip. NOT FOR changing what a session reports (that is the hook writing the light files).
---

# lights

Start the SessionLights strip on the desktop that is active right now. A new
window opens on the active desktop, and Claude Code runs on the desktop the
user is typing in, so the strip lands where they are working.

Run this and report whether a `sessionlights` process is running afterwards:

```
powershell.exe -NoProfile -Command "Start-Process -FilePath (Join-Path $HOME '.claude\bin\sessionlights.exe')"
```

It is safe to run twice: a second strip on a desktop that already shows one
exits by itself, so "nothing new appeared" is a success, not a failure.

If the file is missing, tell the user to build and install it (see the
SessionLights README) rather than searching the disk for another copy.
