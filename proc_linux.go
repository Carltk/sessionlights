package main

// Linux process probes, all from /proc: whether a pid is alive, what it is,
// when it started, and which claude process owns this hook run. Wayland gives
// a client no window handles, so the terminal-window probes return nothing.

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// clockTicks is USER_HZ. It is 100 on every Linux ABI Go targets.
const clockTicks = 100

type linuxProc struct{ root string }

func procInfo() Proc { return linuxProc{root: "/proc"} }

// statFields is /proc/<pid>/stat after the command name. The name sits in
// parentheses and may hold spaces or ')' itself, so the split is at the last
// ')'. Index 0 is field 3 (state).
func statFields(b []byte) (comm string, rest []string, ok bool) {
	lp := bytes.IndexByte(b, '(')
	rp := bytes.LastIndexByte(b, ')')
	if lp < 0 || rp < lp {
		return "", nil, false
	}
	return string(b[lp+1 : rp]), strings.Fields(string(b[rp+1:])), true
}

// bootMs is the boot time in unix ms, from the btime line of /proc/stat.
func bootMs(root string) int64 {
	b, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			s, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return s * 1000
		}
	}
	return 0
}

// stat returns a pid's command name, parent pid and start time in unix ms.
func (p linuxProc) stat(pid int) (comm string, ppid int, startMs int64, ok bool) {
	b, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, 0, false
	}
	comm, f, ok := statFields(b)
	if !ok || len(f) < 20 {
		return "", 0, 0, false
	}
	ppid, _ = strconv.Atoi(f[1])
	// Field 22 (starttime) is ticks since boot; f[0] is field 3.
	ticks, _ := strconv.ParseInt(f[19], 10, 64)
	if boot := bootMs(p.root); boot != 0 {
		startMs = boot + ticks*1000/clockTicks
	}
	return comm, ppid, startMs, true
}

// Info implements Proc. A zombie counts as gone.
func (p linuxProc) Info(pid int) (bool, string, int64) {
	if pid <= 0 {
		return false, "", 0
	}
	b, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false, "", 0
	}
	_, f, ok := statFields(b)
	if ok && len(f) > 0 && (f[0] == "Z" || f[0] == "X") {
		return false, "", 0
	}
	comm, _, start, ok := p.stat(pid)
	if !ok {
		return true, "", 0 // it exists; an unreadable stat is no evidence either way
	}
	return true, comm, start
}

type linuxProbe struct{ proc linuxProc }

func newProbe() sessionProbe { return linuxProbe{proc: linuxProc{root: "/proc"}} }

// SessionPID walks up from this process to the nearest claude ancestor.
func (l linuxProbe) SessionPID() (int, int64) {
	pid := os.Getpid()
	for i := 0; i < 32; i++ { // bounded, as on Windows
		comm, ppid, start, ok := l.proc.stat(pid)
		if !ok {
			return 0, 0
		}
		if isClaudeExe(comm) {
			return pid, start
		}
		if ppid <= 1 || ppid == pid {
			return 0, 0
		}
		pid = ppid
	}
	return 0, 0
}

func (linuxProbe) TerminalWindow(int) *Window  { return nil }
func (linuxProbe) ForegroundTerminal() *Window { return nil }
