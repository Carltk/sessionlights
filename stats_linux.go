package main

// Linux samplers. Every number comes from a kernel file under /proc or /sys:
// no subprocess, no network, no root. Parsers take bytes so the tests feed
// them fixtures.

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cpuTimes is the aggregate "cpu" line of /proc/stat, in clock ticks.
type cpuTimes struct{ busy, total uint64 }

// parseProcStat reads the first line of /proc/stat. idle and iowait count as
// not busy; guest time is already inside user, so it is not added again.
func parseProcStat(b []byte) (cpuTimes, bool) {
	line, _, _ := bytes.Cut(b, []byte{'\n'})
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, false
	}
	var v [8]uint64
	for i := 0; i < 8 && i+1 < len(f); i++ {
		v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
	}
	var total uint64
	for _, x := range v {
		total += x
	}
	idle := v[3] + v[4]
	return cpuTimes{busy: total - idle, total: total}, true
}

// cpuPct is the busy share between two samples, -1 when no time passed.
func cpuPct(a, b cpuTimes) float64 {
	if b.total <= a.total || b.busy < a.busy {
		return -1
	}
	return float64(b.busy-a.busy) * 100 / float64(b.total-a.total)
}

// parseMeminfo returns MemTotal and MemTotal - MemAvailable, in bytes: the
// memory nothing can reclaim, which is what "used" means to a person.
func parseMeminfo(b []byte) (used, total uint64, ok bool) {
	var avail uint64
	var haveT, haveA bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, haveT = n*1024, true
		case "MemAvailable:":
			avail, haveA = n*1024, true
		}
	}
	if !haveT || !haveA || avail > total {
		return 0, 0, false
	}
	return total - avail, total, true
}

// netBytes is received and sent bytes summed over the counted interfaces.
type netBytes struct{ rx, tx uint64 }

// parseNetDev sums /proc/net/dev over the interfaces count accepts.
func parseNetDev(b []byte, count func(iface string) bool) netBytes {
	var n netBytes
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // the two header lines
		}
		name = strings.TrimSpace(name)
		if !count(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		n.rx += rx
		n.tx += tx
	}
	return n
}

// physicalIface counts an interface only when it sits on a device, so a VPN
// tunnel, a bridge or a container veth does not count the same bytes twice.
func physicalIface(sysNet string) func(string) bool {
	return func(iface string) bool {
		if iface == "lo" {
			return false
		}
		_, err := os.Stat(filepath.Join(sysNet, iface, "device"))
		return err == nil
	}
}

// rate is bytes per second between two counters, -1 when it went backwards
// (an interface vanished or a counter reset).
func rate(a, b uint64, dt time.Duration) float64 {
	if b < a || dt <= 0 {
		return -1
	}
	return float64(b-a) / dt.Seconds()
}

// gpuSample is cumulative engine time per DRM client, per engine class.
// Clients are keyed by driver + client id, so one client open in several fds
// or processes is counted once.
type gpuSample map[string]map[string]uint64

// parseFdinfo reads one /proc/<pid>/fdinfo/<fd>. It returns the client key
// and its engine times in ns, or ok=false when the fd is not a DRM client.
func parseFdinfo(b []byte) (key string, engines map[string]uint64, ok bool) {
	var driver, client string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case k == "drm-driver":
			driver = v
		case k == "drm-client-id":
			client = v
		case strings.HasPrefix(k, "drm-engine-") && !strings.HasPrefix(k, "drm-engine-capacity-"):
			ns, err := strconv.ParseUint(strings.TrimSuffix(v, " ns"), 10, 64)
			if err != nil {
				continue
			}
			if engines == nil {
				engines = map[string]uint64{}
			}
			engines[strings.TrimPrefix(k, "drm-engine-")] = ns
		}
	}
	if driver == "" || client == "" || engines == nil {
		return "", nil, false
	}
	return driver + "/" + client, engines, true
}

// gpuPct is the busiest engine class's share of wall time between samples.
// Only clients present in both samples count; a client that went away or a
// counter that went backwards is skipped, never read as negative load.
func gpuPct(a, b gpuSample, dt time.Duration) float64 {
	if dt <= 0 || len(b) == 0 {
		return -1
	}
	busy := map[string]uint64{}
	for key, eb := range b {
		ea, ok := a[key]
		if !ok {
			continue
		}
		for class, nb := range eb {
			if na, ok := ea[class]; ok && nb >= na {
				busy[class] += nb - na
			}
		}
	}
	var max uint64
	for _, ns := range busy {
		if ns > max {
			max = ns
		}
	}
	pct := float64(max) * 100 / float64(dt.Nanoseconds())
	if pct > 100 {
		pct = 100
	}
	return pct
}

// sampler holds the previous counters; Sample turns two readings into rates.
type sampler struct {
	proc, sys string

	t    time.Time
	cpu  cpuTimes
	net  netBytes
	gpu  gpuSample
	have bool

	// drmFds caches which fdinfo files are DRM clients. Finding them means
	// reading every fd link of every process, so it is redone only every
	// gpuRescan; each tick reads just the cached files.
	drmFds   []string
	rescanAt time.Time
}

const gpuRescan = 30 * time.Second

func newSampler() *sampler { return &sampler{proc: "/proc", sys: "/sys"} }

// Sample reads every counter once. The first call has nothing to diff
// against, so its rates are unknown.
func (s *sampler) Sample(now time.Time) Stats {
	out := Unknown
	if b, err := os.ReadFile(filepath.Join(s.proc, "meminfo")); err == nil {
		if used, total, ok := parseMeminfo(b); ok {
			out.MemUsed, out.MemTotal = used, total
		}
	}
	var cpu cpuTimes
	cpuOK := false
	if b, err := os.ReadFile(filepath.Join(s.proc, "stat")); err == nil {
		cpu, cpuOK = parseProcStat(b)
	}
	var net netBytes
	netOK := false
	if b, err := os.ReadFile(filepath.Join(s.proc, "net", "dev")); err == nil {
		net, netOK = parseNetDev(b, physicalIface(filepath.Join(s.sys, "class", "net"))), true
	}
	gpu := s.readGPU(now)

	if s.have {
		dt := now.Sub(s.t)
		if cpuOK {
			out.CPU = cpuPct(s.cpu, cpu)
		}
		if netOK {
			out.RxBps = rate(s.net.rx, net.rx, dt)
			out.TxBps = rate(s.net.tx, net.tx, dt)
		}
		if s.gpu != nil {
			out.GPU = gpuPct(s.gpu, gpu, dt)
		}
	}
	s.t, s.cpu, s.net, s.gpu, s.have = now, cpu, net, gpu, true
	return out
}

func (s *sampler) readGPU(now time.Time) gpuSample {
	if now.After(s.rescanAt) {
		s.drmFds = onePerClient(findDRMFds(s.proc))
		s.rescanAt = now.Add(gpuRescan)
	}
	out := gpuSample{}
	for _, path := range s.drmFds {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // process or fd gone; the next rescan drops it
		}
		if key, eng, ok := parseFdinfo(b); ok {
			out[key] = eng
		}
	}
	return out
}

// onePerClient keeps one fdinfo path per DRM client. A process often holds
// several fds on one client, and each fdinfo read costs the kernel a walk of
// that client's contexts, so reading the same client twice is pure waste.
func onePerClient(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		key, _, ok := parseFdinfo(b)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// findDRMFds lists the fdinfo paths of every fd, in processes this user can
// read, that points at a /dev/dri node.
func findDRMFds(proc string) []string {
	pids, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range pids {
		if p.Name()[0] < '0' || p.Name()[0] > '9' {
			continue
		}
		fdDir := filepath.Join(proc, p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err == nil && strings.HasPrefix(target, "/dev/dri/") {
				out = append(out, filepath.Join(proc, p.Name(), "fdinfo", fd.Name()))
			}
		}
	}
	return out
}
