//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Everything here that touches our own window runs on the Gio window thread
// via w.Run. Calling a message-sending user32 function from the event
// goroutine deadlocks: that thread is blocked handing us the current event.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pGetWindowRect       = user32.NewProc("GetWindowRect")
	pMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	pEnumWindows         = user32.NewProc("EnumWindows")
	pGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	pIsWindowVisible     = user32.NewProc("IsWindowVisible")
	pIsWindow            = user32.NewProc("IsWindow")
	pIsIconic            = user32.NewProc("IsIconic")
	pShowWindow          = user32.NewProc("ShowWindow")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	pAttachThreadInput   = user32.NewProc("AttachThreadInput")
	pGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
	pCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	pCoCreateInstance    = ole32.NewProc("CoCreateInstance")
)

const (
	hwndTopmost    = ^uintptr(0) // (HWND)-1
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
	swpNoZOrder    = 0x0004
	swpNoActivate  = 0x0010
	swRestore      = 9
	monitorNearest = 2
	coinitMulti    = 0x0
	clsctxInproc   = 0x1
	clsctxLocal    = 0x4
	stripTitle     = "PAI Lights"
	queryLimited   = windows.PROCESS_QUERY_LIMITED_INFORMATION
	unixEpochIn100 = 116444736000000000 // FILETIME of 1970-01-01
)

// ---- processes -----------------------------------------------------------

type winProc struct{}

func (winProc) Info(pid int) (bool, string, int64) {
	h, err := windows.OpenProcess(queryLimited, false, uint32(pid))
	if err != nil {
		// Access denied still means the process exists; anything else is gone.
		return err == windows.ERROR_ACCESS_DENIED, "", 0
	}
	defer windows.CloseHandle(h)

	var code uint32
	if windows.GetExitCodeProcess(h, &code) == nil && code != 259 { // STILL_ACTIVE
		return false, "", 0
	}

	exe := ""
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		exe = strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:n])))
	}

	var created, exit, kern, user windows.Filetime
	var start int64
	if windows.GetProcessTimes(h, &created, &exit, &kern, &user) == nil {
		ft := int64(created.HighDateTime)<<32 | int64(created.LowDateTime)
		start = (ft - unixEpochIn100) / 10000
	}
	return true, exe, start
}

// ---- virtual desktops (IVirtualDesktopManager: public and documented) ----

var (
	clsidVDM = windows.GUID{Data1: 0xaa509086, Data2: 0x5ca9, Data3: 0x4c25, Data4: [8]byte{0x8f, 0x95, 0x58, 0x9d, 0x3c, 0x07, 0xb4, 0x8a}}
	iidVDM   = windows.GUID{Data1: 0xa5cd92ff, Data2: 0x29be, Data3: 0x454c, Data4: [8]byte{0x8d, 0x04, 0xd8, 0x28, 0x79, 0xfb, 0x3f, 0x1b}}
)

// vdm owns the COM object on one locked MTA thread; callers post requests.
type vdm struct {
	req chan func(obj unsafe.Pointer)
}

func newDesktops() *vdm {
	d := &vdm{req: make(chan func(unsafe.Pointer))}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		pCoInitializeEx.Call(0, coinitMulti)
		var obj unsafe.Pointer
		hr, _, _ := pCoCreateInstance.Call(
			uintptr(unsafe.Pointer(&clsidVDM)), 0, clsctxInproc|clsctxLocal,
			uintptr(unsafe.Pointer(&iidVDM)), uintptr(unsafe.Pointer(&obj)))
		if hr != 0 {
			log.Printf("IVirtualDesktopManager unavailable: hr=%#x", hr)
		}
		close(ready)
		for f := range d.req {
			f(obj)
		}
	}()
	<-ready
	return d
}

// vtbl returns method slot of a COM object's vtable (slots 0-2 are IUnknown).
func vtbl(obj unsafe.Pointer, slot int) uintptr {
	return (*[8]uintptr)(*(*unsafe.Pointer)(obj))[slot]
}

// OnCurrent reports whether hwnd is on the desktop in front of you. A dead or
// unknown handle is "not here", which only costs it the ring.
func (d *vdm) OnCurrent(hwnd int64) bool {
	out := make(chan bool, 1)
	d.req <- func(obj unsafe.Pointer) {
		if obj == nil {
			out <- false
			return
		}
		var on int32
		hr, _, _ := syscall.SyscallN(vtbl(obj, 3), uintptr(obj), uintptr(hwnd), uintptr(unsafe.Pointer(&on)))
		out <- hr == 0 && on != 0
	}
	return <-out
}

// DesktopID names the desktop hwnd lives on, for keying its saved position.
func (d *vdm) DesktopID(hwnd uintptr) string {
	out := make(chan string, 1)
	d.req <- func(obj unsafe.Pointer) {
		var g windows.GUID
		if obj == nil {
			out <- ""
			return
		}
		hr, _, _ := syscall.SyscallN(vtbl(obj, 4), uintptr(obj), hwnd, uintptr(unsafe.Pointer(&g)))
		if hr != 0 {
			out <- ""
			return
		}
		out <- strings.Trim(g.String(), "{}")
	}
	return <-out
}

// DesktopOf is DesktopID for a session's terminal.
func (d *vdm) DesktopOf(hwnd int64) string { return d.DesktopID(uintptr(hwnd)) }

// The desktop order and names are not in IVirtualDesktopManager. Explorer
// keeps them in the registry: VirtualDesktopIDs is the GUIDs back to back in
// Task View order, and a renamed desktop has a Name value under its GUID.
const vdKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\VirtualDesktops`

func readDesks() DeskInfo {
	info := DeskInfo{Names: map[string]string{}}
	k, err := registry.OpenKey(registry.CURRENT_USER, vdKey, registry.QUERY_VALUE)
	if err != nil {
		return info
	}
	raw, _, err := k.GetBinaryValue("VirtualDesktopIDs")
	k.Close()
	if err != nil {
		return info
	}
	for i := 0; i+16 <= len(raw); i += 16 {
		g := *(*windows.GUID)(unsafe.Pointer(&raw[i])) // same byte layout as the struct
		id := strings.ToUpper(strings.Trim(g.String(), "{}"))
		info.IDs = append(info.IDs, id)
		if nk, err := registry.OpenKey(registry.CURRENT_USER, vdKey+`\Desktops\{`+id+`}`, registry.QUERY_VALUE); err == nil {
			if name, _, err := nk.GetStringValue("Name"); err == nil && name != "" {
				info.Names[id] = name
			}
			nk.Close()
		}
	}
	return info
}

// The desktop in front of you is not in IVirtualDesktopManager either.
// Explorer writes it, per logon session, as CurrentVirtualDesktop under
// SessionInfo\<session>\VirtualDesktops, and rewrites it on every switch.
func currentDeskKey() string {
	var sid uint32
	if windows.ProcessIdToSessionId(uint32(os.Getpid()), &sid) != nil {
		sid = 1
	}
	return fmt.Sprintf(`Software\Microsoft\Windows\CurrentVersion\Explorer\SessionInfo\%d\VirtualDesktops`, sid)
}

// currentDesktop is the id of the desktop in front of you, "" when unknown.
func currentDesktop() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, currentDeskKey(), registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	raw, _, err := k.GetBinaryValue("CurrentVirtualDesktop")
	if err != nil || len(raw) != 16 {
		return ""
	}
	g := *(*windows.GUID)(unsafe.Pointer(&raw[0]))
	return strings.ToUpper(strings.Trim(g.String(), "{}"))
}

// onDesktopSwitch calls f each time Explorer records a new current desktop.
// It blocks in RegNotifyChangeKeyValue, so it costs nothing while you stay
// put; run it on its own goroutine.
func onDesktopSwitch(f func()) {
	k, err := registry.OpenKey(registry.CURRENT_USER, currentDeskKey(), registry.NOTIFY|registry.QUERY_VALUE)
	if err != nil {
		log.Printf("desktop switch watch: %v", err)
		return
	}
	defer k.Close()
	for {
		if err := windows.RegNotifyChangeKeyValue(windows.Handle(k), false, windows.REG_NOTIFY_CHANGE_LAST_SET, 0, false); err != nil {
			log.Printf("desktop switch watch: %v", err)
			return
		}
		f()
	}
}

// ---- VirtualDesktop.exe: what the documented interface cannot do ---------

// vdExe is MScholtes/VirtualDesktop (github.com/MScholtes/VirtualDesktop),
// the build matching this Windows. It pins our window to every desktop and
// switches desktops directly. Empty means "not available": the strip then
// runs one instance per desktop, as it did before.
type vdExe string

func (v vdExe) run(args ...string) error {
	if v == "" {
		return fmt.Errorf("no VirtualDesktop.exe")
	}
	cmd := exec.Command(string(v), append([]string{"/q"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	err := cmd.Run()
	// It answers with the pipeline value as its exit code; only 127 is failure.
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 127 {
		return nil
	}
	return err
}

func (v vdExe) pin(hwnd uintptr) error { return v.run(fmt.Sprintf("/pwh:%d", hwnd)) }

// switchTo brings forward the desktop window hwnd sits on.
func (v vdExe) switchTo(hwnd int64) error {
	return v.run(fmt.Sprintf("/gdfwh:%d", hwnd), "/s")
}

// ---- windows -------------------------------------------------------------

// enumCallbacks are never freed by Go, so build the visitors once and reuse.
var (
	enumMu     sync.Mutex
	enumTarget func(h uintptr) bool
	enumCB     = syscall.NewCallback(func(h, _ uintptr) uintptr {
		if enumTarget(h) {
			return 1
		}
		return 0
	})
)

func eachWindow(visit func(h uintptr) bool) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumTarget = visit
	pEnumWindows.Call(enumCB, 0)
}

func windowPID(h uintptr) uint32 {
	var pid uint32
	pGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func windowTitle(h uintptr) string {
	buf := make([]uint16, 64)
	n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func visible(h uintptr) bool {
	v, _, _ := pIsWindowVisible.Call(h)
	return v != 0
}

// stripAlreadyHere is true when another process already shows a strip on
// the desktop in front of you. That includes the WPF strip, which is pinned
// to every desktop and so is always "here".
//
// anywhere widens it to every desktop: a pinned strip is the only strip.
func stripAlreadyHere(d Desktops, anywhere bool) bool {
	me := uint32(os.Getpid())
	found := false
	eachWindow(func(h uintptr) bool {
		if windowPID(h) != me && visible(h) && windowTitle(h) == stripTitle && (anywhere || d.OnCurrent(int64(h))) {
			found = true
			return false
		}
		return true
	})
	return found
}

// setTopmost pins the window above others. app.TopMost is not used: on Gio
// v0.10.3 it leaves the window unable to go top-most at all.
func setTopmost(hwnd uintptr) {
	pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
}

func windowPos(hwnd uintptr) (x, y int, ok bool) {
	var r struct{ L, T, R, B int32 }
	res, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return int(r.L), int(r.T), res != 0
}

// monitorTop is the top of the work area on the monitor hwnd is mostly on, so
// a taskbar along the top is left uncovered.
func monitorTop(hwnd uintptr) (int, bool) {
	m, _, _ := pMonitorFromWindow.Call(hwnd, monitorNearest)
	if m == 0 {
		return 0, false
	}
	var mi struct {
		Size      uint32
		Mon, Work struct{ L, T, R, B int32 }
		Flags     uint32
	}
	mi.Size = uint32(unsafe.Sizeof(mi))
	res, _, _ := pGetMonitorInfoW.Call(m, uintptr(unsafe.Pointer(&mi)))
	return int(mi.Work.T), res != 0
}

func moveWindow(hwnd uintptr, x, y int) {
	pSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

// activate brings a terminal window to the front. Windows switches to that
// window's virtual desktop as a side effect, which is the only documented
// way to switch desktops.
func activate(target int64) bool {
	h := uintptr(target)
	if ok, _, _ := pIsWindow.Call(h); h == 0 || ok == 0 {
		return false
	}
	if ic, _, _ := pIsIconic.Call(h); ic != 0 {
		pShowWindow.Call(h, swRestore)
	}
	if r, _, _ := pSetForegroundWindow.Call(h); r != 0 {
		return true
	}
	// Refused. Borrow the foreground thread's input state, which makes this
	// thread eligible to set the foreground.
	fg, _, _ := pGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	fgThread, _, _ := pGetWindowThreadPID.Call(fg, 0)
	me, _, _ := pGetCurrentThreadId.Call()
	if fgThread == me {
		return false
	}
	pAttachThreadInput.Call(me, fgThread, 1)
	defer pAttachThreadInput.Call(me, fgThread, 0)
	pShowWindow.Call(h, swRestore)
	r, _, _ := pSetForegroundWindow.Call(h)
	return r != 0
}

// systemFonts loads Segoe UI so the strip matches the rest of Windows, plus
// the clock's face. Missing files fall back to Gio's bundled Go fonts.
func systemFonts() []font.FontFace {
	dir := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	var faces []font.FontFace
	for _, f := range []struct {
		file, typeface string
		w              font.Weight
	}{
		{"segoeui.ttf", "Segoe UI", font.Normal},
		{"seguisb.ttf", "Segoe UI", font.SemiBold},
		{clockFile, clockFace, font.Normal},
	} {
		b, err := os.ReadFile(filepath.Join(dir, f.file))
		if err != nil {
			continue
		}
		face, err := opentype.Parse(b)
		if err != nil {
			continue
		}
		faces = append(faces, font.FontFace{Font: font.Font{Typeface: font.Typeface(f.typeface), Weight: f.w}, Face: face})
	}
	return faces
}
