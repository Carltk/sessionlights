# SessionLights

A tiny always-on-top strip for Windows that shows what each of your [Claude Code](https://code.claude.com) sessions is doing: one box per session, with a dot coloured by its state. You can see at a glance which session is waiting on you without cycling through terminal tabs.

| Dot | Meaning |
|---|---|
| amber | busy: a turn is running |
| orange | busy for more than 5 minutes |
| green | idle: finished, nothing wanted |
| red | waiting on you (permission prompt or question) |
| deep red | the turn failed on an API error |

It is written in Go with [Gio](https://gioui.org), builds without a C compiler, and redraws only when something changes. At idle it uses about 56 MB of RAM and no measurable CPU.

## Using it

- **Drag** the grip on the left edge to move the strip. Its position is remembered per virtual desktop.
- **Click** a session to bring its terminal to the front. If the terminal is on another virtual desktop, Windows switches to it.
- **Right-click** to close.
- A **ring** marks sessions whose terminal is on the desktop you are looking at. It updates a few seconds after you switch desktop.

One strip runs per virtual desktop, so you start one on each desktop where you want it. Starting a second strip on a desktop that already has one does nothing: the new copy exits by itself. The `/lights` skill below does this from inside Claude Code.

## Install

You need [Go](https://go.dev/dl/) 1.26 or later.

```
git clone https://github.com/Carltk/sessionlights
cd sessionlights
GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H windowsgui -s -w" -o sessionlights.exe .
mkdir -p ~/.claude/bin
cp sessionlights.exe ~/.claude/bin/
```

Those commands are for Git Bash. In PowerShell, set `$env:GOARCH="amd64"; $env:CGO_ENABLED="0"` first and use `Copy-Item`. `-H windowsgui` stops a console window opening alongside the strip. Close any running strip before copying over it, because Windows won't replace a running exe.

To start a strip at login, put a shortcut to `sessionlights.exe` in `shell:startup`.

## Install the `/lights` skill into Claude Code

The repo ships a Claude Code [skill](https://code.claude.com/docs/en/skills) that starts a strip on your current desktop. Copy its folder into your personal skills directory:

```
mkdir -p ~/.claude/skills
cp -r skill/lights ~/.claude/skills/
```

In PowerShell: `Copy-Item -Recurse skill\lights $HOME\.claude\skills\`.

Claude Code watches the skills directory, so the skill is available straight away, even in a session that is already open. Type `/lights`, or ask Claude to "show the session lights". The skill expects the exe at `~/.claude/bin/sessionlights.exe`; if you put it somewhere else, edit the path in `SKILL.md`.

To give one project the skill without installing it for every session, copy the folder into that project's `.claude/skills/` instead.

## Where the lights come from

SessionLights only displays sessions. Something else has to report them: a Claude Code [hook](https://code.claude.com/docs/en/hooks) that writes one JSON file per session and updates it as the session changes state. Without one, the strip just says "no sessions". It was built for the SessionLight hook in [PAI](https://github.com/danielmiessler/LifeOS) (Personal AI Infrastructure) setups, and any hook that writes this format works.

By default it reads `~/.claude/PAI/MEMORY/STATE/session-lights/`; pass `-dir <folder>` to read elsewhere. Each file looks like this:

```json
{
  "sessionId": "0f8e2c1a-5b7d-4e3f-9a61-2d4c8b0e7f13",
  "name": "My project",
  "cwd": "C:\\code\\my-project",
  "state": "waiting",
  "since": "2026-09-30T10:19:37+10:00",
  "detail": "Claude needs your permission to use Bash",
  "window": { "hwnd": 66466, "pid": 11740 },
  "pid": 2196,
  "pidStart": 1790724369056
}
```

| Field | Required | Meaning |
|---|---|---|
| `sessionId` | yes | stable id; one file per session |
| `name` | yes | shown in bold, cut to 23 characters |
| `state` | yes | `busy`, `idle`, `waiting` or `failed`; anything else shows a grey dot |
| `since` | for `busy` | RFC 3339 time the state began; turns amber to orange after 5 minutes |
| `detail` | no | second line, cut to 27 characters; falls back to the state |
| `window.hwnd` | no | the terminal's top-level window handle; enables click-to-focus and the ring |
| `pid` | no | the `claude.exe` process; its death hides the box |
| `pidStart` | no | that process's start time in unix ms, so a reused PID is not mistaken for the session |

A file without a `pid` is hidden once it hasn't been modified for 30 minutes. Files are re-read as soon as they change, and a half-written file is skipped until the next write.

Saved positions go next to the lights folder, as `session-lights-go-<desktop-id>.json`.

## Flags

- `-dir <folder>` reads light files from another folder.
- `-log <file>` writes a debug log: the desktop id, position restores, and the frame count every 5 seconds.

## Notes for hacking on it

- `lights.go` is the platform-neutral view model and is covered by `go test`.
- `win_windows.go` is the Windows glue: process checks, `IVirtualDesktopManager`, focusing and positioning.
- `ui.go` is the Gio layout, and `main.go` wires it together.
- Build with `GOARCH=amd64` spelled out if your environment might set another architecture; a 32-bit build fails at runtime.

Gio v0.10.3 on Windows has a few traps, worked around here:

- `app.TopMost(true)` stops the window going top-most at all, so top-most is set on the window handle instead.
- Calling a message-sending Win32 function from the `w.Event()` loop deadlocks, because the window thread is waiting on that loop. Such calls go through `go w.Run(...)`.
- A drag area on top of clickable content swallows the clicks, which is why the strip drags from a grip.

Linux isn't supported yet. Only the window glue would need a Linux counterpart, and on Wayland a normal window can't place itself or stay on top.

## Licence

MIT. See [LICENSE](LICENSE).
