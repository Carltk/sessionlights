package main

import (
	"image/color"
	"testing"
	"time"
)

func TestLoadColourBands(t *testing.T) {
	cases := []struct {
		pct  float64
		want color.NRGBA
	}{
		{-1, colDim}, {0, colCruising}, {24.9, colCruising}, {25, colWorking},
		{49.9, colWorking}, {50, colBusyBand}, {74.9, colBusyBand}, {75, colPegged}, {100, colPegged},
	}
	for _, c := range cases {
		if got := LoadColour(c.pct); got != c.want {
			t.Errorf("LoadColour(%v) = %v, want %v", c.pct, got, c.want)
		}
	}
}

func TestNetColourRamps(t *testing.T) {
	rates := []float64{0, netTrickle, netFlowing, netFast, netGigabit}
	for i, r := range rates {
		if got := NetColour(r, false); got != downSteps[i] {
			t.Errorf("down %v: step %v, want %v", r, got, downSteps[i])
		}
		if got := NetColour(r, true); got != upSteps[i] {
			t.Errorf("up %v: step %v, want %v", r, got, upSteps[i])
		}
	}
	if NetColour(-1, true) != colDim {
		t.Error("unknown rate should be dim")
	}
	// Upload is always bright: every step is clearly lit, unlike download's first.
	for _, c := range upSteps {
		if int(c.R)+int(c.G)+int(c.B) < 300 {
			t.Errorf("upload step %v is not bright", c)
		}
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[float64]string{-1: "--", 0: "0%", 23.4: "23%", 120: "100%"} {
		if got := FmtPct(in); got != want {
			t.Errorf("FmtPct(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{-1: "--", 0: "0K", 840e3: "840K", 1.24e6: "1.2M", 12e6: "12M", 1.1e9: "1.1G"} {
		if got := FmtRate(in); got != want {
			t.Errorf("FmtRate(%v) = %q, want %q", in, got, want)
		}
	}
	if got := FmtGiB(9.5 * (1 << 30)); got != "9.5G" {
		t.Errorf("FmtGiB = %q", got)
	}
}

func TestHostSafeWidth(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{11, 22, 22}, {22, 22, 22}, {27, 22, 33}, {33, 22, 33}, {80, 22, 80},
	} {
		if got := hostSafeWidth(c.w, c.h); got != c.want {
			t.Errorf("hostSafeWidth(%d,%d) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

func TestStatSegmentsHideUnreadableGPU(t *testing.T) {
	if _, ok := statSegments(Unknown, false)[segGPU]; ok {
		t.Error("GPU segment shown with no readable GPU")
	}
	if _, ok := statSegments(Unknown, true)[segGPU]; !ok {
		t.Error("GPU segment missing with a readable GPU")
	}
}

func TestMemText(t *testing.T) {
	s := Stats{MemUsed: 10095022080, MemTotal: 16467214336} // a 16 GiB laptop
	if got := memText(s); got != "9.4/16" {
		t.Fatalf("memText = %q, want 9.4/16", got)
	}
	if memText(Stats{}) != "--" {
		t.Fatal("unknown memory should read --")
	}
}

func TestShortName(t *testing.T) {
	for in, want := range map[string]string{
		"ULBar stats bar": "ULBar", "Research": "Research", "credentials-vault": "credential",
		"my-project-name": "my-project", "  padded  ": "padded",
	} {
		if got := shortName(in); got != want {
			t.Errorf("shortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionViewColoursNames(t *testing.T) {
	now := time.Now()
	lights := []Light{
		{SessionID: "b", Name: "zeta", State: "idle", modTime: now},
		{SessionID: "a", Name: "Alpha build", State: "waiting", modTime: now, PID: 0},
	}
	boxes := sessionView(lights, liveProc{}, now)
	if len(boxes) != 2 || boxes[0].Short != "Alpha" || boxes[0].Col != colWaiting ||
		boxes[1].Short != "zeta" || boxes[1].Col != colIdle {
		t.Fatalf("boxes %+v", boxes)
	}
	if boxes[0].Line != "Alpha build - waiting" {
		t.Fatalf("line %q", boxes[0].Line)
	}
}
