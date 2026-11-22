package main

// A StatusNotifierItem: the tray protocol GNOME's AppIndicator extension, KDE
// and most Wayland bars draw in their top panel. Each item is one object path
// on our single D-Bus connection, registered with the watcher by
// "<unique name>@<path>"-style path registration.

import (
	"fmt"
	"log"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sniIface     = "org.kde.StatusNotifierItem"
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = "/StatusNotifierWatcher"
	watcherIface = "org.kde.StatusNotifierWatcher"
)

// toolTip is SNI's (icon name, icon pixmaps, title, body).
type toolTip struct {
	Icon   string
	Pixmap []pixmap
	Title  string
	Body   string
}

type trayItem struct {
	conn  *dbus.Conn
	path  dbus.ObjectPath
	props *prop.Properties

	mu       sync.Mutex
	icon     []pixmap
	iconFile string
	label    string
	title    string
	status   string
}

// sniMethods are the calls a host makes on a click or scroll. A left click
// runs onActivate, off the D-Bus goroutine; everything else is ignored.
type sniMethods struct{ onActivate func() }

func (m sniMethods) Activate(x, y int32) *dbus.Error {
	if m.onActivate != nil {
		go m.onActivate()
	}
	return nil
}
func (sniMethods) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (sniMethods) ContextMenu(x, y int32) *dbus.Error       { return nil }
func (sniMethods) Scroll(d int32, o string) *dbus.Error     { return nil }

const sniIntrospect = `<interface name="org.kde.StatusNotifierItem">
<property name="Category" type="s" access="read"/>
<property name="Id" type="s" access="read"/>
<property name="Title" type="s" access="read"/>
<property name="Status" type="s" access="read"/>
<property name="WindowId" type="i" access="read"/>
<property name="IconName" type="s" access="read"/>
<property name="IconPixmap" type="a(iiay)" access="read"/>
<property name="ItemIsMenu" type="b" access="read"/>
<property name="Menu" type="o" access="read"/>
<property name="IconThemePath" type="s" access="read"/>
<property name="OverlayIconName" type="s" access="read"/>
<property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
<property name="AttentionIconName" type="s" access="read"/>
<property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
<property name="AttentionMovieName" type="s" access="read"/>
<property name="IconAccessibleDesc" type="s" access="read"/>
<property name="AttentionAccessibleDesc" type="s" access="read"/>
<property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
<property name="XAyatanaLabel" type="s" access="read"/>
<property name="XAyatanaLabelGuide" type="s" access="read"/>
<property name="XAyatanaOrderingIndex" type="u" access="read"/>
<method name="Activate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="SecondaryActivate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="ContextMenu"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
<method name="Scroll"><arg name="delta" type="i" direction="in"/><arg name="orientation" type="s" direction="in"/></method>
<signal name="NewTitle"/>
<signal name="NewIcon"/>
<signal name="NewToolTip"/>
<signal name="NewStatus"><arg name="status" type="s"/></signal>
<signal name="XAyatanaNewLabel"><arg name="label" type="s"/><arg name="guide" type="s"/></signal>
</interface>`

// newTrayItem exports an item at path. order is the AppIndicator ordering
// hint, so the segments keep their order across restarts.
func newTrayItem(conn *dbus.Conn, path dbus.ObjectPath, id string, order uint32, onActivate func()) (*trayItem, error) {
	t := &trayItem{conn: conn, path: path, status: "Active"}
	if err := conn.Export(sniMethods{onActivate: onActivate}, path, sniIface); err != nil {
		return nil, err
	}
	str := func(v string) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitFalse} }
	props, err := prop.Export(conn, path, prop.Map{sniIface: {
		"Category":   str("SystemServices"),
		"Id":         str(id),
		"Title":      str(id),
		"Status":     str("Active"),
		"WindowId":   {Value: int32(0), Emit: prop.EmitFalse},
		"IconName":   str(""),
		"IconPixmap": {Value: []pixmap{}, Emit: prop.EmitFalse},
		"ItemIsMenu": {Value: false, Emit: prop.EmitFalse},
		// GNOME's AppIndicator host requires Menu; this value means "none".
		"Menu":                    {Value: dbus.ObjectPath("/NO_DBUSMENU"), Emit: prop.EmitFalse},
		"IconThemePath":           str(""),
		"OverlayIconName":         str(""),
		"OverlayIconPixmap":       {Value: []pixmap{}, Emit: prop.EmitFalse},
		"AttentionIconName":       str(""),
		"AttentionIconPixmap":     {Value: []pixmap{}, Emit: prop.EmitFalse},
		"AttentionMovieName":      str(""),
		"IconAccessibleDesc":      str(""),
		"AttentionAccessibleDesc": str(""),
		"ToolTip":                 {Value: toolTip{}, Emit: prop.EmitFalse},
		"XAyatanaLabel":           str(""),
		"XAyatanaLabelGuide":      str(""),
		"XAyatanaOrderingIndex":   {Value: order, Emit: prop.EmitFalse},
	}})
	if err != nil {
		return nil, err
	}
	t.props = props
	node := &introspect.Node{Name: string(path), Interfaces: []introspect.Interface{
		introspect.IntrospectData, prop.IntrospectData,
	}}
	intro := introspect.NewIntrospectable(node)
	// Append the SNI interface by hand: introspect.Node has no raw-XML slot.
	raw := string(intro)
	raw = raw[:len(raw)-len("</node>")] + sniIntrospect + "</node>"
	if err := conn.Export(introspect.Introspectable(raw), path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}
	return t, nil
}

// register announces the item to the watcher. Called again whenever the
// watcher restarts (the shell reloaded, the extension was toggled).
func (t *trayItem) register() error {
	obj := t.conn.Object(watcherName, watcherPath)
	return obj.Call(watcherIface+".RegisterStatusNotifierItem", 0, string(t.path)).Err
}

// SetIcon replaces the pixmaps and tells the host, only when they changed.
func (t *trayItem) SetIcon(p []pixmap) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if samePixmaps(t.icon, p) {
		return
	}
	t.icon = p
	t.props.SetMust(sniIface, "IconPixmap", p)
	t.emit("NewIcon")
}

// SetIconFile points the item at a PNG by absolute path. GNOME's host draws
// a file at least 1.5x as wide as tall at its true size, where a pixmap is
// always squeezed into a square, so wide segments go this way. The path must
// change with the content: the host caches by name.
func (t *trayItem) SetIconFile(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.iconFile == path {
		return
	}
	t.iconFile = path
	t.props.SetMust(sniIface, "IconName", path)
	t.emit("NewIcon")
}

// SetActive shows or hides the item ("Passive" hides it in most hosts).
func (t *trayItem) SetActive(on bool) {
	st := "Passive"
	if on {
		st = "Active"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == st {
		return
	}
	t.status = st
	t.props.SetMust(sniIface, "Status", st)
	t.emit("NewStatus", st)
	if on {
		t.emit("NewIcon") // changes made while hidden were not drawn
	}
}

// SetLabel sets the text a host may draw beside the icon.
func (t *trayItem) SetLabel(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.label == s {
		return
	}
	t.label = s
	t.props.SetMust(sniIface, "XAyatanaLabel", s)
	t.emit("XAyatanaNewLabel", s, "")
}

// SetTitle is the item's accessible name and hover text.
func (t *trayItem) SetTitle(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.title == s {
		return
	}
	t.title = s
	t.props.SetMust(sniIface, "Title", s)
	t.props.SetMust(sniIface, "ToolTip", toolTip{Pixmap: []pixmap{}, Title: s})
	t.props.SetMust(sniIface, "IconAccessibleDesc", s)
	t.emit("NewTitle")
	t.emit("NewToolTip")
}

func (t *trayItem) emit(sig string, args ...any) {
	if err := t.conn.Emit(t.path, sniIface+"."+sig, args...); err != nil {
		log.Printf("emit %s %s: %v", t.path, sig, err)
	}
}

func samePixmaps(a, b []pixmap) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].W != b[i].W || a[i].H != b[i].H || string(a[i].Data) != string(b[i].Data) {
			return false
		}
	}
	return true
}

// watchWatcher re-registers every item when the watcher comes back.
func watchWatcher(conn *dbus.Conn, items func() []*trayItem) error {
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
				continue
			}
			if newOwner, _ := sig.Body[2].(string); newOwner != "" {
				for _, it := range items() {
					if err := it.register(); err != nil {
						log.Printf("re-register %s: %v", it.path, err)
					}
				}
			}
		}
	}()
	return nil
}

func itemPath(name string) dbus.ObjectPath {
	return dbus.ObjectPath(fmt.Sprintf("/SessionLights/%s", name))
}
