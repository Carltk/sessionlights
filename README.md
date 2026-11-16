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

- **Drag** the grip on the left edge to move the strip. A pinned strip keeps one position everywhere; an unpinned one remembers a position per virtual desktop.
- **Click** a session to bring its terminal to the front. If the terminal is on another virtual desktop, Windows switches to it.
- **Drag** a session sideways to reorder it. Sessions are grouped by the virtual desktop their terminal is on, in Task View order, so a session moves only within its own desktop's group. The order is shared by every strip.
- **Right-click** to close.
- A **ring** marks sessions whose terminal is on the desktop you are looking at. It updates within a second of a switch when pinned (see `-vd`), a few seconds otherwise.
- The blue **badge** after the grip is the number of the desktop you are looking at (pinned) or the strip sits on (unpinned), plus its name if you have renamed it in Task View. The small number before each group is that group's desktop.

With `-vd` one strip is pinned to every desktop: it follows you and keeps one position for every desktop. Without it, one strip runs per virtual desktop, so you start one on each desktop where you want it. Either way a second strip that would duplicate one does nothing: the new copy exits by itself.

Each box is labelled with the session's title if you have given it one with Claude Code's built-in `/rename` (or `claude --name`), otherwise with the folder the session runs in.

## Install as a Claude Code plugin

The repo is a Claude Code [plugin](https://code.claude.com/docs/en/plugins). Inside Claude Code:

```
/plugin marketplace add Carltk/sessionlights
/plugin install sessionlights@sessionlights
```

On Claude Code 2.1.275 or later, `/plugin install sessionlights --marketplace Carltk/sessionlights` does both in one step.

That gives you everything, with no Go and nothing else to install:

- **The hook.** Every session reports its state to the strip from then on. Sessions that were already open start reporting once they are restarted.
- **The `/lights` skill.** Type `/lights`, or ask Claude to "show the session lights", to start a strip on the desktop you are on.
- **The exe**, `bin/sessionlights.exe`, which is both the strip and the hook.

To start a strip at login, put a shortcut to the plugin's `bin/sessionlights.exe` in `shell:startup`. `/plugin` shows where the plugin is installed.

## Build it yourself

You need [Go](https://go.dev/dl/) 1.26 or later.

```
git clone https://github.com/Carltk/sessionlights
cd sessionlights
GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-H windowsgui -s -w" -o bin/sessionlights.exe .
```

Those commands are for Git Bash. In PowerShell, set `$env:GOARCH="amd64"; $env:CGO_ENABLED="0"` first. `-H windowsgui` stops a console window opening alongside the strip, and when the exe runs as a hook. `-trimpath` keeps your own folder names out of the binary. Close any running strip before rebuilding, because Windows won't replace a running exe.

To use your build as the plugin, point Claude Code at the folder: `claude --plugin-dir <path to the clone>` for one session, or `/plugin marketplace add <path to the clone>` to install it.

## Where the lights come from

The strip only displays sessions. Something has to report them: a Claude Code [hook](https://code.claude.com/docs/en/hooks) that writes one JSON file per session and updates it as the session changes state. The plugin's hook is `sessionlights hook`, which reads the event Claude Code passes on stdin:

| Event | Light |
|---|---|
| `SessionStart` | idle |
| `UserPromptSubmit` | busy (tool failure count reset) |
| `PostToolUseFailure` | busy, with "N tool failures" as detail |
| `Notification` asking for permission or input | waiting |
| `Stop` | idle |
| `StopFailure` | failed, with the error as detail |
| `SessionEnd` | file removed |

It always exits 0 and stays silent, so a broken light can never block a session. Any other producer that writes the same format works too.

Hook and strip both use `~/.claude/session-lights/` (inside `CLAUDE_CONFIG_DIR` if you set it). Set `SESSIONLIGHTS_DIR` to move the hook's folder, and pass the strip `-dir <folder>` to match. Each file looks like this:

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

Saved positions go next to the lights folder, as `session-lights-go-pinned.json` (pinned) or `session-lights-go-<desktop-id>.json`, and the dragged order as `session-lights-order.json`.

## Flags

- `-dir <folder>` reads light files from another folder (default `~/.claude/session-lights`).
- `-vd <VirtualDesktop.exe>` pins one strip to every desktop and makes clicking a session switch desktop first. It needs [MScholtes/VirtualDesktop](https://github.com/MScholtes/VirtualDesktop): `VirtualDesktop.exe` on Windows 10, `VirtualDesktop11.exe` on Windows 11, `VirtualDesktop11-24H2.exe` on 24H2 and later. The desktop in front is read from Explorer's `SessionInfo<n>VirtualDesktopsCurrentVirtualDesktop` registry value, which Explorer rewrites on every switch.
- `-log <file>` writes a debug log: the desktop id, position restores, and the frame count every 5 seconds.

## Notes for hacking on it

- `lights.go` is the platform-neutral view model and is covered by `go test`.
- `win_windows.go` is the Windows glue: process checks, `IVirtualDesktopManager`, focusing and positioning. Desktop order and names are not in that interface, so they come from Explorer's `VirtualDesktops` registry key.
- `ui.go` is the Gio layout, and `main.go` wires it together.
- `hook.go` is the producer (`sessionlights hook`), covered by `go test`; `hook_windows.go` finds the session's `claude.exe` and the terminal window hosting its console.
- Build with `GOARCH=amd64` spelled out if your environment might set another architecture; a 32-bit build fails at runtime.

Gio v0.10.3 on Windows has a few traps, worked around here:

- `app.TopMost(true)` stops the window going top-most at all, so top-most is set on the window handle instead.
- Calling a message-sending Win32 function from the `w.Event()` loop deadlocks, because the window thread is waiting on that loop. Such calls go through `go w.Run(...)`.
- A drag area on top of clickable content swallows the clicks, which is why the strip drags from a grip.

Linux isn't supported yet. Only the window glue would need a Linux counterpart, and on Wayland a normal window can't place itself or stay on top.

## Licence

MIT. See [LICENSE](LICENSE).
