package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeProc map[int]struct {
	exe   string
	start int64
}

func (f fakeProc) Info(pid int) (bool, string, int64) {
	p, ok := f[pid]
	return ok, p.exe, p.start
}

type fakeDesk map[int64]bool

func (f fakeDesk) OnCurrent(h int64) bool { return f[h] }

// Window handles in the 200s live on desktop B, the rest on A.
func (f fakeDesk) DesktopOf(h int64) string {
	if h >= 200 {
		return "B"
	}
	return "A"
}

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func TestDotColour(t *testing.T) {
	cases := []struct {
		state, since string
		want         string
	}{
		{"idle", "", "idle"},
		{"waiting", "", "waiting"},
		{"failed", "", "failed"},
		{"busy", now.Add(-10 * time.Second).Format(time.RFC3339), "busy"},
		{"busy", now.Add(-301 * time.Second).Format(time.RFC3339), "busy"}, // no long-busy warning any more
		{"busy", "not a time", "busy"},
		{"whatever", "", "dim"},
	}
	names := map[string]any{"idle": colIdle, "waiting": colWaiting, "failed": colFailed, "busy": colBusy, "dim": colDim}
	for _, c := range cases {
		got := dotColour(Light{State: c.state, Since: c.since}, now)
		if got != names[c.want] {
			t.Errorf("%s since %q: got %v, want %s", c.state, c.since, got, c.want)
		}
	}
}

func TestTipText(t *testing.T) {
	cases := []struct {
		l    Light
		want string
	}{
		{Light{Name: "Research", State: "busy", Agents: 3}, "Research: busy · 3 sub-agents"},
		{Light{Name: "Research", State: "busy", Agents: 1}, "Research: busy · 1 sub-agent"},
		{Light{Name: "Sync", State: "failed", Detail: "API error"}, "Sync: failed, needs attention · API error"},
		{Light{Name: "X", State: "odd"}, "X: unknown"},
	}
	for _, c := range cases {
		if got := tipText(c.l); got != c.want {
			t.Errorf("tipText(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

// TestLoadLightsCountsAgents: one marker file per running sub-agent, in a
// directory beside the light, is the count.
func TestLoadLightsCountsAgents(t *testing.T) {
	dir := t.TempDir()
	light := filepath.Join(dir, "s1.json")
	if err := os.WriteFile(light, []byte(`{"sessionId":"s1","name":"One","state":"busy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s2.json"), []byte(`{"sessionId":"s2","state":"idle"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ad := AgentsDir(light)
	if err := os.MkdirAll(ad, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2"} {
		if err := os.WriteFile(filepath.Join(ad, id), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]int{}
	for _, l := range LoadLights(dir) {
		got[l.SessionID] = l.Agents
	}
	if len(got) != 2 || got["s1"] != 2 || got["s2"] != 0 {
		t.Errorf("agents = %v, want s1:2 s2:0 and the .agents dir not read as a light", got)
	}
}

func TestAlive(t *testing.T) {
	procs := fakeProc{
		10: {"claude.exe", 1_000_000},
		11: {"notepad.exe", 1_000_000},
		12: {"claude.exe", 0},
		13: {"claude.exe.old.1792015049048.11132", 1_000_000},
		14: {"claude.exe.bak", 1_000_000},
	}
	cases := []struct {
		name string
		l    Light
		want bool
	}{
		{"live, start matches", Light{PID: 10, PIDStart: 1_000_500}, true},
		{"live, start off by 3s: recycled", Light{PID: 10, PIDStart: 1_003_000}, false},
		{"pid gone", Light{PID: 99, PIDStart: 1}, false},
		{"pid reused by another exe", Light{PID: 11}, false},
		{"session survived a self-update", Light{PID: 13, PIDStart: 1_000_000}, true},
		{"self-update, no pidStart: name fallback", Light{PID: 13}, true},
		{"any rename, start matches: identity wins", Light{PID: 14, PIDStart: 1_000_000}, true},
		{"other name, no pidStart: rejected", Light{PID: 14}, false},
		{"start unreadable: keep", Light{PID: 12, PIDStart: 5}, true},
		{"no pidStart recorded: keep", Light{PID: 10}, true},
		{"no pid, fresh", Light{modTime: now.Add(-29 * time.Minute)}, true},
		{"no pid, stale", Light{modTime: now.Add(-31 * time.Minute)}, false},
	}
	for _, c := range cases {
		if got := Alive(c.l, procs, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildViewSortTruncateRing(t *testing.T) {
	procs := fakeProc{1: {"claude", 0}, 2: {"claude", 0}}
	lights := []Light{
		{SessionID: "b", Name: "Zeta session with a very long name", State: "idle", PID: 1, Window: &Window{HWND: 100}},
		{SessionID: "a", Name: "Alpha", State: "waiting", Detail: "Claude needs your permission to use Bash", PID: 2},
		{SessionID: "dead", Name: "Ghost", State: "idle", PID: 3},
	}
	v := BuildView(lights, procs, fakeDesk{100: true}, DeskInfo{}, "", nil, now)
	if len(v.Boxes) != 2 {
		t.Fatalf("want 2 live boxes, got %d", len(v.Boxes))
	}
	a, z := v.Boxes[0], v.Boxes[1]
	if a.Name != "Alpha" || z.Key != "b" {
		t.Errorf("not sorted by name: %q, %q", a.Name, z.Name)
	}
	if z.Name != "Zeta" {
		t.Errorf("name truncation: %q", z.Name)
	}
	if a.Tip != "Alpha: waiting for you · Claude needs your permission to use Bash" {
		t.Errorf("tip with detail: %q", a.Tip)
	}
	if z.Tip != "Zeta session with a very long name: idle" {
		t.Errorf("tip keeps the full name: %q", z.Tip)
	}
	if !z.OnHere || a.OnHere {
		t.Errorf("ring: zeta %v alpha %v", z.OnHere, a.OnHere)
	}
}

func TestBuildViewDesktopOrder(t *testing.T) {
	procs := fakeProc{1: {"claude", 0}}
	lights := []Light{
		{SessionID: "a1", Name: "A one", PID: 1, Window: &Window{HWND: 101}},
		{SessionID: "b1", Name: "B one", PID: 1, Window: &Window{HWND: 201}},
		{SessionID: "a2", Name: "A two", PID: 1, Window: &Window{HWND: 102}},
		{SessionID: "nowin", Name: "Aardvark", PID: 1},
		{SessionID: "b2", Name: "B two", PID: 1, Window: &Window{HWND: 202}},
	}
	// Task View shows B first, so B's sessions lead; nowin has no desktop and goes last.
	desks := DeskInfo{IDs: []string{"B", "A"}, Names: map[string]string{"B": "Work"}}
	v := BuildView(lights, procs, fakeDesk{}, desks, "b", []string{"b2", "a2"}, now)
	got := keysOf(v.Boxes)
	want := []string{"b2", "b1", "a2", "a1", "nowin"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v, want %v", got, want)
	}
	if v.Boxes[0].Desk != 1 || v.Boxes[2].Desk != 2 || v.Boxes[4].Desk != 0 {
		t.Errorf("desk numbers: %+v", v.Boxes)
	}
	if v.Here != 1 || v.HereName != "Work" {
		t.Errorf("here: %d %q (names are keyed upper-case, the lookup must match)", v.Here, v.HereName)
	}
}

func TestMoveStaysInDesktopGroup(t *testing.T) {
	boxes := []Box{{Key: "b1", Desk: 1}, {Key: "b2", Desk: 1}, {Key: "a1", Desk: 2}}
	cases := []struct {
		key  string
		to   int
		want string
	}{
		{"b2", 0, "b2,b1,a1"},
		{"b1", 2, "b2,b1,a1"},  // past the end of its group: stops at the edge
		{"a1", 0, "b1,b2,a1"},  // cannot jump ahead of another desktop
		{"b1", 99, "b2,b1,a1"}, // index past the end is clamped
	}
	for _, c := range cases {
		keys := Move(keysOf(boxes), c.key, c.to)
		bs := append([]Box(nil), boxes...)
		SortBoxes(bs, keys)
		if got := strings.Join(keysOf(bs), ","); got != c.want {
			t.Errorf("move %s to %d: got %s, want %s", c.key, c.to, got, c.want)
		}
	}
}

func TestViewEqual(t *testing.T) {
	a := View{Boxes: []Box{{Key: "x", Dot: colIdle}}}
	b := View{Boxes: []Box{{Key: "x", Dot: colIdle}}}
	c := View{Boxes: []Box{{Key: "x", Dot: colBusy}}}
	if !a.Equal(b) || a.Equal(c) || a.Equal(View{}) {
		t.Error("View.Equal wrong")
	}
}

func TestLoadLightsSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"sessionId":"s","name":"N","state":"idle","window":{"hwnd":66466,"pid":1},"pid":2196,"pidStart":1790724369056}`), 0o644)
	os.WriteFile(filepath.Join(dir, "bom.json"), append([]byte{0xEF, 0xBB, 0xBF}, `{"sessionId":"b","name":"B","state":"busy"}`...), 0o644)
	os.WriteFile(filepath.Join(dir, "half.json"), []byte(`{"sessionId":`), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`x`), 0o644)
	got := LoadLights(dir)
	if len(got) != 2 {
		t.Fatalf("want good + bom, got %+v", got)
	}
	byName := map[string]Light{got[0].Name: got[0], got[1].Name: got[1]}
	if n := byName["N"]; n.Window == nil || n.Window.HWND != 66466 || n.PIDStart != 1790724369056 {
		t.Errorf("good file: %+v", n)
	}
	if b := byName["B"]; b.State != "busy" {
		t.Errorf("BOM file: %+v", b)
	}
	if LoadLights(filepath.Join(dir, "missing")) != nil {
		t.Error("missing dir should give nil")
	}
}

func TestBuildViewAttachID(t *testing.T) {
	now := time.Now()
	lights := []Light{
		{SessionID: "a", Name: "A", State: "idle", AttachID: "0fdaa8eb", modTime: now},
		{SessionID: "b", Name: "B", State: "idle", AttachID: "0fdaa8eb & calc", modTime: now},
		{SessionID: "c", Name: "C", State: "idle", modTime: now},
	}
	v := BuildView(lights, fakeProc{}, fakeDesk{}, DeskInfo{}, "", nil, now)
	got := map[string]string{}
	for _, b := range v.Boxes {
		got[b.Key] = b.Attach
	}
	if got["a"] != "0fdaa8eb" || got["b"] != "" || got["c"] != "" {
		t.Errorf("attach ids: %+v", got)
	}
}
