// SessionLights is an always-on-top strip showing the state of every live PAI
// session: one box per session, coloured by what it is doing.
//
// It reads one JSON "light" file per session (see README) and redraws only
// when one changes. One instance runs per virtual desktop; a second launch on
// a desktop that already shows a strip exits straight away.
//
// Drag the grip on the left to move it. Click a session to jump to its
// terminal. Right-click to close.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/fsnotify/fsnotify"
)

// slowTick catches what no file change announces: a session process dying,
// a terminal being moved to another desktop.
const slowTick = 5 * time.Second

func main() {
	home, _ := os.UserHomeDir()
	// Default: where PAI's SessionLight hook writes. Any producer works.
	dir := flag.String("dir", filepath.Join(home, ".claude", "PAI", "MEMORY", "STATE", "session-lights"), "light files directory")
	logFile := flag.String("log", "", "write a debug log (frame count, events) to this file")
	flag.Parse()

	log.SetOutput(os.Stderr)
	if *logFile != "" {
		if f, err := os.Create(*logFile); err == nil {
			log.SetOutput(f)
		}
	}

	desks := newDesktops()
	if stripAlreadyHere(desks) {
		log.Print("a strip already shows on this desktop; exiting")
		return
	}

	// Saved positions sit beside the lights directory, never inside it, so
	// they are not mistaken for light files.
	s := &strip{dir: *dir, stateDir: filepath.Dir(*dir), desks: desks, proc: winProc{}}
	go func() {
		w := new(app.Window)
		// Not app.TopMost: see setTopmost.
		w.Option(app.Title(stripTitle), app.Decorated(false), app.Size(unit.Dp(160), unit.Dp(40)))
		s.w = w
		if err := s.run(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

type strip struct {
	dir, stateDir string
	desks         *vdm
	proc          Proc
	w             *app.Window

	view   atomic.Pointer[View]
	frames atomic.Int64

	mu      sync.Mutex
	hwnd    uintptr
	posFile string
	lastPos [2]int
}

func (s *strip) run() error {
	s.view.Store(&View{})
	ui := newUI(s)
	var ops op.Ops
	started := false
	for {
		switch e := s.w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.Win32ViewEvent:
			if e.Valid() && !started {
				started = true
				go s.attach(e.HWND)
			}
		case app.FrameEvent:
			s.frames.Add(1)
			gtx := app.NewContext(&ops, e)
			ui.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// attach does the once-per-window setup that needs the native handle, then
// starts watching for changes.
func (s *strip) attach(hwnd uintptr) {
	s.mu.Lock()
	s.hwnd = hwnd
	s.mu.Unlock()

	id := s.desks.DesktopID(hwnd)
	log.Printf("desktop=%q strip-on-current=%v", id, s.desks.OnCurrent(int64(hwnd)))
	if id == "" {
		id = "default"
	}
	s.posFile = filepath.Join(s.stateDir, "session-lights-go-"+id+".json")

	s.w.Run(func() {
		setTopmost(hwnd)
		if x, y, ok := s.loadPos(); ok {
			moveWindow(hwnd, x, y)
			log.Printf("restore %d,%d from %s", x, y, s.posFile)
		}
		s.lastPos[0], s.lastPos[1], _ = windowPos(hwnd)
	})

	s.refresh()
	go s.watch()
	go s.tick()
}

// refresh rebuilds the view and redraws only if it changed.
func (s *strip) refresh() {
	v := BuildView(LoadLights(s.dir), s.proc, s.desks, time.Now())
	if old := s.view.Load(); old != nil && old.Equal(v) {
		return
	}
	s.view.Store(&v)
	s.w.Invalidate()
}

func (s *strip) watch() {
	wt, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watcher: %v (falling back to the slow tick)", err)
		return
	}
	defer wt.Close()
	if err := wt.Add(s.dir); err != nil {
		log.Printf("watch %s: %v (falling back to the slow tick)", s.dir, err)
		return
	}
	// A hook writes a file in a few quick steps; wait for it to settle.
	var debounce <-chan time.Time
	for {
		select {
		case _, ok := <-wt.Events:
			if !ok {
				return
			}
			debounce = time.After(100 * time.Millisecond)
		case err, ok := <-wt.Errors:
			if !ok {
				return
			}
			log.Printf("watch error: %v", err)
		case <-debounce:
			debounce = nil
			s.refresh()
		}
	}
}

func (s *strip) tick() {
	last := int64(0)
	for range time.Tick(slowTick) {
		s.refresh()
		s.savePosIfMoved()
		if n := s.frames.Load(); n != last {
			log.Printf("frames=%d", n)
			last = n
		}
	}
}

// ---- actions from the UI (called off the event goroutine) --------------------

func (s *strip) focus(hwnd int64) {
	s.w.Run(func() { activate(hwnd) })
}

func (s *strip) close() {
	s.savePosIfMoved()
	s.w.Perform(system.ActionClose)
}

// ---- position, remembered per desktop ----------------------------------------

type pos struct{ X, Y int }

func (s *strip) loadPos() (int, int, bool) {
	b, err := os.ReadFile(s.posFile)
	if err != nil {
		return 0, 0, false
	}
	var p pos
	if json.Unmarshal(b, &p) != nil {
		return 0, 0, false
	}
	return p.X, p.Y, true
}

func (s *strip) savePosIfMoved() {
	s.mu.Lock()
	hwnd := s.hwnd
	s.mu.Unlock()
	if hwnd == 0 || s.posFile == "" {
		return
	}
	var x, y int
	var ok bool
	s.w.Run(func() { x, y, ok = windowPos(hwnd) })
	if !ok || (x == s.lastPos[0] && y == s.lastPos[1]) {
		return
	}
	s.lastPos = [2]int{x, y}
	b, _ := json.Marshal(pos{x, y})
	if err := os.WriteFile(s.posFile, b, 0o644); err != nil {
		log.Printf("save position: %v", err)
	}
}
