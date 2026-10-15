package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Session drives one interactive `doubletake` process. doubletake discovers
// receivers once per run, prints a numbered list and reads the choice (and any
// PIN) from stdin, so the UI answers those prompts and "refresh" restarts it.

type State string

const (
	StateIdle        State = "idle"
	StateDiscovering State = "discovering"
	StateChoose      State = "choose"
	StatePin         State = "pin"
	StateConnecting  State = "connecting"
	StateStreaming   State = "streaming"
	StateError       State = "error"
)

type Device struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Model string `json:"model"`
	IP    string `json:"ip"`
}

type Status struct {
	State   State    `json:"state"`
	Devices []Device `json:"devices"`
	Message string   `json:"message"`
	Source  string   `json:"source"`
}

const (
	stopGrace       = 3 * time.Second
	msgNoDevices    = "No receivers found. Press Refresh."
	msgPickerHint   = "Connecting… choose what to share when the screen-sharing dialog appears."
	msgSearching    = "Searching for receivers…"
	msgChoose       = "Choose a receiver."
	msgCastingEnded = "Casting ended."
)

var (
	deviceLine   = regexp.MustCompile(`^\s*\[(\d+)\]\s+(.+)\s+\(([^()]*)\)\s+-\s+(\S+)\s*$`)
	credPrompt   = regexp.MustCompile(`(?i)enter the .*(pin|code|password)`)
	noDevices    = regexp.MustCompile(`(?i)no apple tvs found`)
	goLogPrefix  = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)
	whitespaceRe = regexp.MustCompile(`\s+`)
	waylandSock  = regexp.MustCompile(`^wayland-\d+$`)
)

type process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
}

type Session struct {
	binary  string
	noAudio bool

	mu        sync.Mutex
	source    string
	proc      *process
	state     State
	devices   []Device
	message   string
	lastError string
}

func NewSession(binary, source string, noAudio bool) *Session {
	return &Session{binary: binary, source: source, noAudio: noAudio, state: StateIdle, devices: []Device{}}
}

func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	devices := append([]Device(nil), s.devices...)
	if devices == nil {
		devices = []Device{}
	}
	return Status{State: s.state, Devices: devices, Message: s.message, Source: s.source}
}

// Busy reports whether a connection is being set up or running, i.e. whether
// restarting doubletake would interrupt the user.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StatePin || s.state == StateConnecting || s.state == StateStreaming
}

func (s *Session) set(state State, msg string) {
	s.state = state
	s.message = msg
}

// SetSource changes the capture source used by the next Start.
func (s *Session) SetSource(source string) {
	s.mu.Lock()
	s.source = source
	s.mu.Unlock()
}

// Start (re)starts discovery, stopping any running doubletake first.
func (s *Session) Start() {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = []Device{}
	s.lastError = ""

	args := []string{"-source", s.source}
	if s.noAudio {
		args = append(args, "-no-audio")
	}
	name, args := withoutCapabilities(s.binary, args)
	cmd := exec.Command(name, args...)
	cmd.Env = sessionEnv(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.set(StateError, "Could not start doubletake: "+err.Error())
		return
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		s.set(StateError, "Could not start doubletake: "+err.Error())
		return
	}
	p := &process{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	s.proc = p
	s.set(StateDiscovering, msgSearching)

	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); readLines(stdout, func(l string) { s.onStdout(p, l) }) }()
	go func() { defer readers.Done(); readLines(stderr, func(l string) { s.onStderr(p, l) }) }()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		close(p.done)
		s.onExit(p, err)
	}()
}

// readLines calls onLine for each line. Prompts are printed without a trailing
// newline, so an unterminated remainder ending in ':' is delivered as well.
func readLines(r io.Reader, onLine func(string)) {
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			onLine(strings.TrimRight(string(pending[:i]), "\r"))
			pending = pending[i+1:]
		}
		if trimmed := strings.TrimRight(string(pending), " \t"); strings.HasSuffix(trimmed, ":") {
			onLine(string(pending))
			pending = nil
		}
		if err != nil {
			if len(pending) > 0 {
				onLine(string(pending))
			}
			return
		}
	}
}

func (s *Session) onStdout(p *process, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != s.proc {
		return
	}
	if m := deviceLine.FindStringSubmatch(line); m != nil {
		index, _ := strconv.Atoi(m[1])
		s.devices = append(s.devices, Device{
			Index: index,
			Name:  strings.TrimSpace(whitespaceRe.ReplaceAllString(m[2], " ")),
			Model: strings.TrimSpace(m[3]),
			IP:    m[4],
		})
		return
	}
	switch {
	case strings.Contains(line, "select device"):
		s.set(StateChoose, msgChoose)
	case credPrompt.MatchString(line):
		s.set(StatePin, strings.TrimSuffix(strings.TrimSpace(line), ":")+".")
	case strings.HasPrefix(line, "selected:"):
		s.set(StateConnecting, msgPickerHint)
	case noDevices.MatchString(line):
		s.message = msgNoDevices
	}
}

func (s *Session) onStderr(p *process, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != s.proc {
		return
	}
	text := strings.TrimSpace(goLogPrefix.ReplaceAllString(line, ""))
	switch {
	case text == "":
	case strings.Contains(text, "screen capture started"):
		s.set(StateStreaming, "Casting.")
	case noDevices.MatchString(text):
		s.message = msgNoDevices
	case s.state != StateStreaming:
		s.lastError = text
	}
}

func (s *Session) onExit(p *process, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != s.proc {
		return
	}
	s.proc = nil
	switch {
	case s.message == msgNoDevices:
		s.set(StateError, msgNoDevices)
	case s.state == StateStreaming:
		s.set(StateIdle, msgCastingEnded)
	case s.lastError != "":
		s.set(StateError, s.lastError)
	default:
		s.set(StateError, "doubletake exited: "+errString(err))
	}
}

func errString(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// Choose answers the device prompt with a listed index.
func (s *Session) Choose(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil || s.state != StateChoose {
		return false
	}
	for _, d := range s.devices {
		if d.Index == index {
			if _, err := io.WriteString(s.proc.stdin, strconv.Itoa(index)+"\n"); err != nil {
				return false
			}
			s.set(StateConnecting, "Connecting…")
			return true
		}
	}
	return false
}

// Pin answers a PIN/password prompt. The code only ever travels over stdin.
func (s *Session) Pin(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = strings.NewReplacer("\r", "", "\n", "").Replace(code)
	if s.proc == nil || s.state != StatePin || code == "" {
		return false
	}
	if _, err := io.WriteString(s.proc.stdin, code+"\n"); err != nil {
		return false
	}
	s.set(StateConnecting, msgPickerHint)
	return true
}

// Stop ends the running doubletake. A connected session gets SIGINT so it can
// tear down the receiver session (doubletake force-exits after 3 s itself);
// during discovery or at a prompt there is nothing to clean up, and doubletake
// would just sit out that timeout, so it is killed right away.
func (s *Session) Stop() {
	s.mu.Lock()
	p := s.proc
	graceful := s.state == StateConnecting || s.state == StateStreaming
	s.proc = nil
	s.devices = []Device{}
	s.set(StateIdle, "")
	s.mu.Unlock()
	if p == nil {
		return
	}
	if !graceful {
		_ = p.cmd.Process.Kill()
		<-p.done
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-p.done:
	case <-time.After(stopGrace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
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
