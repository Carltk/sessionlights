// Renders the SessionLights listing icon: a dark tile holding a strip of
// three state lights (idle green, busy amber, waiting red), the busy one
// carrying the small sub-agent dot. Signed-distance shapes, 4x supersampled.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const N = 1024

type rgba struct{ r, g, b, a float64 }

func hex(v uint32, a float64) rgba {
	return rgba{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255, a}
}

// over composites src (straight alpha) onto dst.
func over(d, s rgba) rgba {
	a := s.a + d.a*(1-s.a)
	if a == 0 {
		return rgba{}
	}
	f := func(sc, dc float64) float64 { return (sc*s.a + dc*d.a*(1-s.a)) / a }
	return rgba{f(s.r, d.r), f(s.g, d.g), f(s.b, d.b), a}
}

func roundRect(x, y, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(x-cx) - hw + r
	qy := math.Abs(y-cy) - hh + r
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

func circle(x, y, cx, cy, r float64) float64 { return math.Hypot(x-cx, y-cy) - r }

func cover(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

func shade(x, y float64) rgba {
	px := rgba{}
	// Tile.
	tile := roundRect(x, y, 512, 512, 512, 512, 200)
	bg := hex(0x161B22, 1)
	// Faint vertical sheen.
	t := y / N
	bg.r += 0.03 * (1 - t)
	bg.g += 0.03 * (1 - t)
	bg.b += 0.04 * (1 - t)
	bg.a = cover(tile)
	px = over(px, bg)
	if bg.a == 0 {
		return px
	}
	// Strip.
	const sy, shw, shh = 560.0, 380.0, 128.0
	strip := roundRect(x, y, 512, sy, shw, shh, shh)
	s := hex(0x0D1117, cover(strip))
	px = over(px, s)
	edge := math.Abs(strip+3) - 3
	px = over(px, rgba{1, 1, 1, 0.16 * cover(edge)})
	// Lights with glow.
	lights := []struct {
		cx  float64
		col uint32
	}{{272, 0x3FB950}, {512, 0xE0A33E}, {752, 0xF04747}}
	for _, l := range lights {
		dd := circle(x, y, l.cx, sy, 78)
		if dd > 0 && dd < 70 {
			g := hex(l.col, 0.38*math.Exp(-dd/22))
			px = over(px, g)
		}
		c := hex(l.col, cover(dd))
		// Soft highlight on the upper left of each light.
		h := math.Max(0, 1-math.Hypot(x-(l.cx-26), y-(sy-28))/60)
		c.r = math.Min(1, c.r+0.22*h)
		c.g = math.Min(1, c.g+0.22*h)
		c.b = math.Min(1, c.b+0.22*h)
		px = over(px, c)
	}
	// Sub-agent dot above the busy light, with a tile-coloured ring.
	ring := circle(x, y, 600, 420, 46)
	px = over(px, hex(0x161B22, cover(ring)))
	dot := circle(x, y, 600, 420, 32)
	px = over(px, hex(0xE0A33E, cover(dot)))
	return px
}

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, N, N))
	const ss = 4
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			var acc rgba
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					p := shade(float64(x)+(float64(i)+0.5)/ss, float64(y)+(float64(j)+0.5)/ss)
					acc.r += p.r * p.a
					acc.g += p.g * p.a
					acc.b += p.b * p.a
					acc.a += p.a
				}
			}
			n := float64(ss * ss)
			a := acc.a / n
			var c color.NRGBA
			if a > 0 {
				c = color.NRGBA{uint8(acc.r / acc.a * 255), uint8(acc.g / acc.a * 255), uint8(acc.b / acc.a * 255), uint8(a*255 + 0.5)}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	f, _ := os.Create(os.Args[1])
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
