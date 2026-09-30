package main

import (
	"bytes"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"sort"
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
}

// View is the whole strip. Empty means "no sessions".
type View struct {
	Boxes []Box
}

func (v View) Equal(o View) bool {
	if len(v.Boxes) != len(o.Boxes) {
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

// Desktops answers whether a window is on the desktop in front of you.
type Desktops interface {
	OnCurrent(hwnd int64) bool
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
	// An unreadable name is not evidence either way.
	if exe != "" && exe != "claude" && exe != "claude.exe" {
		return false
	}
	if l.PIDStart == 0 || start == 0 {
		return true // cannot compare: never hide a session on a failed probe
	}
	d := start - l.PIDStart
	return d > -pidStartSlop && d < pidStartSlop
}

// BuildView turns light files into the strip's contents.
func BuildView(lights []Light, p Proc, d Desktops, now time.Time) View {
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
		}
		boxes = append(boxes, b)
	}
	sort.SliceStable(boxes, func(i, j int) bool { return boxes[i].Name < boxes[j].Name })
	return View{Boxes: boxes}
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
