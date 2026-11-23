//go:build windows

package main

// The hover text: one native tracking tooltip, owned by the strip window and
// placed by hand under the box the pointer is on. Gio has no tooltips, and a
// native one costs nothing while hidden.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comctl32              = windows.NewLazySystemDLL("comctl32.dll")
	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	pCreateWindowExW      = user32.NewProc("CreateWindowExW")
	pSendMessageW         = user32.NewProc("SendMessageW")
	pGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmUser            = 0x0400
	ttmTrackActivate  = wmUser + 17
	ttmTrackPosition  = wmUser + 18
	ttmSetMaxTipWidth = wmUser + 24
	ttmAddToolW       = wmUser + 50
	ttmUpdateTipTextW = wmUser + 57
	ttfTrack          = 0x0020
	ttfAbsolute       = 0x0080
	ttsNoPrefix       = 0x02
	ttsAlwaysTip      = 0x01
	wsPopupStyle      = 0x80000000
	wsExTopmost       = 0x00000008
	wsExToolWindow    = 0x00000080
	iccWin95Classes   = 0x000000FF
)

// toolInfo is TTTOOLINFOW without lpReserved (TTTOOLINFOW_V2_SIZE), which
// both comctl32 v5 and v6 accept; this exe has no v6 manifest.
type toolInfo struct {
	size   uint32
	flags  uint32
	hwnd   uintptr
	id     uintptr
	rect   struct{ L, T, R, B int32 }
	hinst  uintptr
	text   *uint16
	lParam uintptr
}

type tooltip struct {
	hwnd  uintptr
	owner uintptr
	ti    toolInfo
}

// ensure makes the tooltip window the first time it is needed.
func (t *tooltip) ensure(owner uintptr) bool {
	if t.hwnd != 0 {
		return true
	}
	icc := struct{ size, classes uint32 }{8, iccWin95Classes}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	class, _ := windows.UTF16PtrFromString("tooltips_class32")
	hinst, _, _ := pGetModuleHandleW.Call(0)
	h, _, _ := pCreateWindowExW.Call(wsExTopmost|wsExToolWindow, uintptr(unsafe.Pointer(class)), 0,
		wsPopupStyle|ttsNoPrefix|ttsAlwaysTip, 0, 0, 0, 0, owner, 0, hinst, 0)
	if h == 0 {
		return false
	}
	t.hwnd, t.owner = h, owner
	t.ti = toolInfo{flags: ttfTrack | ttfAbsolute, hwnd: owner, id: 1, hinst: hinst}
	t.ti.size = uint32(unsafe.Sizeof(t.ti))
	empty, _ := windows.UTF16PtrFromString(" ")
	t.ti.text = empty
	pSendMessageW.Call(h, ttmAddToolW, 0, uintptr(unsafe.Pointer(&t.ti)))
	pSendMessageW.Call(h, ttmSetMaxTipWidth, 0, 520)
	return true
}

// show puts text at screen pixel (x, y). Window thread only.
func (t *tooltip) show(owner uintptr, x, y int, text string) {
	if !t.ensure(owner) {
		return
	}
	p, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	t.ti.text = p
	pSendMessageW.Call(t.hwnd, ttmUpdateTipTextW, 0, uintptr(unsafe.Pointer(&t.ti)))
	pSendMessageW.Call(t.hwnd, ttmTrackPosition, 0, uintptr(uint32(uint16(int16(x)))|uint32(uint16(int16(y)))<<16))
	pSendMessageW.Call(t.hwnd, ttmTrackActivate, 1, uintptr(unsafe.Pointer(&t.ti)))
}

// hide takes the tooltip away. Window thread only.
func (t *tooltip) hide() {
	if t.hwnd != 0 {
		pSendMessageW.Call(t.hwnd, ttmTrackActivate, 0, uintptr(unsafe.Pointer(&t.ti)))
	}
}

var pGetCursorPos = user32.NewProc("GetCursorPos")

// cursorPos is the cursor in screen pixels.
func cursorPos() (x, y int, ok bool) {
	var p struct{ X, Y int32 }
	res, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y), res != 0
}

// windowRect is hwnd's rect in screen pixels. GetWindowRect sends no
// message, so it is safe off the window thread.
func windowRect(hwnd uintptr) (l, t, r, b int, ok bool) {
	var rc struct{ L, T, R, B int32 }
	res, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	return int(rc.L), int(rc.T), int(rc.R), int(rc.B), res != 0
}
