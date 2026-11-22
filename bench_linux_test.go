package main

import (
	"bytes"
	"image/color"
	"testing"
	"time"
)

func BenchmarkSample(b *testing.B) {
	s := newSampler()
	now := time.Now()
	s.Sample(now)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(2 * time.Second)
		s.Sample(now) // rescan every 15th call, as live (30 s / 2 s)
	}
}

func BenchmarkRenderTick(b *testing.B) {
	st := Stats{CPU: 23, GPU: 4, MemUsed: 9 << 30, MemTotal: 16 << 30, RxBps: 12e3, TxBps: 4e3}
	segs := statSegments(st, true)
	segs[segSessions] = segment{Dots: []color.NRGBA{colBusy, colIdle}}
	for i := 0; i < b.N; i++ {
		for k, s := range segs {
			img := renderSegment(s, 22, measure(widthTemplates[k], 22))
			var buf bytes.Buffer
			pngEnc.Encode(&buf, img)
		}
	}
}

func BenchmarkSampleNoRescan(b *testing.B) {
	s := newSampler()
	now := time.Now()
	s.Sample(now)
	s.rescanAt = now.Add(1000 * time.Hour)
	for i := 0; i < b.N; i++ {
		now = now.Add(2 * time.Second)
		s.Sample(now)
	}
}

func BenchmarkFindDRMFds(b *testing.B) {
	for i := 0; i < b.N; i++ {
		findDRMFds("/proc")
	}
}
