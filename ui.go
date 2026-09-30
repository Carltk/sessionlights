package main

import (
	"image"
	"image/color"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

var (
	colBg     = rgb(0x161B22)
	colEdge   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x33}
	colName   = rgb(0xE6EDF3)
	colHere   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x8C}
	colHereBg = color.NRGBA{0xFF, 0xFF, 0xFF, 0x1A}
	colGrip   = rgb(0x5A5F6A)
)

type ui struct {
	s    *strip
	th   *material.Theme
	tags map[string]*int // stable event targets, one per session
	bg   int             // event target for the empty background
	want image.Point     // last window size requested, in px
}

func newUI(s *strip) *ui {
	th := material.NewTheme()
	// Gio's bundled fonts cost ~20 MB resident; load them only as a fallback.
	faces := systemFonts()
	if len(faces) == 0 {
		faces = gofont.Collection()
	}
	th.Shaper = text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces))
	th.Face = "Segoe UI"
	return &ui{s: s, th: th, tags: map[string]*int{}}
}

func (u *ui) layout(gtx layout.Context) {
	v := *u.s.view.Load()
	u.handle(gtx, v)

	// Background: right-click anywhere that is not a session closes too.
	paint.Fill(gtx.Ops, colBg)
	bgArea := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.bg)
	bgArea.Pop()

	// Lay out against a generous box so the content decides the width,
	// then fit the window to it.
	measure := gtx
	measure.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(4000), gtx.Dp(200))}
	dims := layout.UniformInset(unit.Dp(0)).Layout(measure, func(gtx layout.Context) layout.Dimensions {
		return u.strip(gtx, v)
	})
	u.fit(gtx, dims.Size)

	edge := clip.Stroke{Path: clip.Rect{Max: gtx.Constraints.Max}.Path(), Width: 1}.Op()
	paint.FillShape(gtx.Ops, colEdge, edge)
}

func (u *ui) fit(gtx layout.Context, size image.Point) {
	if size == u.want {
		return
	}
	u.want = size
	px := gtx.Metric.PxPerDp
	go u.s.w.Option(app.Size(unit.Dp(float32(size.X)/px), unit.Dp(float32(size.Y)/px)))
}

func (u *ui) strip(gtx layout.Context, v View) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(6), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{layout.Rigid(u.grip)}
		if len(v.Boxes) == 0 {
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, u.empty)
			}))
		}
		for i, b := range v.Boxes {
			b := b
			left := unit.Dp(6)
			if i > 0 {
				left = unit.Dp(10)
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: left}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.box(gtx, b) })
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

// grip is the drag handle: the only move area. A strip-wide move area would
// swallow every click, because Gio's hit test walks past click handlers to
// any move area underneath.
func (u *ui) grip(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Dp(12), gtx.Dp(28))
	r := gtx.Dp(1.5)
	for row := 0; row < 4; row++ {
		for col := 0; col < 2; col++ {
			c := image.Pt(gtx.Dp(3)+col*gtx.Dp(5), gtx.Dp(6)+row*gtx.Dp(5))
			paint.FillShape(gtx.Ops, colGrip, clip.Ellipse{Min: c.Sub(image.Pt(r, r)), Max: c.Add(image.Pt(r, r))}.Op(gtx.Ops))
		}
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
	pointer.CursorGrab.Add(gtx.Ops)
	area.Pop()
	return layout.Dimensions{Size: size}
}

func (u *ui) empty(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return dot(gtx, colDim) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.label(gtx, "no sessions", 12, colDim, false)
			})
		}),
	)
}

func (u *ui) box(gtx layout.Context, b Box) layout.Dimensions {
	// Record the content first: the ring and hit area need its size.
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(7), Right: unit.Dp(9), Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return dot(gtx, b.Dot) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, b.Name, 12, colName, true) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, b.Sub, 10, b.Dot, false) }),
					)
				})
			}),
		)
	})
	content := m.Stop()

	// The ring's space is always reserved and only its colour changes, so
	// dots never shift sideways when you switch desktop.
	rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6))
	if b.OnHere {
		paint.FillShape(gtx.Ops, colHereBg, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, colHere, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1}.Op())
	}
	content.Add(gtx.Ops)

	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, u.tag(b.Key))
	if b.HWND != 0 {
		pointer.CursorPointer.Add(gtx.Ops)
	}
	area.Pop()
	return dims
}

func (u *ui) tag(key string) *int {
	t, ok := u.tags[key]
	if !ok {
		t = new(int)
		u.tags[key] = t
	}
	return t
}

// handle reads last frame's clicks: primary on a box focuses its terminal,
// secondary anywhere closes the strip. Actions leave the event goroutine.
func (u *ui) handle(gtx layout.Context, v View) {
	filter := func(t event.Tag) pointer.Filter {
		return pointer.Filter{Target: t, Kinds: pointer.Press | pointer.Release}
	}
	for {
		ev, ok := gtx.Event(filter(&u.bg))
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press && e.Buttons.Contain(pointer.ButtonSecondary) {
			go u.s.close()
		}
	}
	for _, b := range v.Boxes {
		for {
			ev, ok := gtx.Event(filter(u.tag(b.Key)))
			if !ok {
				break
			}
			e, ok := ev.(pointer.Event)
			if !ok || e.Kind != pointer.Press {
				continue
			}
			switch {
			case e.Buttons.Contain(pointer.ButtonSecondary):
				go u.s.close()
			case e.Buttons.Contain(pointer.ButtonPrimary) && b.HWND != 0:
				go u.s.focus(b.HWND)
			}
		}
	}
}

func dot(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	d := gtx.Dp(11)
	paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
	return layout.Dimensions{Size: image.Pt(d, d)}
}

func (u *ui) label(gtx layout.Context, s string, sp float32, c color.NRGBA, bold bool) layout.Dimensions {
	l := material.Label(u.th, unit.Sp(sp), s)
	l.Color = c
	l.MaxLines = 1
	l.Font.Typeface = "Segoe UI"
	if bold {
		l.Font.Weight = font.SemiBold
	}
	gtx.Constraints.Min = image.Point{}
	return l.Layout(gtx)
}
