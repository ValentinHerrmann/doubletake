package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"doubletake/internal/daemon/daemonclient"
)

const daemonStartTimeout = 5 * time.Second

var waylandSock = regexp.MustCompile(`^wayland-\d+$`)

// ensureDaemon makes sure a doubletake daemon answers on the client's socket,
// starting one if needed. The daemon runs in its own session so casting
// continues after the UI window is closed.
func ensureDaemon(client *daemonclient.Client, binary string, noAudio bool) error {
	if _, err := client.Status(); err == nil {
		return nil
	}
	if binary == "" {
		return errors.New("doubletake not found; pass -doubletake <path>")
	}
	args := []string{"-daemonize", "-socket", client.SocketPath}
	if noAudio {
		args = append(args, "-no-audio")
	}
	name, args := withoutCapabilities(binary, args)
	cmd := exec.Command(name, args...)
	cmd.Env = sessionEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start doubletake daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(daemonStartTimeout)
	for {
		if _, err := client.Status(); err == nil {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("doubletake daemon exited: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("doubletake daemon did not start in time")
		}
	}
}

// findDoubletake prefers a doubletake built alongside this binary, since only
// the fork's build supports per-connection capture sources.
func findDoubletake(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, name := range []string{"doubletake", "doubletake-src"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return candidate
			}
		}
	}
	for _, name := range []string{"doubletake-src", "doubletake"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// sessionEnv fills in the desktop session variables GUI children need to reach
// the compositor, the screencast portal (session D-Bus) and PipeWire. They are
// missing when the UI is started from a system service.
func sessionEnv(base []string) []string {
	env := map[string]string{}
	var order []string
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	setDefault := func(k, v string) {
		if env[k] == "" {
			if _, seen := env[k]; !seen {
				order = append(order, k)
			}
			env[k] = v
		}
	}
	setDefault("XDG_RUNTIME_DIR", "/run/user/"+strconv.Itoa(os.Getuid()))
	runtimeDir := env["XDG_RUNTIME_DIR"]
	setDefault("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(runtimeDir, "bus"))
	if env["WAYLAND_DISPLAY"] == "" {
		if entries, err := os.ReadDir(runtimeDir); err == nil {
			for _, e := range entries {
				if waylandSock.MatchString(e.Name()) {
					setDefault("WAYLAND_DISPLAY", e.Name())
					break
				}
			}
		}
	}
	if env["WAYLAND_DISPLAY"] == "" {
		setDefault("DISPLAY", ":0")
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

// withoutCapabilities wraps a command in setpriv when this process holds
// ambient capabilities (e.g. CAP_NET_BIND_SERVICE from a systemd unit).
// Children inherit them, and xdg-desktop-portal then cannot inspect the
// caller ("Unable to open /proc/<pid>/root") and refuses the screencast.
func withoutCapabilities(name string, args []string) (string, []string) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !hasAmbientCaps(string(status)) {
		return name, args
	}
	setpriv, err := exec.LookPath("setpriv")
	if err != nil {
		return name, args
	}
	return setpriv, append([]string{"--inh-caps=-all", "--ambient-caps=-all", "--", name}, args...)
}

func hasAmbientCaps(procStatus string) bool {
	for _, line := range strings.Split(procStatus, "\n") {
		if v, ok := strings.CutPrefix(line, "CapAmb:"); ok {
			return strings.Trim(strings.TrimSpace(v), "0") != ""
		}
	}
	return false
}
