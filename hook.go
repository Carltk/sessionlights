package main

// Hook mode: `sessionlights hook` is the producer side. Claude Code runs it on
// session events with the event as JSON on stdin, and it writes the light file
// the strip reads. One binary does both jobs so the plugin needs no runtime.
//
// A light is a convenience, and a broken convenience must never wedge a
// session, so every path here exits 0 and fails silent.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// hookInput is the part of Claude Code's hook payload the lights use.
type hookInput struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	Cwd              string `json:"cwd"`
	Event            string `json:"hook_event_name"`
	SessionTitle     string `json:"session_title"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	Error            string `json:"error"`
	AgentID          string `json:"agent_id"`
}

// record is a light file as the hook writes it: the documented format plus two
// fields only the hook reads back (updatedAt, failures).
type record struct {
	SessionID string  `json:"sessionId"`
	Name      string  `json:"name"`
	Cwd       string  `json:"cwd"`
	State     string  `json:"state"`
	Since     string  `json:"since"`
	UpdatedAt string  `json:"updatedAt"`
	Detail    string  `json:"detail,omitempty"`
	Failures  int     `json:"failures,omitempty"`
	Window    *Window `json:"window,omitempty"`
	PID       int     `json:"pid,omitempty"`
	PIDStart  int64   `json:"pidStart,omitempty"`
}

// sessionProbe finds the session's process and terminal. Real on Windows,
// faked in tests; every method may return zero values.
type sessionProbe interface {
	// SessionPID is the nearest claude.exe ancestor of this process and its
	// start time in unix ms.
	SessionPID() (pid int, startMs int64)
	// TerminalWindow is the exact terminal window hosting pid's console.
	TerminalWindow(pid int) *Window
	// ForegroundTerminal is the window in front, when it is a terminal.
	ForegroundTerminal() *Window
}

// lightsDir is where the hook writes and the strip reads by default.
// SESSIONLIGHTS_DIR overrides it; CLAUDE_CONFIG_DIR moves its parent.
func lightsDir() string {
	if d := os.Getenv("SESSIONLIGHTS_DIR"); d != "" {
		return d
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "session-lights")
}

// runHookMain is the `sessionlights hook` entry point.
func runHookMain(probe sessionProbe) {
	defer func() { _ = recover() }()
	raw := readEvent(os.Stdin, stdinDeadline)
	_ = handleHook(raw, lightsDir(), probe, procInfo(), time.Now())
}

// stdinDeadline caps the wait for the payload. Claude Code writes it at once,
// so this only matters if the pipe is held open without data.
const stdinDeadline = 2 * time.Second

// readEvent returns the first JSON value on r. It stops at the end of that
// value rather than at EOF, so a pipe left open cannot hold the hook, and it
// gives up after the deadline. Large payloads (a big prompt or tool input)
// are read whole; 64 MiB is only a guard against a runaway writer.
func readEvent(r io.Reader, deadline time.Duration) []byte {
	got := make(chan []byte, 1)
	go func() {
		var v json.RawMessage
		if json.NewDecoder(io.LimitReader(r, 64<<20)).Decode(&v) != nil {
			got <- nil
			return
		}
		got <- v
	}()
	select {
	case b := <-got:
		return b
	case <-time.After(deadline):
		return nil
	}
}

var blockingRe = regexp.MustCompile(`(?i)permission|approve|waiting for your input|needs your input`)

// isBlocking says whether a Notification means Claude is stuck on the human.
func isBlocking(in hookInput) bool {
	switch in.NotificationType {
	case "permission_prompt", "elicitation_dialog":
		return true
	}
	return blockingRe.MatchString(in.Message)
}

var safeIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func lightFile(dir, sessionID string) string {
	safe := safeIDRe.ReplaceAllString(sessionID, "")
	if safe == "" {
		safe = "unknown"
	}
	return filepath.Join(dir, safe+".json")
}

// handleHook applies one event. Returns an error only for tests; the caller
// ignores it.
func handleHook(raw []byte, dir string, probe sessionProbe, proc Proc, now time.Time) error {
	var in hookInput
	if err := json.Unmarshal(bytes.TrimSpace(raw), &in); err != nil || in.SessionID == "" {
		return err
	}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	path := lightFile(dir, in.SessionID)

	type change struct {
		state    string
		detail   string
		failures string // reset | bump | keep
		capture  bool   // try the foreground window when the exact one fails
	}
	var c change
	switch in.Event {
	case "SubagentStart", "SubagentStop":
		return markAgent(path, in.AgentID, in.Event == "SubagentStart", now)
	case "PostToolUse":
		// Fires after every tool, so it only writes when it has something to
		// say: a tool ran, so a waiting light's permission was granted. A
		// sub-agent's tools share the session id but say nothing about the
		// prompt the main thread is waiting on, so they are ignored.
		if in.AgentID != "" {
			return nil
		}
		var prev record
		if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &prev) != nil || prev.State != "waiting" {
			return nil
		}
		c = change{state: "busy", failures: "keep"}
	case "SessionStart":
		_ = os.RemoveAll(AgentsDir(path)) // a resumed session starts with none
		c = change{state: "idle", failures: "reset", capture: true}
	case "UserPromptSubmit":
		c = change{state: "busy", failures: "reset", capture: true}
	case "Stop":
		c = change{state: "idle", failures: "keep"}
	case "Notification":
		if !isBlocking(in) {
			return nil
		}
		c = change{state: "waiting", detail: clipText(in.Message, 80), failures: "keep"}
	case "StopFailure":
		msg := in.Error
		if msg == "" {
			msg = "API error"
		}
		c = change{state: "failed", detail: clipText(msg, 80), failures: "keep"}
	case "PostToolUseFailure":
		c = change{state: "busy", failures: "bump"}
	case "SessionEnd":
		_ = os.RemoveAll(AgentsDir(path))
		retry(func() error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		})
		sweep(dir, proc, now, path)
		return nil
	default:
		return nil
	}

	var prev record
	havePrev := false
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &prev) == nil {
		havePrev = true
	}

	stamp := now.Format(time.RFC3339)
	r := record{
		SessionID: in.SessionID,
		Cwd:       in.Cwd,
		State:     c.state,
		Since:     stamp,
		UpdatedAt: stamp,
	}
	if havePrev {
		if prev.State == c.state && prev.Since != "" {
			r.Since = prev.Since // since moves only when the state does
		}
		r.Failures = prev.Failures
		r.Window = prev.Window
		r.PID = prev.PID
		r.PIDStart = prev.PIDStart
	}
	switch c.failures {
	case "reset":
		r.Failures = 0
	case "bump":
		r.Failures++
	}

	r.Name = sessionName(in, prev.Name)

	// Probe every time: a resumed session runs in a new claude.exe, and the
	// old pid in the file would tie the light to a dead or reused process.
	if pid, start := probe.SessionPID(); pid != 0 {
		r.PID, r.PIDStart = pid, start
	}
	if w := probe.TerminalWindow(r.PID); w != nil {
		r.Window = w // exact, re-read every event so a dragged tab follows
	} else if c.capture {
		if w := probe.ForegroundTerminal(); w != nil {
			r.Window = w
		}
	}

	r.Detail = c.detail
	if r.Detail == "" && r.Failures > 0 {
		r.Detail = plural(r.Failures, "tool failure")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	// A temp name per process, so two events for one session never share it.
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	// The strip may have the file open for a moment, and Windows refuses to
	// replace an open file, so a failed rename is retried rather than dropped.
	if err := retry(func() error { return os.Rename(tmp, path) }); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	sweep(dir, proc, now, path)
	return nil
}

// markAgent adds or removes one sub-agent's marker beside the light, then
// touches the light so the strip, which watches only the top folder, looks
// again. One file per agent id, so parallel starts cannot lose a count.
func markAgent(lightPath, agentID string, start bool, now time.Time) error {
	id := safeIDRe.ReplaceAllString(agentID, "")
	if id == "" {
		return nil
	}
	dir := AgentsDir(lightPath)
	marker := filepath.Join(dir, id)
	if start {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return err
		}
	} else {
		// The folder stays: removing it when empty would race a concurrent
		// start between its MkdirAll and WriteFile. SessionStart, SessionEnd
		// and the sweep remove it.
		_ = os.Remove(marker)
	}
	_ = os.Chtimes(lightPath, now, now)
	return nil
}

// retry runs f up to 10 times, 15 ms apart, for Windows sharing violations
// that clear as soon as a reader closes the file.
func retry(f func() error) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	return err
}

// sessionName picks the label: the title Claude Code passes (set by /rename or
// --name), else the newest custom-title in the transcript, else the name the
// light already had, else the folder name.
func sessionName(in hookInput, prevName string) string {
	if t := strings.TrimSpace(in.SessionTitle); t != "" {
		return t
	}
	if t := transcriptTitle(in.TranscriptPath, in.SessionID); t != "" {
		return t
	}
	if prevName != "" {
		return prevName
	}
	if base := filepath.Base(strings.TrimRight(in.Cwd, `\/`)); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	if len(in.SessionID) > 8 {
		return in.SessionID[:8]
	}
	return in.SessionID
}

// transcriptTail bounds how much of a transcript is scanned per event. A title
// set earlier than this is still kept, because the light carries its name
// forward.
const transcriptTail = 2 << 20

// transcriptTitle returns the newest custom-title record in the tail of the
// transcript, or "".
func transcriptTitle(path, sessionID string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > transcriptTail {
		_, _ = f.Seek(st.Size()-transcriptTail, io.SeekStart)
	}
	title := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	marker := []byte(`"custom-title"`)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, marker) {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			SessionID   string `json:"sessionId"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "custom-title" {
			continue
		}
		if rec.SessionID != "" && sessionID != "" && rec.SessionID != sessionID {
			continue
		}
		if t := strings.TrimSpace(rec.CustomTitle); t != "" {
			title = t
		}
	}
	return title
}

// maxLightSize skips anything too big to be a light, so a stray file in the
// folder cannot slow every hook down.
const maxLightSize = 64 << 10

// sweep removes light files whose session is gone, so a crash or reboot that
// skipped SessionEnd does not leave a box behind.
func sweep(dir string, proc Proc, now time.Time, own string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if full == own {
			continue // this session's light was just written; never sweep it
		}
		info, err := e.Info()
		if err != nil || info.Size() > maxLightSize {
			continue
		}
		var l Light
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) // the strip accepts a BOM, so the sweep must too
		if json.Unmarshal(b, &l) != nil || l.SessionID == "" || l.State == "" {
			continue // not a light; the folder may hold other files
		}
		l.modTime = info.ModTime()
		if !Alive(l, proc, now) {
			_ = os.Remove(full)
			_ = os.RemoveAll(AgentsDir(full))
		}
	}
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
