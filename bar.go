package main

// What the bar says: the session dots and the stat segments, already
// coloured. Surfaces only draw what this decides.

import (
	"image/color"
	"sort"
	"strings"
	"time"
)

// sessionDots is one dot per live session, in name order, in its state colour.
func sessionDots(lights []Light, p Proc, now time.Time) ([]color.NRGBA, []string) {
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
	dots := make([]color.NRGBA, len(live))
	names := make([]string, len(live))
	for i, l := range live {
		dots[i] = dotColour(l, now)
		name := l.Name
		if name == "" {
			name = l.SessionID
		}
		names[i] = truncate(name, maxName) + " - " + l.State
	}
	return dots, names
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
		segCPU: {Runs: []run{{Text: "CPU ", Col: colLabel}, {Text: FmtPct(s.CPU), Col: LoadColour(s.CPU)}}},
		segMem: {Runs: []run{{Text: "MEM ", Col: colLabel}, {Text: memText(s), Col: LoadColour(s.MemPct())}}},
		segNet: {Runs: []run{
			{Arrow: -1, Col: colLabel}, {Text: FmtRate(s.RxBps), Col: NetColour(s.RxBps, false)},
			{Text: " ", Col: colLabel}, {Arrow: 1, Col: colLabel}, {Text: FmtRate(s.TxBps), Col: NetColour(s.TxBps, true)},
		}},
	}
	if gpuReadable {
		m[segGPU] = segment{Runs: []run{{Text: "GPU ", Col: colLabel}, {Text: FmtPct(s.GPU), Col: LoadColour(s.GPU)}}}
	}
	return m
}

func memText(s Stats) string {
	if s.MemTotal == 0 {
		return "--"
	}
	return FmtGiB(s.MemUsed)
}

// barTitle is the one-line text form: hover text and accessible name.
func barTitle(s Stats, sessions []string) string {
	parts := []string{
		"CPU " + FmtPct(s.CPU),
		"GPU " + FmtPct(s.GPU),
		"MEM " + memText(s) + " / " + FmtGiB(s.MemTotal),
		"↓" + FmtRate(s.RxBps) + "/s ↑" + FmtRate(s.TxBps) + "/s",
	}
	if len(sessions) > 0 {
		parts = append([]string{strings.Join(sessions, ", ")}, parts...)
	}
	return strings.Join(parts, "  ·  ")
}
