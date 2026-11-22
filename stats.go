package main

// The stats half of the bar, after ULBar: every stat is a small sampler, and
// colour tells you before the number does. This file is platform-neutral:
// the numbers' shape, the bands and the formatting. Each OS fills Stats in
// its own stats_<os>.go.

import (
	"fmt"
	"image/color"
	"math"
)

// Stats is one sample. A negative percentage or rate means "not known", so
// a missing GPU never reads as an idle one.
type Stats struct {
	CPU      float64 // busy %, 0-100
	GPU      float64 // busiest engine %, 0-100
	MemUsed  uint64  // bytes
	MemTotal uint64  // bytes
	RxBps    float64 // download bytes/s
	TxBps    float64 // upload bytes/s
}

// Unknown is a Stats with nothing sampled yet.
var Unknown = Stats{CPU: -1, GPU: -1, RxBps: -1, TxBps: -1}

// MemPct is memory used as a percentage of total, -1 when unknown.
func (s Stats) MemPct() float64 {
	if s.MemTotal == 0 {
		return -1
	}
	return float64(s.MemUsed) * 100 / float64(s.MemTotal)
}

// Load bands. A threshold is the lower edge of its band.
const (
	bandWorking = 25.0
	bandBusy    = 50.0
	bandPegged  = 75.0
)

// Network bands, bytes/s: the lower edge of each step.
const (
	netTrickle = 10e3  // 10 KB/s
	netFlowing = 1e6   // 1 MB/s
	netFast    = 10e6  // 10 MB/s
	netGigabit = 100e6 // 100 MB/s, a saturated gigabit link
)

var (
	colCruising = rgb(0x3FB950)
	colWorking  = rgb(0xA6C83A)
	colBusyBand = rgb(0xE0A33E)
	colPegged   = rgb(0xF04747)

	// Download climbs through greens to a gigabit glow.
	downSteps = []color.NRGBA{rgb(0x2E6B3A), rgb(0x3FB950), rgb(0x56D364), rgb(0x7EE787), rgb(0xB8FFC4)}
	// Upload is always bright; only the hue moves, orange to red.
	upSteps = []color.NRGBA{rgb(0xF0B060), rgb(0xF09040), rgb(0xE8742C), rgb(0xF05A3A), rgb(0xF04747)}
)

// LoadColour bands a 0-100 load. Unknown reads dim.
func LoadColour(pct float64) color.NRGBA {
	switch {
	case pct < 0 || math.IsNaN(pct):
		return colDim
	case pct < bandWorking:
		return colCruising
	case pct < bandBusy:
		return colWorking
	case pct < bandPegged:
		return colBusyBand
	}
	return colPegged
}

// NetColour bands a rate. up picks the upload ramp.
func NetColour(bps float64, up bool) color.NRGBA {
	if bps < 0 || math.IsNaN(bps) {
		return colDim
	}
	steps := downSteps
	if up {
		steps = upSteps
	}
	i := 0
	switch {
	case bps >= netGigabit:
		i = 4
	case bps >= netFast:
		i = 3
	case bps >= netFlowing:
		i = 2
	case bps >= netTrickle:
		i = 1
	}
	return steps[i]
}

// FmtPct is "23%", or "--" when unknown.
func FmtPct(pct float64) string {
	if pct < 0 || math.IsNaN(pct) {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", math.Min(pct, 100))
}

// FmtGiB is "9.1G": gigabytes in binary units, one decimal.
func FmtGiB(b uint64) string {
	return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
}

// FmtRate is a compact rate: "0K", "840K", "1.2M", "12M", "1.1G" per second.
func FmtRate(bps float64) string {
	switch {
	case bps < 0 || math.IsNaN(bps):
		return "--"
	case bps < 1e6:
		return fmt.Sprintf("%.0fK", bps/1e3)
	case bps < 10e6:
		return fmt.Sprintf("%.1fM", bps/1e6)
	case bps < 1e9:
		return fmt.Sprintf("%.0fM", bps/1e6)
	}
	return fmt.Sprintf("%.1fG", bps/1e9)
}
