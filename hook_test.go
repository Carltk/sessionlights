package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeProbe struct {
	pid   int
	start int64
	exact *Window
	front *Window
}

func (f fakeProbe) SessionPID() (int, int64)    { return f.pid, f.start }
func (f fakeProbe) TerminalWindow(int) *Window  { return f.exact }
func (f fakeProbe) ForegroundTerminal() *Window { return f.front }

// liveProc says every pid is a live claude.exe started at 1000.
type liveProc struct{}

func (liveProc) Info(int) (bool, string, int64) { return true, "claude.exe", 1000 }

const sid = "0f8e2c1a-5b7d-4e3f-9a61-2d4c8b0e7f13"

func hookEvent(t *testing.T, name string, extra map[string]string) []byte {
	t.Helper()
	m := map[string]string{"session_id": sid, "cwd": filepath.Join("code", "my-project"), "hook_event_name": name}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func readRec(t *testing.T, dir string) (record, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, sid+".json"))
	if err != nil {
		return record{}, false
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("light is not JSON: %v", err)
	}
	return r, true
}

func TestHookTransitions(t *testing.T) {
	dir := t.TempDir()
	p := fakeProbe{pid: 4242, start: 1000, exact: &Window{HWND: 66466, PID: 11740}}
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

	steps := []struct {
		event    string
		extra    map[string]string
		state    string
		detail   string
		failures int
		gone     bool
	}{
		{"SessionStart", nil, "idle", "", 0, false},
		{"UserPromptSubmit", nil, "busy", "", 0, false},
		{"PostToolUseFailure", nil, "busy", "1 tool failure", 1, false},
		{"PostToolUseFailure", nil, "busy", "2 tool failures", 2, false},
		{"Notification", map[string]string{"message": "Claude is done thinking"}, "busy", "2 tool failures", 2, false},
		{"Notification", map[string]string{"message": "Claude needs your permission to use Bash"}, "waiting", "Claude needs your permission to use Bash", 2, false},
		{"Stop", nil, "idle", "2 tool failures", 2, false},
		{"UserPromptSubmit", nil, "busy", "", 0, false},
		{"StopFailure", map[string]string{"error": "rate_limit"}, "failed", "rate_limit", 0, false},
		{"StopFailure", nil, "failed", "API error", 0, false},
		{"SessionEnd", nil, "", "", 0, true},
	}
	for i, s := range steps {
		if err := handleHook(hookEvent(t, s.event, s.extra), dir, p, liveProc{}, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("step %d %s: %v", i, s.event, err)
		}
		r, ok := readRec(t, dir)
		if s.gone {
			if ok {
				t.Fatalf("step %d %s: light should be removed", i, s.event)
			}
			continue
		}
		if !ok {
			t.Fatalf("step %d %s: no light written", i, s.event)
		}
		if r.State != s.state || r.Detail != s.detail || r.Failures != s.failures {
			t.Errorf("step %d %s: got state=%q detail=%q failures=%d, want %q %q %d", i, s.event, r.State, r.Detail, r.Failures, s.state, s.detail, s.failures)
		}
		if r.PID != 4242 || r.PIDStart != 1000 || r.Window == nil || r.Window.HWND != 66466 {
			t.Errorf("step %d %s: process/window not recorded: %+v", i, s.event, r)
		}
	}
}

func TestHookSinceMovesOnlyOnStateChange(t *testing.T) {
	dir := t.TempDir()
	p := fakeProbe{pid: 1, start: 1000}
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	handleHook(hookEvent(t, "UserPromptSubmit", nil), dir, p, liveProc{}, t0)
	handleHook(hookEvent(t, "PostToolUseFailure", nil), dir, p, liveProc{}, t0.Add(time.Minute))
	r, _ := readRec(t, dir)
	if r.Since != t0.Format(time.RFC3339) {
		t.Fatalf("busy→busy moved since to %s", r.Since)
	}
	handleHook(hookEvent(t, "Stop", nil), dir, p, liveProc{}, t0.Add(2*time.Minute))
	r, _ = readRec(t, dir)
	if r.Since != t0.Add(2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("busy→idle kept since %s", r.Since)
	}
}

func TestHookNameSources(t *testing.T) {
	dir := t.TempDir()
	p := fakeProbe{}
	now := time.Now()

	// Folder name when nothing else is known.
	handleHook(hookEvent(t, "SessionStart", nil), dir, p, liveProc{}, now)
	if r, _ := readRec(t, dir); r.Name != "my-project" {
		t.Fatalf("folder fallback: got %q", r.Name)
	}

	// Transcript custom-title (what /rename writes), newest wins, other sessions ignored.
	tr := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tr, []byte(
		`{"type":"user","message":"hi"}`+"\n"+
			`{"type":"custom-title","customTitle":"First","sessionId":"`+sid+`"}`+"\n"+
			`{"type":"custom-title","customTitle":"Other","sessionId":"someone-else"}`+"\n"+
			`{"type":"custom-title","customTitle":"Research","sessionId":"`+sid+`"}`+"\n"), 0o644)
	handleHook(hookEvent(t, "Stop", map[string]string{"transcript_path": tr}), dir, p, liveProc{}, now)
	if r, _ := readRec(t, dir); r.Name != "Research" {
		t.Fatalf("transcript title: got %q", r.Name)
	}

	// session_title from Claude Code beats the transcript.
	handleHook(hookEvent(t, "UserPromptSubmit", map[string]string{"transcript_path": tr, "session_title": "Live title"}), dir, p, liveProc{}, now)
	if r, _ := readRec(t, dir); r.Name != "Live title" {
		t.Fatalf("session_title: got %q", r.Name)
	}

	// Later event with no title anywhere keeps the name the light had.
	handleHook(hookEvent(t, "Stop", nil), dir, p, liveProc{}, now)
	if r, _ := readRec(t, dir); r.Name != "Live title" {
		t.Fatalf("carry forward: got %q", r.Name)
	}
}

func TestHookForegroundOnlyOnPromptOrStart(t *testing.T) {
	dir := t.TempDir()
	front := &Window{HWND: 7, PID: 8}
	p := fakeProbe{front: front}
	handleHook(hookEvent(t, "Stop", nil), dir, p, liveProc{}, time.Now())
	if r, _ := readRec(t, dir); r.Window != nil {
		t.Fatalf("Stop must not guess a window: %+v", r.Window)
	}
	handleHook(hookEvent(t, "UserPromptSubmit", nil), dir, p, liveProc{}, time.Now())
	if r, _ := readRec(t, dir); r.Window == nil || r.Window.HWND != 7 {
		t.Fatalf("prompt should capture foreground terminal: %+v", r.Window)
	}
}

func TestHookIgnoresBadInput(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{"", "   ", "not json", `{"hook_event_name":"Stop"}`, `{"session_id":"x","hook_event_name":"PreToolUse"}`} {
		handleHook([]byte(raw), dir, fakeProbe{}, liveProc{}, time.Now())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("bad input wrote %d files", len(entries))
	}
}

func TestHookSweepsDeadLights(t *testing.T) {
	dir := t.TempDir()
	dead, _ := json.Marshal(Light{SessionID: "dead", Name: "x", State: "idle", PID: 99, PIDStart: 900000})
	os.WriteFile(filepath.Join(dir, "dead.json"), dead, 0o644)
	os.WriteFile(filepath.Join(dir, "session-lights-order.json"), []byte(`["a","b"]`), 0o644)
	os.MkdirAll(filepath.Join(dir, "dead.agents"), 0o755)
	os.WriteFile(filepath.Join(dir, "dead.agents", "a1"), nil, 0o644)
	handleHook(hookEvent(t, "SessionStart", nil), dir, fakeProbe{}, liveProc{}, time.Now()) // liveProc start 1000, far outside the 2 s slop
	if _, err := os.Stat(filepath.Join(dir, "dead.json")); !os.IsNotExist(err) {
		t.Fatal("light with a recycled pid survived the sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, "dead.agents")); !os.IsNotExist(err) {
		t.Fatal("a swept light's sub-agent markers survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "session-lights-order.json")); err != nil {
		t.Fatal("sweep removed a file that is not a light")
	}
}

func TestLightsDirOverride(t *testing.T) {
	t.Setenv("SESSIONLIGHTS_DIR", `X:\lights`)
	if got := lightsDir(); got != `X:\lights` {
		t.Fatalf("override: %s", got)
	}
	t.Setenv("SESSIONLIGHTS_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", `Y:\cfg`)
	if got := lightsDir(); got != filepath.Join(`Y:\cfg`, "session-lights") {
		t.Fatalf("config dir: %s", got)
	}
}

func TestReadEventStopsAtFirstValueOnOpenPipe(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write([]byte(`{"session_id":"a","hook_event_name":"Stop"}`)) // pipe never closed
	start := time.Now()
	b := readEvent(pr, 2*time.Second)
	if len(b) == 0 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("got %q after %v", b, time.Since(start))
	}
}

func TestReadEventGivesUpOnSilentPipe(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	start := time.Now()
	if b := readEvent(pr, 100*time.Millisecond); b != nil {
		t.Fatalf("got %q", b)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v", d)
	}
}

func TestReadEventLargePayload(t *testing.T) {
	big := `{"session_id":"a","hook_event_name":"Stop","prompt":"` + strings.Repeat("x", 3<<20) + `"}`
	if b := readEvent(strings.NewReader(big), time.Second); len(b) != len(big) {
		t.Fatalf("read %d of %d bytes", len(b), len(big))
	}
}

func TestHookResumeReprobesProcess(t *testing.T) {
	dir := t.TempDir()
	handleHook(hookEvent(t, "SessionStart", nil), dir, fakeProbe{pid: 111, start: 1000}, liveProc{}, time.Now())
	handleHook(hookEvent(t, "SessionStart", nil), dir, fakeProbe{pid: 222, start: 1000}, liveProc{}, time.Now())
	if r, _ := readRec(t, dir); r.PID != 222 {
		t.Fatalf("resumed session kept old pid %d", r.PID)
	}
}

func TestSweepBOMAndNonLights(t *testing.T) {
	dir := t.TempDir()
	dead, _ := json.Marshal(Light{SessionID: "dead", Name: "x", State: "idle", PID: 99, PIDStart: 900000})
	os.WriteFile(filepath.Join(dir, "bom.json"), append([]byte{0xEF, 0xBB, 0xBF}, dead...), 0o644)
	os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"sessionId":"z","pid":99,"pidStart":900000}`), 0o644)
	handleHook(hookEvent(t, "SessionStart", nil), dir, fakeProbe{}, liveProc{}, time.Now())
	if _, err := os.Stat(filepath.Join(dir, "bom.json")); !os.IsNotExist(err) {
		t.Fatal("dead light with a BOM was not swept")
	}
	if _, err := os.Stat(filepath.Join(dir, "other.json")); err != nil {
		t.Fatal("a JSON file with no state was swept")
	}
}

func TestHookNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		handleHook(hookEvent(t, "PostToolUseFailure", nil), dir, fakeProbe{}, liveProc{}, time.Now())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only the light, got %d files", len(entries))
	}
}

// TestHookSubagentsAndPermissionClear: sub-agents are counted one marker per
// agent id, and a tool running after a permission prompt turns red back to
// busy, while PostToolUse on a busy light writes nothing.
func TestHookSubagentsAndPermissionClear(t *testing.T) {
	dir := t.TempDir()
	p := fakeProbe{pid: 4242, start: 1000}
	now := time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	ev := func(name string, extra map[string]string) {
		t.Helper()
		if err := handleHook(hookEvent(t, name, extra), dir, p, liveProc{}, now); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	count := func() int {
		for _, l := range LoadLights(dir) {
			if l.SessionID == sid {
				return l.Agents
			}
		}
		return -1
	}

	ev("UserPromptSubmit", nil)
	ev("SubagentStart", map[string]string{"agent_id": "a1"})
	ev("SubagentStart", map[string]string{"agent_id": "a2"})
	ev("SubagentStart", map[string]string{"agent_id": "a2"}) // repeated start is still one agent
	if n := count(); n != 2 {
		t.Fatalf("agents after two starts = %d, want 2", n)
	}
	ev("SubagentStop", map[string]string{"agent_id": "a1"})
	if n := count(); n != 1 {
		t.Fatalf("agents after one stop = %d, want 1", n)
	}

	before, _ := os.ReadFile(filepath.Join(dir, sid+".json"))
	ev("PostToolUse", nil)
	after, _ := os.ReadFile(filepath.Join(dir, sid+".json"))
	if string(before) != string(after) {
		t.Error("PostToolUse rewrote a busy light")
	}

	ev("Notification", map[string]string{"notification_type": "permission_prompt", "message": "Claude needs your permission to use Bash"})
	if r, _ := readRec(t, dir); r.State != "waiting" {
		t.Fatalf("state = %q, want waiting", r.State)
	}
	ev("PostToolUse", map[string]string{"agent_id": "a2"}) // a sub-agent's tool says nothing about the main prompt
	if r, _ := readRec(t, dir); r.State != "waiting" {
		t.Fatalf("sub-agent PostToolUse cleared the wait: state %q", r.State)
	}
	ev("PostToolUse", nil)
	if r, _ := readRec(t, dir); r.State != "busy" || r.Detail != "" {
		t.Errorf("after the approved tool: state %q detail %q, want busy and no detail", r.State, r.Detail)
	}

	ev("SessionEnd", nil)
	if _, err := os.Stat(AgentsDir(filepath.Join(dir, sid+".json"))); !os.IsNotExist(err) {
		t.Errorf("agents dir survives SessionEnd: %v", err)
	}
}

// TestHookStopPrunesStaleAgents: at the main thread's Stop, markers for
// agents not in background_tasks go (their SubagentStop never came); a
// running workflow, or a Stop without the field, keeps them all.
func TestHookStopPrunesStaleAgents(t *testing.T) {
	dir := t.TempDir()
	p := fakeProbe{pid: 4242, start: 1000}
	now := time.Now()
	raw := func(js string) {
		t.Helper()
		if err := handleHook([]byte(js), dir, p, liveProc{}, now); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		for _, l := range LoadLights(dir) {
			if l.SessionID == sid {
				return l.Agents
			}
		}
		return -1
	}
	start := func(ids ...string) {
		for _, id := range ids {
			raw(`{"session_id":"` + sid + `","hook_event_name":"SubagentStart","agent_id":"` + id + `"}`)
		}
	}
	raw(`{"session_id":"` + sid + `","hook_event_name":"UserPromptSubmit"}`)
	start("fg1", "bg1", "bg2")

	raw(`{"session_id":"` + sid + `","hook_event_name":"Stop"}`) // older CLI: no field, no pruning
	if n := count(); n != 3 {
		t.Fatalf("no background_tasks field: agents = %d, want 3", n)
	}
	raw(`{"session_id":"` + sid + `","hook_event_name":"Stop","background_tasks":[{"id":"bg1","type":"local_agent","status":"running"},{"id":"w1","type":"local_workflow","status":"running"}]}`)
	if n := count(); n != 3 {
		t.Fatalf("workflow running: agents = %d, want 3 (its agents are not listed)", n)
	}
	raw(`{"session_id":"` + sid + `","hook_event_name":"SubagentStop","agent_id":"x","background_tasks":[]}`) // a sub-agent's own stop never prunes
	if n := count(); n != 3 {
		t.Fatalf("SubagentStop pruned: agents = %d, want 3", n)
	}
	raw(`{"session_id":"` + sid + `","hook_event_name":"Stop","background_tasks":[{"id":"bg1","type":"local_agent","status":"running"},{"id":"b9","type":"local_bash","status":"running"}]}`)
	if n := count(); n != 1 {
		t.Fatalf("after prune: agents = %d, want 1 (bg1 only)", n)
	}
	raw(`{"session_id":"` + sid + `","hook_event_name":"Stop","background_tasks":[]}`)
	if n := count(); n != 0 {
		t.Fatalf("nothing in the background: agents = %d, want 0", n)
	}
}
