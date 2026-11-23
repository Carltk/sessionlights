//go:build windows

package main

// The Windows samplers: CPU from GetSystemTimes, memory from
// GlobalMemoryStatusEx, network from GetIfTable2 and GPU from the PDH
// "GPU Engine" counters (the numbers Task Manager shows). No process is
// spawned and nothing touches the network.

import (
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	pGetSystemTimes          = modkernel32.NewProc("GetSystemTimes")
	pGlobalMemoryStatusEx    = modkernel32.NewProc("GlobalMemoryStatusEx")
	modpdh                   = windows.NewLazySystemDLL("pdh.dll")
	pPdhOpenQuery            = modpdh.NewProc("PdhOpenQueryW")
	pPdhAddEnglishCounter    = modpdh.NewProc("PdhAddEnglishCounterW")
	pPdhCollectQueryData     = modpdh.NewProc("PdhCollectQueryData")
	pPdhGetFormattedCounterA = modpdh.NewProc("PdhGetFormattedCounterArrayW")
)

// ---- CPU ---------------------------------------------------------------------

type cpuTimes struct{ busy, total uint64 }

// winCPUTimes turns GetSystemTimes' three counters into busy and total.
// Kernel time includes idle time, so busy is kernel + user - idle.
func winCPUTimes(idle, kernel, user uint64) cpuTimes {
	total := kernel + user
	busy := uint64(0)
	if total > idle {
		busy = total - idle
	}
	return cpuTimes{busy: busy, total: total}
}

// cpuPct is busy time over elapsed time between two samples, -1 when the
// counters did not move forward.
func cpuPct(a, b cpuTimes) float64 {
	if b.total <= a.total || b.busy < a.busy {
		return -1
	}
	return float64(b.busy-a.busy) * 100 / float64(b.total-a.total)
}

func ft64(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

func readCPU() (cpuTimes, bool) {
	var idle, kernel, user windows.Filetime
	r, _, _ := pGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return cpuTimes{}, false
	}
	return winCPUTimes(ft64(idle), ft64(kernel), ft64(user)), true
}

// ---- memory ------------------------------------------------------------------

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func readMem() (used, total uint64, ok bool) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 || m.TotalPhys == 0 {
		return 0, 0, false
	}
	return m.TotalPhys - m.AvailPhys, m.TotalPhys, true
}

// ---- network -----------------------------------------------------------------

type netBytes struct{ rx, tx uint64 }

// InterfaceAndOperStatusFlags bits (netioapi.h).
const (
	ifHardware = 1 << 0
	ifFilter   = 1 << 1
)

// countIface is the adapter filter: real hardware that is up. Filter
// interfaces (WFP, QoS, VPN filters) sit on top of a hardware adapter and
// carry the same bytes again, so they are left out.
func countIface(flags uint8, oper uint32) bool {
	return flags&ifHardware != 0 && flags&ifFilter == 0 && oper == windows.IfOperStatusUp
}

// readNet is each counted adapter's lifetime byte counters, by LUID.
func readNet() (map[uint64]netBytes, bool) {
	var t *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableRaw, &t); err != nil {
		return nil, false
	}
	defer windows.FreeMibTable(unsafe.Pointer(t))
	n := map[uint64]netBytes{}
	for _, row := range unsafe.Slice(&t.Table[0], t.NumEntries) {
		if countIface(row.InterfaceAndOperStatusFlags, row.OperStatus) {
			n[row.InterfaceLuid] = netBytes{rx: row.InOctets, tx: row.OutOctets}
		}
	}
	return n, true
}

// netRates sums per-adapter deltas over adapters present in both readings.
// Summing totals first would count an adapter that just came up (Wi-Fi
// reconnecting, a dock) with its whole lifetime of bytes in one tick.
func netRates(a, b map[uint64]netBytes, dt time.Duration) (rx, tx float64) {
	if dt <= 0 {
		return -1, -1
	}
	var drx, dtx uint64
	for luid, now := range b {
		was, ok := a[luid]
		if !ok || now.rx < was.rx || now.tx < was.tx {
			continue // new, or its counters reset
		}
		drx += now.rx - was.rx
		dtx += now.tx - was.tx
	}
	return float64(drx) / dt.Seconds(), float64(dtx) / dt.Seconds()
}

// ---- GPU ---------------------------------------------------------------------

// gpuEngineKey is the engine an instance of "\GPU Engine(*)" belongs to:
// "pid_1234_luid_0x0_0xD0A3_phys_0_eng_3_engtype_3D" -> "luid_0x0_0xD0A3_phys_0_eng_3".
// Every process using that engine has its own instance, so they are summed
// per engine.
func gpuEngineKey(instance string) string {
	i := strings.Index(instance, "luid_")
	if i < 0 {
		return ""
	}
	k := instance[i:]
	if j := strings.Index(k, "_engtype_"); j >= 0 {
		k = k[:j]
	}
	return k
}

// gpuBusiest sums utilisation per engine and returns the busiest engine,
// capped at 100: what Task Manager shows for a GPU.
func gpuBusiest(values map[string]float64) float64 {
	per := map[string]float64{}
	for inst, v := range values {
		if k := gpuEngineKey(inst); k != "" && v > 0 {
			per[k] += v
		}
	}
	best := 0.0
	for _, v := range per {
		best = max(best, v)
	}
	return min(best, 100)
}

const (
	pdhFmtDouble  = 0x00000200
	pdhFmtNoCap   = 0x00008000
	pdhMoreData   = 0x800007D2
	pdhCStatusOK  = 0
	pdhCStatusNew = 1
)

// pdhFmtItem is PDH_FMT_COUNTERVALUE_ITEM_W as laid out on amd64 (the value
// at offset 16). On 386 Go would align the float64 differently; only amd64
// is built.
type pdhFmtItem struct {
	name   *uint16
	status uint32
	_      uint32
	value  float64
}

type gpuQuery struct {
	query, counter uintptr
	primed         bool
	buf            []byte
}

func openGPUQuery() *gpuQuery {
	if pPdhOpenQuery.Find() != nil {
		return nil
	}
	g := &gpuQuery{}
	if r, _, _ := pPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&g.query))); r != 0 {
		return nil
	}
	path, _ := syscall.UTF16PtrFromString(`\GPU Engine(*)\Utilization Percentage`)
	if r, _, _ := pPdhAddEnglishCounter.Call(g.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&g.counter))); r != 0 {
		return nil
	}
	return g
}

// sample collects once. Utilisation is a rate, so the first collection only
// primes it and reads as unknown.
func (g *gpuQuery) sample() float64 {
	if r, _, _ := pPdhCollectQueryData.Call(g.query); r != 0 {
		return -1
	}
	if !g.primed {
		g.primed = true
		return -1
	}
	// The instance list can grow between asking the size and reading (a
	// process starts using the GPU), which answers PDH_MORE_DATA again: ask
	// again with the new size, a few times at most.
	var size, count uint32
	ok := false
	for try := 0; try < 3 && !ok; try++ {
		size, count = 0, 0
		r, _, _ := pPdhGetFormattedCounterA.Call(g.counter, pdhFmtDouble|pdhFmtNoCap, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
		if uint32(r) != pdhMoreData || size == 0 {
			if r == 0 { // no instances: an idle GPU nobody is using
				return 0
			}
			return -1
		}
		if int(size) > len(g.buf) {
			g.buf = make([]byte, size)
		}
		r, _, _ = pPdhGetFormattedCounterA.Call(g.counter, pdhFmtDouble|pdhFmtNoCap, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&g.buf[0])))
		switch {
		case r == 0:
			ok = true
		case uint32(r) != pdhMoreData:
			return -1
		}
	}
	if !ok {
		return -1
	}
	items := unsafe.Slice((*pdhFmtItem)(unsafe.Pointer(&g.buf[0])), count)
	values := make(map[string]float64, count)
	for _, it := range items {
		if it.status == pdhCStatusOK || it.status == pdhCStatusNew {
			values[windows.UTF16PtrToString(it.name)] += it.value
		}
	}
	return gpuBusiest(values)
}

// ---- the sampler -------------------------------------------------------------

type sampler struct {
	t            time.Time
	cpu          cpuTimes
	net          map[uint64]netBytes
	cpuOK, netOK bool // the previous reading of each is usable
	gpu          *gpuQuery
	gpuAt        time.Time // when the GPU was last collected
	gpuLast      float64   // and what it read; reused until gpuEvery passes
	gpuN         int       // collections so far
}

// gpuEvery spaces out the PDH GPU collection, the one costly read: with the
// strip redrawing every tick it doubled the strip's CPU (2026-11-23,
// 0.86-1.17% of a core with it, 0.16% without). PDH reports utilisation
// averaged since the previous collection, so a slower collection is a
// longer average, not a missed spike.
const gpuEvery = 10 * time.Second

func newSampler() *sampler { return &sampler{gpu: openGPUQuery(), gpuLast: -1} }

// Sample reads every counter once. The first call has nothing to diff
// against, so its rates are unknown.
func (s *sampler) Sample(now time.Time) Stats {
	out := Unknown
	if used, total, ok := readMem(); ok {
		out.MemUsed, out.MemTotal = used, total
	}
	cpu, cpuOK := readCPU()
	net, netOK := readNet()
	if s.gpu != nil {
		// The first two collections are close together: the first only
		// primes the rate, so the second gives the first real reading.
		if s.gpuN < 2 || now.Sub(s.gpuAt) >= gpuEvery {
			s.gpuLast, s.gpuAt = s.gpu.sample(), now
			s.gpuN++
		}
		out.GPU = s.gpuLast
	}
	if cpuOK && s.cpuOK {
		out.CPU = cpuPct(s.cpu, cpu)
	}
	if netOK && s.netOK {
		dt := now.Sub(s.t)
		out.RxBps, out.TxBps = netRates(s.net, net, dt)
	}
	s.t, s.cpu, s.net, s.cpuOK, s.netOK = now, cpu, net, cpuOK, netOK
	return out
}
