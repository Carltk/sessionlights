package main

import (
	"image"
	"image/color"
	"strconv"
	"time"

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
	colDrag   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x30}
	colBadge  = color.NRGBA{0x3B, 0x82, 0xF6, 0xFF}
)

type ui struct {
	s     *strip
	th    *material.Theme
	tags  map[string]*int            // stable event targets, one per session
	bg    int                        // event target for the empty background
	want  image.Point                // last window size requested, in px
	slots map[string]image.Rectangle // where each box was drawn last frame
	drag  drag
}

// drag is a press on a box that may turn into a reorder. It only becomes one
// after the pointer moves a few pixels, so a plain click still focuses.
type drag struct {
	key     string
	primary bool
	pressX  float32
	active  bool
	order   []string // preview order while active
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
	return &ui{s: s, th: th, tags: map[string]*int{}, slots: map[string]image.Rectangle{}}
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
	origin := image.Pt(gtx.Dp(6), gtx.Dp(8))
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(6), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.row(gtx, v, origin)
	})
}

// row lays the strip out by hand rather than with Flex, because dragging
// needs to know where every box landed.
func (u *ui) row(gtx layout.Context, v View, origin image.Point) layout.Dimensions {
	type item struct {
		key  string // session key, "" for anything that is not a box
		gap  unit.Dp
		call op.CallOp
		size image.Point
	}
	var items []item
	add := func(key string, gap unit.Dp, w layout.Widget) {
		m := op.Record(gtx.Ops)
		d := w(gtx)
		items = append(items, item{key: key, gap: gap, call: m.Stop(), size: d.Size})
	}

	add("", 0, u.grip)
	if v.Here > 0 {
		add("", 4, func(gtx layout.Context) layout.Dimensions { return u.here(gtx, v) })
	}
	if len(v.Boxes) == 0 {
		add("", 8, u.empty)
	}
	desk := -1
	for _, b := range u.ordered(v) {
		gap := unit.Dp(10)
		if b.Desk != desk {
			desk = b.Desk
			gap = 16
			if desk > 0 {
				n, c := strconv.Itoa(desk), colDim
				if desk == v.Here {
					c = colHere
				}
				add("", gap, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, n, 10, c, true) })
				gap = 4
			}
		}
		b := b
		add(b.Key, gap, func(gtx layout.Context) layout.Dimensions { return u.box(gtx, b) })
	}
	clock := time.Now().Format("15:04")
	add("", 16, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, clock, 13, colName, true) })

	h := 0
	for _, it := range items {
		if it.size.Y > h {
			h = it.size.Y
		}
	}
	slots := map[string]image.Rectangle{}
	x := 0
	for _, it := range items {
		x += gtx.Dp(it.gap)
		at := image.Pt(x, (h-it.size.Y)/2)
		off := op.Offset(at).Push(gtx.Ops)
		it.call.Add(gtx.Ops)
		off.Pop()
		if it.key != "" {
			slots[it.key] = image.Rectangle{Min: origin.Add(at), Max: origin.Add(at).Add(it.size)}
		}
		x += it.size.X
	}
	u.slots = slots
	return layout.Dimensions{Size: image.Pt(x, h)}
}

// ordered is the view's boxes, or the drag preview's order while dragging.
func (u *ui) ordered(v View) []Box {
	if !u.drag.active {
		return v.Boxes
	}
	byKey := make(map[string]Box, len(v.Boxes))
	for _, b := range v.Boxes {
		byKey[b.Key] = b
	}
	out := make([]Box, 0, len(v.Boxes))
	for _, k := range u.drag.order {
		if b, ok := byKey[k]; ok {
			out = append(out, b)
			delete(byKey, k)
		}
	}
	for _, b := range v.Boxes { // sessions that appeared mid-drag
		if _, ok := byKey[b.Key]; ok {
			out = append(out, b)
		}
	}
	return out
}

// here is the badge for the desktop this strip sits on: its number, and its
// name when you have given it one.
func (u *ui) here(gtx layout.Context, v View) layout.Dimensions {
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Left: unit.Dp(6), Right: unit.Dp(6), Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.label(gtx, strconv.Itoa(v.Here), 13, colName, true)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.HereName == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, truncate(v.HereName, maxName), 11, colName, false)
				})
			}),
		)
	})
	content := m.Stop()
	paint.FillShape(gtx.Ops, colBadge, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(5)).Op(gtx.Ops))
	content.Add(gtx.Ops)
	return dims
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
	if u.drag.active && u.drag.key == b.Key {
		paint.FillShape(gtx.Ops, colDrag, rr.Op(gtx.Ops))
	}
	if b.OnHere {
		paint.FillShape(gtx.Ops, colHereBg, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, colHere, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1}.Op())
	}
	content.Add(gtx.Ops)

	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, u.tag(b.Key))
	switch {
	case u.drag.active && u.drag.key == b.Key:
		pointer.CursorGrabbing.Add(gtx.Ops)
	case b.HWND != 0:
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

// handle reads last frame's pointer events. Secondary anywhere closes the
// strip. On a box, a press that moves sideways drags it to a new place in its
// desktop's group; one that does not focuses its terminal on release.
// Actions leave the event goroutine.
func (u *ui) handle(gtx layout.Context, v View) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.bg, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Buttons.Contain(pointer.ButtonSecondary) {
			go u.s.close()
		}
	}
	for _, b := range v.Boxes {
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: u.tag(b.Key), Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
			if !ok {
				break
			}
			e, ok := ev.(pointer.Event)
			if !ok {
				continue
			}
			x := float32(u.slots[b.Key].Min.X) + e.Position.X
			switch e.Kind {
			case pointer.Press:
				if e.Buttons.Contain(pointer.ButtonSecondary) {
					go u.s.close()
					continue
				}
				u.drag = drag{key: b.Key, primary: e.Buttons.Contain(pointer.ButtonPrimary), pressX: x}
			case pointer.Drag:
				if u.drag.key != b.Key || !u.drag.primary {
					continue
				}
				if !u.drag.active {
					if d := x - u.drag.pressX; d > -float32(gtx.Dp(5)) && d < float32(gtx.Dp(5)) {
						continue
					}
					u.drag.active = true
					u.drag.order = keysOf(v.Boxes)
				}
				u.drag.order = u.preview(v, x)
				gtx.Execute(op.InvalidateCmd{})
			case pointer.Release:
				if u.drag.key != b.Key {
					continue
				}
				d := u.drag
				u.drag = drag{}
				switch {
				case d.active:
					go u.s.setOrder(d.order)
				case d.primary && b.HWND != 0:
					go u.s.focus(b.HWND)
				}
				gtx.Execute(op.InvalidateCmd{})
			case pointer.Cancel:
				u.drag = drag{}
				gtx.Execute(op.InvalidateCmd{})
			}
		}
	}
}

// preview is the order with the dragged box placed before the first box
// whose middle lies right of the pointer, then pulled back inside its own
// desktop's group.
func (u *ui) preview(v View, x float32) []string {
	to := 0
	for _, k := range u.drag.order {
		if k == u.drag.key {
			continue
		}
		r := u.slots[k]
		if float32(r.Min.X+r.Max.X)/2 < x {
			to++
		}
	}
	keys := Move(u.drag.order, u.drag.key, to)
	boxes := append([]Box(nil), u.ordered(v)...)
	SortBoxes(boxes, keys)
	return keysOf(boxes)
}

func keysOf(boxes []Box) []string {
	out := make([]string, len(boxes))
	for i, b := range boxes {
		out[i] = b.Key
	}
	return out
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
