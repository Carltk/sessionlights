//go:build windows

package main

import (
	"math"
	"testing"
	"time"
)

func TestWinCPUPct(t *testing.T) {
	// Kernel time includes idle. 1000 ticks pass: kernel 600 (400 of it
	// idle), user 400, so 600 of 1000 were busy.
	a := winCPUTimes(1000, 2000, 1000)
	b := winCPUTimes(1400, 2600, 1400)
	if got := cpuPct(a, b); math.Abs(got-60) > 1e-9 {
		t.Errorf("cpuPct = %v, want 60", got)
	}
	if got := cpuPct(b, a); got != -1 {
		t.Errorf("backwards counters = %v, want -1", got)
	}
}

func TestCountIface(t *testing.T) {
	cases := []struct {
		flags uint8
		oper  uint32
		want  bool
	}{
		{ifHardware, 1, true},             // a NIC that is up
		{ifHardware, 2, false},            // a NIC that is down
		{ifHardware | ifFilter, 1, false}, // a WFP/QoS filter on top of it
		{0, 1, false},                     // loopback, vEthernet, tunnels
	}
	for _, c := range cases {
		if got := countIface(c.flags, c.oper); got != c.want {
			t.Errorf("countIface(%#x, %d) = %v, want %v", c.flags, c.oper, got, c.want)
		}
	}
}

func TestNetRates(t *testing.T) {
	a := map[uint64]netBytes{1: {rx: 1000, tx: 500}, 2: {rx: 5000, tx: 5000}}
	b := map[uint64]netBytes{
		1: {rx: 3000, tx: 1500},       // +2000 / +1000 over 2 s
		2: {rx: 10, tx: 10},           // counters reset: skipped
		3: {rx: 9_000_000_000, tx: 1}, // just came up: its lifetime bytes are not a rate
	}
	rx, tx := netRates(a, b, 2*time.Second)
	if rx != 1000 || tx != 500 {
		t.Errorf("netRates = %v/%v, want 1000/500", rx, tx)
	}
	if rx, _ := netRates(a, b, 0); rx != -1 {
		t.Errorf("zero interval = %v, want -1", rx)
	}
}

func TestGPUBusiest(t *testing.T) {
	v := map[string]float64{
		// Two processes on the same 3D engine add up.
		"pid_100_luid_0x00000000_0x0000D0A3_phys_0_eng_0_engtype_3D": 30,
		"pid_200_luid_0x00000000_0x0000D0A3_phys_0_eng_0_engtype_3D": 25,
		// A different engine is its own figure, not added to 3D.
		"pid_300_luid_0x00000000_0x0000D0A3_phys_0_eng_3_engtype_VideoDecode": 40,
		"not an engine instance": 99,
	}
	if got := gpuBusiest(v); math.Abs(got-55) > 1e-9 {
		t.Errorf("gpuBusiest = %v, want 55", got)
	}
	if got := gpuBusiest(map[string]float64{
		"pid_1_luid_0x0_0x1_phys_0_eng_0_engtype_3D": 80,
		"pid_2_luid_0x0_0x1_phys_0_eng_0_engtype_3D": 70,
	}); got != 100 {
		t.Errorf("cap = %v, want 100", got)
	}
	if k := gpuEngineKey("pid_9_luid_0x0_0x1_phys_0_eng_2_engtype_Copy"); k != "luid_0x0_0x1_phys_0_eng_2" {
		t.Errorf("gpuEngineKey = %q", k)
	}
}

// TestSamplerLive reads the real counters twice: every stat comes back in
// range on this machine.
func TestSamplerLive(t *testing.T) {
	s := newSampler()
	s.Sample(time.Now())
	time.Sleep(500 * time.Millisecond)
	st := s.Sample(time.Now())
	if st.CPU < 0 || st.CPU > 100 {
		t.Errorf("CPU = %v", st.CPU)
	}
	if st.MemTotal == 0 || st.MemUsed == 0 || st.MemUsed > st.MemTotal {
		t.Errorf("mem = %d/%d", st.MemUsed, st.MemTotal)
	}
	if st.RxBps < 0 || st.TxBps < 0 {
		t.Errorf("net = %v/%v", st.RxBps, st.TxBps)
	}
	if s.gpu != nil && (st.GPU < 0 || st.GPU > 100) {
		t.Errorf("GPU = %v", st.GPU)
	}
	t.Logf("cpu %.1f%% gpu %.1f%% mem %s rx %s tx %s", st.CPU, st.GPU, memText(st), FmtRate(st.RxBps), FmtRate(st.TxBps))
}
