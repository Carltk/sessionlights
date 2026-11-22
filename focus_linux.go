package main

// Click-to-focus on Linux. Two steps, because neither alone is enough on
// Wayland:
//
//  1. Inside kitty, switch to the session's tab and window over kitty's
//     remote-control socket. kitty can do this, but Wayland will not let it
//     raise its own OS window without an input event.
//  2. Ask the SessionLights GNOME Shell extension to raise the window that
//     owns the session's process (it walks up to the terminal). The shell is
//     the one party Wayland lets do that.
//
// Any terminal gets step 2; step 1 needs kitty with `listen_on` set.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	shellHelperName  = "io.github.carltk.SessionLights.Shell"
	shellHelperPath  = "/io/github/carltk/SessionLights/Shell"
	shellHelperIface = "io.github.carltk.SessionLights.Shell"
)

// processEnv reads the environment of a process this user owns.
func processEnv(pid int) map[string]string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if k, v, ok := strings.Cut(string(kv), "="); ok {
			env[k] = v
		}
	}
	return env
}

// kittyTarget is where kitty listens and which of its windows holds the
// session, from the session process's environment. ok is false outside kitty
// or when kitty has no listen_on socket. KITTY_LISTEN_ON is used when set;
// kitty does not always export it, so otherwise the socket is found by
// KITTY_PID: kitty appends "-<pid>" to the listen_on name.
func kittyTarget(env map[string]string, unixSockets []string) (addr string, windowID int, ok bool) {
	n, err := strconv.Atoi(env["KITTY_WINDOW_ID"])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	if listen := env["KITTY_LISTEN_ON"]; listen != "" {
		// Only unix sockets; "unix:@name" is a Linux abstract socket, which
		// Go dials as "@name".
		path, isUnix := strings.CutPrefix(listen, "unix:")
		if !isUnix || path == "" {
			return "", 0, false
		}
		return path, n, true
	}
	pid := env["KITTY_PID"]
	if pid == "" {
		return "", 0, false
	}
	for _, sock := range unixSockets {
		if strings.HasSuffix(sock, "-"+pid) {
			return sock, n, true
		}
	}
	return "", 0, false
}

// listeningUnixSockets is the bound names in /proc/net/unix (abstract ones
// start with "@"). Only listening sockets (state 01, flag __SO_ACCEPTCON).
func listeningUnixSockets() []string {
	b, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		// Num RefCount Protocol Flags Type St Inode Path
		if len(f) < 8 || f[3] != "00010000" {
			continue
		}
		out = append(out, f[7])
	}
	return out
}

// kittyCommand frames one remote-control command as kitty expects it.
func kittyCommand(cmd string, payload any) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"cmd":         cmd,
		"version":     []int{0, 26, 0},
		"no_response": true,
		"payload":     payload,
	})
	if err != nil {
		return nil, err
	}
	out := append([]byte("\x1bP@kitty-cmd"), body...)
	return append(out, "\x1b\\"...), nil
}

// kittyFocus switches kitty to window id. It does not wait for a reply.
func kittyFocus(addr string, id int) error {
	msg, err := kittyCommand("focus-window", map[string]any{"match": fmt.Sprintf("id:%d", id)})
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("unix", addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, err = c.Write(msg)
	return err
}

// debugf logs only when SESSIONLIGHTS_DEBUG is set.
func debugf(format string, a ...any) {
	if os.Getenv("SESSIONLIGHTS_DEBUG") != "" {
		log.Printf(format, a...)
	}
}

// focusSession brings the session's terminal forward.
func focusSession(conn *dbus.Conn, pid int) error {
	if addr, id, ok := kittyTarget(processEnv(pid), listeningUnixSockets()); ok {
		err := kittyFocus(addr, id)
		debugf("kitty %s window %d: %v", addr, id, err)
		if err == nil {
			// Let kitty switch tabs (and mark the right OS window as wanting
			// attention) before the shell picks a window to raise.
			time.Sleep(80 * time.Millisecond)
		}
	}
	var found bool
	err := conn.Object(shellHelperName, shellHelperPath).
		Call(shellHelperIface+".ActivateProcess", 0, uint32(pid)).Store(&found)
	if err != nil {
		return fmt.Errorf("shell helper (is the GNOME extension enabled?): %w", err)
	}
	if !found {
		return fmt.Errorf("no window owns pid %d or its ancestors", pid)
	}
	return nil
}
