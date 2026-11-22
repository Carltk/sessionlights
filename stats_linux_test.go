package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	a, ok := parseProcStat([]byte("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3\n"))
	if !ok || a.total != 1000 || a.busy != 150 {
		t.Fatalf("got %+v %v", a, ok)
	}
	// 1000 more ticks, 250 of them busy: 25%.
	b, _ := parseProcStat([]byte("cpu  300 0 100 1500 100 0 0 0 0 0\n"))
	if got := cpuPct(a, b); math.Abs(got-25) > 1e-9 {
		t.Fatalf("cpuPct = %v, want 25", got)
	}
	if cpuPct(b, b) != -1 {
		t.Fatal("no elapsed ticks should be unknown")
	}
	if _, ok := parseProcStat([]byte("intr 1 2 3")); ok {
		t.Fatal("non-cpu line accepted")
	}
}

func TestParseMeminfo(t *testing.T) {
	used, total, ok := parseMeminfo([]byte("MemTotal:       16000000 kB\nMemFree:  1000 kB\nMemAvailable:    6000000 kB\n"))
	if !ok || total != 16000000*1024 || used != 10000000*1024 {
		t.Fatalf("used %d total %d ok %v", used, total, ok)
	}
	if _, _, ok := parseMeminfo([]byte("MemTotal: 10 kB\n")); ok {
		t.Fatal("missing MemAvailable accepted")
	}
}

func TestParseNetDev(t *testing.T) {
	in := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 5000  10    0    0    0     0          0         0 5000  10    0    0    0     0       0          0
wlp2s0: 1000  20    0    0    0     0          0         0  200  30    0    0    0     0       0          0
docker0: 7777  20    0    0    0     0          0         0  7777  30    0    0    0     0       0          0
enx01: 3000  1 0 0 0 0 0 0 300 1 0 0 0 0 0 0
`)
	only := map[string]bool{"wlp2s0": true, "enx01": true}
	n := parseNetDev(in, func(i string) bool { return only[i] })
	if n.rx != 4000 || n.tx != 500 {
		t.Fatalf("got %+v, want rx 4000 tx 500 (lo and docker0 excluded)", n)
	}
	if got := rate(1000, 3000, 2*time.Second); got != 1000 {
		t.Fatalf("rate = %v", got)
	}
	if rate(3000, 1000, time.Second) != -1 {
		t.Fatal("a counter going backwards should be unknown")
	}
}

func TestPhysicalIface(t *testing.T) {
	sys := t.TempDir()
	os.MkdirAll(filepath.Join(sys, "wlp2s0", "device"), 0o755)
	os.MkdirAll(filepath.Join(sys, "docker0"), 0o755)
	f := physicalIface(sys)
	if !f("wlp2s0") || f("docker0") || f("lo") || f("tailscale0") {
		t.Fatal("wrong interface selection")
	}
}

const fdinfoI915 = `pos:	0
flags:	02100002
drm-driver:	i915
drm-client-id:	18
drm-pdev:	0000:00:02.0
drm-total-system0:	527732 KiB
drm-engine-render:	%d ns
drm-engine-copy:	0 ns
drm-engine-video:	%d ns
drm-engine-capacity-video:	2
`

func TestParseFdinfoAndGPUPct(t *testing.T) {
	key, eng, ok := parseFdinfo([]byte(sprintf(fdinfoI915, 1000, 0)))
	if !ok || key != "i915/18" || eng["render"] != 1000 {
		t.Fatalf("key %q eng %v ok %v", key, eng, ok)
	}
	if _, ok := eng["capacity-video"]; ok {
		t.Fatal("capacity line read as an engine")
	}
	if _, _, ok := parseFdinfo([]byte("pos: 0\nflags: 1\n")); ok {
		t.Fatal("non-DRM fd accepted")
	}

	a := gpuSample{"i915/18": {"render": 1e9, "video": 0}, "i915/gone": {"render": 5e9}}
	b := gpuSample{"i915/18": {"render": 1.5e9, "video": 0.2e9}, "i915/new": {"render": 9e9}}
	// Over 2 s: render busy 0.5 s (25%), video 0.2 s (10%). Busiest engine wins;
	// a client that left or arrived is not counted.
	if got := gpuPct(a, b, 2*time.Second); math.Abs(got-25) > 1e-9 {
		t.Fatalf("gpuPct = %v, want 25", got)
	}
	back := gpuSample{"i915/18": {"render": 0}}
	if got := gpuPct(a, back, time.Second); got != 0 {
		t.Fatalf("backwards counter gave %v", got)
	}
}

func TestSamplerOnFakeProc(t *testing.T) {
	proc, sys := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(proc, "net"), 0o755)
	os.MkdirAll(filepath.Join(sys, "class", "net", "eth0", "device"), 0o755)
	os.MkdirAll(filepath.Join(proc, "42", "fd"), 0o755)
	os.MkdirAll(filepath.Join(proc, "42", "fdinfo"), 0o755)
	os.Symlink("/dev/dri/renderD128", filepath.Join(proc, "42", "fd", "7"))
	os.Symlink("/dev/null", filepath.Join(proc, "42", "fd", "8"))
	write := func(cpu string, rx, tx, render int) {
		os.WriteFile(filepath.Join(proc, "stat"), []byte(cpu), 0o644)
		os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal: 1000 kB\nMemAvailable: 250 kB\n"), 0o644)
		os.WriteFile(filepath.Join(proc, "net", "dev"), []byte(sprintf("h\nh\n  eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", rx, tx)), 0o644)
		os.WriteFile(filepath.Join(proc, "42", "fdinfo", "7"), []byte(sprintf(fdinfoI915, render, 0)), 0o644)
	}
	s := &sampler{proc: proc, sys: sys}
	t0 := time.Unix(1000, 0)
	write("cpu  0 0 0 100 0 0 0 0\n", 0, 0, 0)
	first := s.Sample(t0)
	if first.CPU != -1 || first.RxBps != -1 || first.GPU != -1 {
		t.Fatalf("first sample should have no rates: %+v", first)
	}
	if first.MemUsed != 750*1024 || first.MemTotal != 1000*1024 {
		t.Fatalf("mem %+v", first)
	}
	write("cpu  50 0 0 150 0 0 0 0\n", 4000, 2000, 1e9)
	got := s.Sample(t0.Add(2 * time.Second))
	if got.CPU != 50 || got.RxBps != 2000 || got.TxBps != 1000 || math.Abs(got.GPU-50) > 1e-9 {
		t.Fatalf("second sample %+v", got)
	}
	if len(s.drmFds) != 1 {
		t.Fatalf("drm fds %v, want only fd 7", s.drmFds)
	}
}

func TestKittyTarget(t *testing.T) {
	socks := []string{"@other-12", "@kitty-459237", "/run/user/1000/bus"}
	cases := []struct {
		env  map[string]string
		addr string
		id   int
		ok   bool
	}{
		{map[string]string{"KITTY_LISTEN_ON": "unix:@kitty-459237", "KITTY_WINDOW_ID": "3"}, "@kitty-459237", 3, true},
		{map[string]string{"KITTY_PID": "459237", "KITTY_WINDOW_ID": "1"}, "@kitty-459237", 1, true},
		{map[string]string{"KITTY_PID": "999", "KITTY_WINDOW_ID": "1"}, "", 0, false}, // kitty without listen_on
		{map[string]string{"KITTY_LISTEN_ON": "tcp:localhost:1234", "KITTY_WINDOW_ID": "3"}, "", 0, false},
		{map[string]string{"KITTY_LISTEN_ON": "unix:@kitty-1", "KITTY_WINDOW_ID": "x"}, "", 0, false},
		{nil, "", 0, false},
	}
	for _, c := range cases {
		addr, id, ok := kittyTarget(c.env, socks)
		if addr != c.addr || id != c.id || ok != c.ok {
			t.Errorf("%v: got %q %d %v", c.env, addr, id, ok)
		}
	}
}

func TestKittyCommandFraming(t *testing.T) {
	b, err := kittyCommand("focus-window", map[string]any{"match": "id:3"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "\x1bP@kitty-cmd{") || !strings.HasSuffix(s, "}\x1b\\") ||
		!strings.Contains(s, `"cmd":"focus-window"`) || !strings.Contains(s, `"match":"id:3"`) ||
		!strings.Contains(s, `"no_response":true`) {
		t.Fatalf("framing %q", s)
	}
}
