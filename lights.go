package main

import (
	"bytes"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Light is one session's state file. The format is described in the README.
type Light struct {
	SessionID string  `json:"sessionId"`
	Name      string  `json:"name"`
	Cwd       string  `json:"cwd"`
	State     string  `json:"state"`
	Since     string  `json:"since"`
	Detail    string  `json:"detail"`
	Window    *Window `json:"window"`
	PID       int     `json:"pid"`
	PIDStart  int64   `json:"pidStart"`

	modTime time.Time
}

// Window is the terminal hosting a session.
type Window struct {
	HWND int64 `json:"hwnd"`
	PID  int   `json:"pid"`
}

// Box is what the strip draws for one session: everything already decided,
// so the UI does no thinking and two equal views mean nothing to redraw.
type Box struct {
	Key    string
	Name   string
	Sub    string
	Dot    color.NRGBA
	HWND   int64
	OnHere bool
	Desk   int // 1-based virtual desktop number; 0 when unknown
}

// View is the whole strip. Empty means "no sessions".
type View struct {
	Boxes    []Box
	Here     int    // number of the desktop this strip sits on; 0 when unknown
	HereName string // that desktop's name, when it has been given one
}

func (v View) Equal(o View) bool {
	if v.Here != o.Here || v.HereName != o.HereName || len(v.Boxes) != len(o.Boxes) {
		return false
	}
	for i := range v.Boxes {
		if v.Boxes[i] != o.Boxes[i] {
			return false
		}
	}
	return true
}

// Deliberately not pure hues: on a dark strip the saturated versions vibrate.
var (
	colBusy    = rgb(0xE0A33E)
	colWarn    = rgb(0xE8742C)
	colIdle    = rgb(0x3FB950)
	colWaiting = rgb(0xF04747)
	colFailed  = rgb(0xD0304A)
	colDim     = rgb(0x5A5F6A)
)

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xFF}
}

const (
	warnAfter    = 300 * time.Second
	staleNoPID   = 30 * time.Minute
	pidStartSlop = 2000 // ms
	maxName      = 23
	maxSub       = 27
)

// Proc answers questions about live processes. Real on Windows, faked in tests.
type Proc interface {
	// Info reports whether pid is alive, its executable base name, and its
	// creation time in unix ms (0 if unreadable).
	Info(pid int) (alive bool, exe string, startMs int64)
}

// Desktops answers which virtual desktop a window is on.
type Desktops interface {
	OnCurrent(hwnd int64) bool
	// DesktopOf is the id of the desktop hwnd lives on, "" when unknown.
	DesktopOf(hwnd int64) string
}

// DeskInfo is the desktops in the order Task View shows them, by id.
type DeskInfo struct {
	IDs   []string
	Names map[string]string
}

// Number is the 1-based position of desktop id, 0 when it is not listed.
func (d DeskInfo) Number(id string) int {
	for i, x := range d.IDs {
		if strings.EqualFold(x, id) {
			return i + 1
		}
	}
	return 0
}

// LoadLights reads every light file in dir. A file mid-write is skipped: a
// missed frame beats a crash, and the next change event re-reads it.
func LoadLights(dir string) []Light {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Light
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Windows PowerShell's utf8 writes a BOM, which encoding/json rejects.
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		var l Light
		if json.Unmarshal(b, &l) != nil {
			continue
		}
		if info, err := e.Info(); err == nil {
			l.modTime = info.ModTime()
		}
		out = append(out, l)
	}
	return out
}

// Alive decides whether a light still belongs to a running session.
//
// A light file outlives its session whenever SessionEnd never fires - a
// reboot, a crash, a closed terminal. The hook sweeps them, but only while
// some session is alive to run it, so the viewer checks too.
func Alive(l Light, p Proc, now time.Time) bool {
	if l.PID == 0 {
		return now.Sub(l.modTime) < staleNoPID
	}
	alive, exe, start := p.Info(l.PID)
	if !alive {
		return false
	}
	// PIDs are recycled, so the PID existing is not the session existing.
	// PID plus creation time is the exact identity, so when both are known the
	// name is not consulted: a session that self-updated keeps running under a
	// renamed binary, and the image path follows the rename.
	if l.PIDStart != 0 && start != 0 {
		d := start - l.PIDStart
		return d > -pidStartSlop && d < pidStartSlop
	}
	// No start time to compare: the name is the only guard against a reused
	// PID. An unreadable name is not evidence either way.
	if exe != "" && !isClaudeExe(exe) {
		return false
	}
	return true // never hide a session on a failed probe
}

// isClaudeExe is the fallback identity check. It accepts a self-updated
// session too: Claude Code updates by renaming the running binary to
// claude.exe.old.<ms>.<pid>.
func isClaudeExe(exe string) bool {
	return exe == "claude" || exe == "claude.exe" || strings.HasPrefix(exe, "claude.exe.old.")
}

// BuildView turns light files into the strip's contents, ordered by desktop
// number, then by the order you dragged them into, then by name. here is the
// id of the desktop the strip itself sits on.
func BuildView(lights []Light, p Proc, d Desktops, desks DeskInfo, here string, order []string, now time.Time) View {
	var boxes []Box
	for _, l := range lights {
		if !Alive(l, p, now) {
			continue
		}
		b := Box{
			Key:  l.SessionID,
			Name: truncate(l.Name, maxName),
			Dot:  dotColour(l, now),
		}
		sub := l.Detail
		if sub == "" {
			sub = l.State
		}
		b.Sub = truncate(sub, maxSub)
		if l.Window != nil && l.Window.HWND != 0 {
			b.HWND = l.Window.HWND
			b.OnHere = d.OnCurrent(l.Window.HWND)
			b.Desk = desks.Number(d.DesktopOf(l.Window.HWND))
		}
		boxes = append(boxes, b)
	}
	SortBoxes(boxes, order)
	return View{Boxes: boxes, Here: desks.Number(here), HereName: desks.Names[strings.ToUpper(here)]}
}

// SortBoxes orders boxes by desktop (unknown last), then by their place in
// order, then by name. A session missing from order goes after the ones in it.
func SortBoxes(boxes []Box, order []string) {
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}
	pos := func(b Box) int {
		if r, ok := rank[b.Key]; ok {
			return r
		}
		return len(order)
	}
	desk := func(b Box) int {
		if b.Desk == 0 {
			return 1 << 30
		}
		return b.Desk
	}
	sort.SliceStable(boxes, func(i, j int) bool {
		a, b := boxes[i], boxes[j]
		if desk(a) != desk(b) {
			return desk(a) < desk(b)
		}
		if pos(a) != pos(b) {
			return pos(a) < pos(b)
		}
		return a.Name < b.Name
	})
}

// Move returns keys with key moved to index to. The caller re-sorts, so a
// move past the edge of its desktop's group just lands at that edge.
func Move(keys []string, key string, to int) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != key {
			out = append(out, k)
		}
	}
	if to < 0 {
		to = 0
	}
	if to > len(out) {
		to = len(out)
	}
	out = append(out[:to], append([]string{key}, out[to:]...)...)
	return out
}

func dotColour(l Light, now time.Time) color.NRGBA {
	switch l.State {
	case "idle":
		return colIdle
	case "waiting":
		return colWaiting
	case "failed":
		return colFailed
	case "busy":
		if since, err := time.Parse(time.RFC3339, l.Since); err == nil && now.Sub(since) >= warnAfter {
			return colWarn
		}
		return colBusy
	}
	return colDim
}

// truncate cuts on runes, keeping the whole thing within max runes.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}
