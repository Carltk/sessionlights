package main

// What the bar says: the session names and the stat segments, already
// coloured. Surfaces only draw what this decides.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// sessionView is every live session, in name order: a short name in its
// state colour for the bar, and a full "name - state" line for hover text.
func sessionView(lights []Light, p Proc, now time.Time) (segment, []string) {
	live := make([]Light, 0, len(lights))
	for _, l := range lights {
		if Alive(l, p, now) {
			live = append(live, l)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		a, b := strings.ToLower(live[i].Name), strings.ToLower(live[j].Name)
		if a != b {
			return a < b
		}
		return live[i].SessionID < live[j].SessionID
	})
	var seg segment
	names := make([]string, len(live))
	for i, l := range live {
		name := l.Name
		if name == "" {
			name = l.SessionID
		}
		names[i] = truncate(name, maxName) + " - " + l.State
		if i > 0 {
			seg.Runs = append(seg.Runs, run{Text: "  "})
		}
		seg.Runs = append(seg.Runs, run{Text: shortName(name), Col: dotColour(l, now)})
	}
	return seg, names
}

// shortNameLen is how many characters of a session's name the bar shows.
const shortNameLen = 10

// shortName cuts a name to shortNameLen runes at a word end when one is
// close, so "ULBar stats bar" reads "ULBar" rather than "ULBar stat".
func shortName(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= shortNameLen {
		return s
	}
	cut := r[:shortNameLen]
	if i := strings.LastIndexAny(string(cut), " -_/."); i >= 4 {
		return strings.TrimSpace(string(cut)[:i])
	}
	return string(cut)
}

// Segment keys, in bar order.
const (
	segSessions = "sessions"
	segCPU      = "cpu"
	segGPU      = "gpu"
	segMem      = "mem"
	segNet      = "net"
)

var segOrder = []string{segSessions, segCPU, segGPU, segMem, segNet}

// statSegments is each stat as label + banded value. The GPU segment is left
// out when the GPU cannot be read at all, rather than showing "--" forever.
func statSegments(s Stats, gpuReadable bool) map[string]segment {
	m := map[string]segment{
		segCPU: {Runs: []run{{Text: "C-", Col: colLabel}, {Text: FmtPct(s.CPU), Col: LoadColour(s.CPU)}}},
		segMem: {Runs: []run{{Text: "M-", Col: colLabel}, {Text: memText(s), Col: LoadColour(s.MemPct())}}},
		segNet: {Runs: []run{
			{Arrow: -1, Col: colLabel}, {Text: FmtRate(s.RxBps), Col: NetColour(s.RxBps, false)},
			{Text: " ", Col: colLabel}, {Arrow: 1, Col: colLabel}, {Text: FmtRate(s.TxBps), Col: NetColour(s.TxBps, true)},
		}},
	}
	if gpuReadable {
		m[segGPU] = segment{Runs: []run{{Text: "G-", Col: colLabel}, {Text: FmtPct(s.GPU), Col: LoadColour(s.GPU)}}}
	}
	return m
}

// memText is used/total in GiB: "9.4/16". The total is rounded up, because
// the kernel keeps a slice back and a 16 GiB machine reports about 15.3.
func memText(s Stats) string {
	if s.MemTotal == 0 {
		return "--"
	}
	return fmt.Sprintf("%.1f/%.0f", float64(s.MemUsed)/(1<<30), math.Ceil(float64(s.MemTotal)/(1<<30)))
}

// sessionsTitle is the hover text: every live session with its state.
func sessionsTitle(sessions []string) string {
	if len(sessions) == 0 {
		return "SessionLights: no sessions"
	}
	return strings.Join(sessions, "  ·  ")
}

// barTitle is the one-line text form: hover text and accessible name.
func barTitle(s Stats, sessions []string) string {
	parts := []string{
		"CPU " + FmtPct(s.CPU),
		"GPU " + FmtPct(s.GPU),
		"MEM " + memText(s) + " GiB",
		"↓" + FmtRate(s.RxBps) + "/s ↑" + FmtRate(s.TxBps) + "/s",
	}
	if len(sessions) > 0 {
		parts = append([]string{strings.Join(sessions, ", ")}, parts...)
	}
	return strings.Join(parts, "  ·  ")
}
