package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// fakeProcTree writes /proc/<pid>/stat files: comm, state, ppid, starttime.
func fakeProcTree(t *testing.T, procs map[int][3]any) string {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "stat"), []byte("cpu  1 2 3 4\nbtime 1700000000\n"), 0o644)
	for pid, p := range procs {
		dir := filepath.Join(root, fmt.Sprint(pid))
		os.MkdirAll(dir, 0o755)
		// Fields 3..22: state ppid pgrp session tty tpgid flags minflt cminflt
		// majflt cmajflt utime stime cutime cstime priority nice threads
		// itrealvalue starttime.
		line := fmt.Sprintf("%d (%s) %s %d 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0\n",
			pid, p[0], p[1], p[2], 12345)
		os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o644)
	}
	return root
}

func TestLinuxProcInfo(t *testing.T) {
	root := fakeProcTree(t, map[int][3]any{
		100: {"claude", "S", 1},
		200: {"weird) name", "R", 100},
		300: {"claude", "Z", 1},
	})
	p := linuxProc{root: root}
	alive, exe, start := p.Info(100)
	wantStart := int64(1700000000)*1000 + 12345*1000/clockTicks
	if !alive || exe != "claude" || start != wantStart {
		t.Fatalf("Info(100) = %v %q %d, want true claude %d", alive, exe, start, wantStart)
	}
	if _, exe, _ := p.Info(200); exe != "weird) name" {
		t.Fatalf("comm with ')' parsed as %q", exe)
	}
	if alive, _, _ := p.Info(300); alive {
		t.Fatal("zombie reported alive")
	}
	if alive, _, _ := p.Info(999); alive {
		t.Fatal("missing pid reported alive")
	}
}

func TestLinuxAliveRejectsReusedPID(t *testing.T) {
	root := fakeProcTree(t, map[int][3]any{100: {"claude", "S", 1}, 101: {"bash", "S", 1}})
	p := linuxProc{root: root}
	_, _, start := p.Info(100)
	now := time.Now()
	if !Alive(Light{PID: 100, PIDStart: start}, p, now) {
		t.Fatal("live session hidden")
	}
	if Alive(Light{PID: 100, PIDStart: start - 60_000}, p, now) {
		t.Fatal("pid reused by a later process still shown")
	}
	if Alive(Light{PID: 101}, p, now) {
		t.Fatal("pid reused by a non-claude process still shown")
	}
	if Alive(Light{PID: 555, PIDStart: start}, p, now) {
		t.Fatal("dead pid shown")
	}
}

func TestLinuxProbeOnRealTree(t *testing.T) {
	// Under `go test` there is usually no claude ancestor; when there is (run
	// from a Claude Code session) it must be a live claude with a start time.
	pid, start := newProbe().SessionPID()
	if pid == 0 {
		t.Skip("no claude ancestor")
	}
	alive, exe, st := procInfo().Info(pid)
	if !alive || !isClaudeExe(exe) || st != start || start == 0 {
		t.Fatalf("pid %d: alive %v exe %q start %d vs %d", pid, alive, exe, st, start)
	}
}
