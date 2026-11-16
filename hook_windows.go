package main

// Windows half of hook mode: find the claude.exe that owns this session and
// the terminal window it is typed into.
//
// The terminal is not in the process ancestry: under ConPTY the chain stops at
// whatever launched the shell. The console reaches it, though. The session's
// claude.exe is attached to a pseudo-console whose window the terminal makes
// an owned window of the host window, and re-owns when a tab is dragged
// elsewhere. Attaching to that console and asking for the owner gives the
// exact window. This exe is a GUI-subsystem binary, so it has no console of
// its own to detach from and attaching opens nothing on screen.

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pAttachConsole    = kernel32.NewProc("AttachConsole")
	pFreeConsole      = kernel32.NewProc("FreeConsole")
	pGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	pGetWindow        = user32.NewProc("GetWindow")
)

const gwOwner = 4

func procInfo() Proc { return winProc{} }

type winProbe struct{}

func (winProbe) SessionPID() (int, int64) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, 0
	}
	defer windows.CloseHandle(snap)

	type entry struct {
		parent uint32
		exe    string
	}
	procs := map[uint32]entry{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		procs[pe.ProcessID] = entry{pe.ParentProcessID, strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))}
	}

	pid := uint32(windows.GetCurrentProcessId())
	for i := 0; i < 32; i++ { // bounded: a PID cycle in a stale snapshot must not spin
		e, ok := procs[pid]
		if !ok {
			return 0, 0
		}
		if isClaudeExe(e.exe) {
			_, _, start := winProc{}.Info(int(pid))
			return int(pid), start
		}
		if e.parent == 0 || e.parent == pid {
			return 0, 0
		}
		pid = e.parent
	}
	return 0, 0
}

func (winProbe) TerminalWindow(pid int) *Window {
	if pid == 0 {
		return nil
	}
	pFreeConsole.Call()
	if r, _, _ := pAttachConsole.Call(uintptr(pid)); r == 0 {
		return nil
	}
	con, _, _ := pGetConsoleWindow.Call()
	owner, _, _ := pGetWindow.Call(con, gwOwner)
	pFreeConsole.Call()

	h := owner
	if h == 0 && con != 0 && visible(con) {
		h = con // classic conhost: the console window is the terminal
	}
	if h == 0 {
		return nil
	}
	return &Window{HWND: int64(h), PID: int(windowPID(h))}
}

// terminals are the hosts a foreground window may belong to. Anything else in
// front when a prompt arrives (a remote prompt, a scheduled one) is ignored
// and the last known window kept.
var terminals = map[string]bool{
	"windowsterminal.exe": true, "openconsole.exe": true, "conhost.exe": true,
	"cmd.exe": true, "powershell.exe": true, "pwsh.exe": true,
	"wezterm-gui.exe": true, "alacritty.exe": true, "mintty.exe": true,
	"tabby.exe": true, "hyper.exe": true, "code.exe": true, "cursor.exe": true,
}

func (winProbe) ForegroundTerminal() *Window {
	h, _, _ := pGetForegroundWindow.Call()
	if h == 0 {
		return nil
	}
	pid := windowPID(h)
	_, exe, _ := winProc{}.Info(int(pid))
	if !terminals[filepath.Base(exe)] {
		return nil
	}
	return &Window{HWND: int64(h), PID: int(pid)}
}
