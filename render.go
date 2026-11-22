package main

// Software rendering of bar segments to small bitmaps, for surfaces that take
// pictures rather than draw widgets (a tray item). Coloured text and dots on
// a transparent background; the panel shows through.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// pixmap is one size of an icon: ARGB32 in network byte order, as SNI wants.
type pixmap struct {
	W, H int32
	Data []byte
}

// run is a stretch of text in one colour. A run with Arrow set draws a
// triangle instead (+1 up, -1 down): UI fonts often lack the arrow glyphs.
type run struct {
	Text  string
	Col   color.NRGBA
	Arrow int
}

// segment is one thing on the bar: optional dots, then text runs.
type segment struct {
	Dots []color.NRGBA
	Runs []run
}

// key identifies what a segment looks like, so an unchanged one is not
// rendered again.
func (s segment) key() string {
	var b strings.Builder
	for _, d := range s.Dots {
		fmt.Fprintf(&b, "%02x%02x%02x%02x.", d.R, d.G, d.B, d.A)
	}
	for _, r := range s.Runs {
		fmt.Fprintf(&b, "|%d:%s:%02x%02x%02x%02x", r.Arrow, r.Text, r.Col.R, r.Col.G, r.Col.B, r.Col.A)
	}
	return b.String()
}

// colLabel is the "CPU" in "CPU 23%": quiet, so the banded value leads.
var colLabel = rgb(0xC8CCD4)

// fontFiles are tried in order; the first that parses is used. The panel's
// own UI font first, so the bar reads like the rest of the top bar.
var fontFiles = []string{
	"/usr/share/fonts/truetype/ubuntu/UbuntuSans[wdth,wght].ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Medium.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
}

var (
	fontOnce sync.Once
	uiFont   *sfnt.Font
	faceMu   sync.Mutex
	faces    = map[int]font.Face{}
)

func loadFont() *sfnt.Font {
	fontOnce.Do(func() {
		for _, p := range fontFiles {
			if b, err := os.ReadFile(p); err == nil {
				if f, err := opentype.Parse(b); err == nil {
					uiFont = f
					return
				}
			}
		}
		uiFont, _ = opentype.Parse(gomedium.TTF)
	})
	return uiFont
}

// faceFor is the font at the size that suits a bitmap h pixels tall.
func faceFor(h int) font.Face {
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faces[h]; ok {
		return f
	}
	f, err := opentype.NewFace(loadFont(), &opentype.FaceOptions{
		Size: float64(h) * 0.66, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	faces[h] = f
	return f
}

// measure is the segment's width in pixels at height h.
func measure(s segment, h int) int {
	face := faceFor(h)
	d := dotSize(h)
	w := 0
	if len(s.Dots) > 0 {
		w += len(s.Dots)*d + (len(s.Dots)-1)*dotGap(h)
		if len(s.Runs) > 0 {
			w += h / 3
		}
	}
	for _, r := range s.Runs {
		switch {
		case r.Arrow != 0:
			w += arrowW(h)
		case face != nil:
			w += font.MeasureString(face, r.Text).Ceil()
		}
	}
	return w
}

// arrowW is a triangle's advance: its width plus a little air after it.
func arrowW(h int) int { return int(math.Round(float64(h)*0.42)) + max(1, h/10) }

func dotSize(h int) int { return int(math.Round(float64(h) * 0.5)) }
func dotGap(h int) int  { return int(math.Max(2, math.Round(float64(h)*0.22))) }

// renderSegment draws s into an h-pixel-tall bitmap, at least minW wide so a
// changing number does not make the item jitter.
func renderSegment(s segment, h, minW int) *image.NRGBA {
	w := measure(s, h)
	if w < minW {
		w = minW
	}
	if w < 1 {
		w = 1
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	x := 0
	if len(s.Runs) == 0 {
		x = (w - measure(s, h)) / 2 // dots alone sit centred in their cell
	}
	d := dotSize(h)
	for i, c := range s.Dots {
		if i > 0 {
			x += dotGap(h)
		}
		fillCircle(img, x, (h-d)/2, d, c)
		x += d
	}
	if len(s.Dots) > 0 && len(s.Runs) > 0 {
		x += h / 3
	}
	face := faceFor(h)
	if face == nil {
		return img
	}
	m := face.Metrics()
	// Centre the cap height: baseline sits so ascent-descent is balanced.
	base := (h + m.Ascent.Ceil() - m.Descent.Ceil()) / 2
	for _, r := range s.Runs {
		if r.Arrow != 0 {
			fillArrow(img, x, h, r.Arrow > 0, r.Col)
			x += arrowW(h)
			continue
		}
		dr := &font.Drawer{Dst: img, Src: image.NewUniform(r.Col), Face: face,
			Dot: fixed.P(x, base)}
		dr.DrawString(r.Text)
		x = dr.Dot.X.Ceil()
	}
	return img
}

// hostSafeWidth is the width a tray bitmap needs so GNOME's AppIndicator host
// draws it unscaled: a square is scaled evenly, anything at least 1.5 times
// as wide as tall is drawn as is, and anything between is squashed square.
func hostSafeWidth(w, h int) int {
	switch {
	case w <= h:
		return h
	case 2*w < 3*h:
		return (3*h + 1) / 2
	}
	return w
}

// fillCircle draws an anti-aliased disc of diameter d at (x, y).
func fillCircle(img *image.NRGBA, x, y, d int, c color.NRGBA) {
	r := float64(d) / 2
	cx, cy := float64(x)+r, float64(y)+r
	for py := y; py < y+d; py++ {
		for px := x; px < x+d; px++ {
			dist := math.Hypot(float64(px)+0.5-cx, float64(py)+0.5-cy)
			cov := math.Max(0, math.Min(1, r-dist+0.5))
			if cov == 0 {
				continue
			}
			src := color.NRGBA{c.R, c.G, c.B, uint8(float64(c.A) * cov)}
			draw.Draw(img, image.Rect(px, py, px+1, py+1), image.NewUniform(src), image.Point{}, draw.Over)
		}
	}
}

// fillArrow draws an anti-aliased triangle in a cell h tall at x, pointing up
// or down, about cap height so it sits with the digits.
func fillArrow(img *image.NRGBA, x, h int, up bool, c color.NRGBA) {
	tw := float64(h) * 0.42
	th := float64(h) * 0.40
	top := (float64(h) - th) / 2
	for py := 0; py < h; py++ {
		for px := x; px < x+int(math.Ceil(tw)); px++ {
			cov := 0.0
			for sy := 0; sy < 4; sy++ { // 4x4 supersampling
				for sx := 0; sx < 4; sx++ {
					fx := float64(px-x) + (float64(sx)+0.5)/4
					fy := float64(py) + (float64(sy)+0.5)/4 - top
					if fy < 0 || fy > th {
						continue
					}
					t := fy / th // 0 at the top edge
					if !up {
						t = 1 - t
					}
					half := tw / 2 * t
					if math.Abs(fx-tw/2) <= half {
						cov += 1.0 / 16
					}
				}
			}
			if cov == 0 {
				continue
			}
			src := color.NRGBA{c.R, c.G, c.B, uint8(float64(c.A) * cov)}
			draw.Draw(img, image.Rect(px, py, px+1, py+1), image.NewUniform(src), image.Point{}, draw.Over)
		}
	}
}

// toPixmap converts to SNI's ARGB32, big-endian, not premultiplied.
func toPixmap(img *image.NRGBA) pixmap {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			out = append(out, c.A, c.R, c.G, c.B)
		}
	}
	return pixmap{W: int32(b.Dx()), H: int32(b.Dy()), Data: out}
}
