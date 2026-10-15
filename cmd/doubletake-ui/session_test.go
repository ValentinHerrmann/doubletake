package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// interactiveScript mimics doubletake's interactive flow: device list and a
// prompt without newline, then a PIN prompt, then the capture log line.
const interactiveScript = `
echo "$$" >> "$DIR/pids"
echo "args: $*" > "$DIR/args"
echo "searching for Apple TVs..."
echo ""
echo "available devices:"
echo "  [1] Room 101 Projector (AppleTV3,2) - 10.0.0.5"
printf '  [2] Aula\t(Epson) (EB-L) - 10.0.0.6\n'
printf "\nselect device [1]: "
read choice
echo "$choice" > "$DIR/choice"
echo "selected: Aula (10.0.0.6:7000)"
printf "Enter the PIN shown on the receiver: "
read pin
echo "$pin" > "$DIR/pin"
echo "2026/10/15 12:00:00 screen capture started at 1920x1080 using h264" >&2
exec sleep 30
`

func fakeDoubletake(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "doubletake")
	body := "#!/bin/sh\nDIR=" + strconv.Quote(dir) + "\n" + script
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func waitFor(t *testing.T, s *Session, want State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.Status().State != want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s, got %+v", want, s.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestSessionFlow(t *testing.T) {
	bin, dir := fakeDoubletake(t, interactiveScript)
	s := NewSession(bin, "window", true)
	t.Cleanup(s.Stop)

	s.Start()
	waitFor(t, s, StateChoose)
	want := []Device{
		{Index: 1, Name: "Room 101 Projector", Model: "AppleTV3,2", IP: "10.0.0.5"},
		{Index: 2, Name: "Aula (Epson)", Model: "EB-L", IP: "10.0.0.6"},
	}
	if got := s.Status().Devices; !reflect.DeepEqual(got, want) {
		t.Fatalf("devices = %+v, want %+v", got, want)
	}
	if got := readTrim(t, filepath.Join(dir, "args")); got != "args: -source window -no-audio" {
		t.Fatalf("args = %q", got)
	}

	if s.Choose(3) {
		t.Fatal("Choose accepted an unlisted index")
	}
	if !s.Choose(2) {
		t.Fatal("Choose(2) failed")
	}
	waitFor(t, s, StatePin)
	if !s.Pin("1234") {
		t.Fatal("Pin failed")
	}
	waitFor(t, s, StateStreaming)
	if got := readTrim(t, filepath.Join(dir, "choice")); got != "2" {
		t.Fatalf("choice = %q", got)
	}
	if got := readTrim(t, filepath.Join(dir, "pin")); got != "1234" {
		t.Fatalf("pin = %q", got)
	}

	s.Stop()
	if st := s.Status().State; st != StateIdle {
		t.Fatalf("state after Stop = %s", st)
	}
}

func TestSessionRefreshRestarts(t *testing.T) {
	bin, dir := fakeDoubletake(t, interactiveScript)
	s := NewSession(bin, "any", false)
	t.Cleanup(s.Stop)

	s.Start()
	waitFor(t, s, StateChoose)
	s.Start()
	waitFor(t, s, StateChoose)
	pids := strings.Fields(readTrim(t, filepath.Join(dir, "pids")))
	if len(pids) != 2 {
		t.Fatalf("pids = %v, want 2 runs", pids)
	}
	first, _ := strconv.Atoi(pids[0])
	if err := syscall.Kill(first, 0); err == nil {
		t.Fatalf("first doubletake (%d) still running", first)
	}
}

func TestSessionNoDevices(t *testing.T) {
	bin, _ := fakeDoubletake(t, `
echo "searching for Apple TVs..."
echo "2026/10/15 12:00:00 discovery failed: no Apple TVs found" >&2
exit 1
`)
	s := NewSession(bin, "any", false)
	s.Start()
	waitFor(t, s, StateError)
	if msg := s.Status().Message; msg != msgNoDevices {
		t.Fatalf("message = %q", msg)
	}
}

func TestSessionReportsLastLogLine(t *testing.T) {
	bin, _ := fakeDoubletake(t, `
echo "2026/10/15 12:00:00 connect failed: connection refused" >&2
exit 1
`)
	s := NewSession(bin, "any", false)
	s.Start()
	waitFor(t, s, StateError)
	if msg := s.Status().Message; msg != "connect failed: connection refused" {
		t.Fatalf("message = %q", msg)
	}
}

func TestHasAmbientCaps(t *testing.T) {
	if !hasAmbientCaps("Name:\tnode\nCapAmb:\t0000000000000400\n") {
		t.Fatal("expected ambient caps")
	}
	if hasAmbientCaps("CapAmb:\t0000000000000000\n") {
		t.Fatal("expected no ambient caps")
	}
}

func TestSessionEnvFillsDefaults(t *testing.T) {
	env := sessionEnv([]string{"HOME=/home/x", "XDG_RUNTIME_DIR=/nonexistent"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"HOME=/home/x", "DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/bus", "DISPLAY=:0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
	kept := sessionEnv([]string{"XDG_RUNTIME_DIR=/r", "WAYLAND_DISPLAY=wayland-1"})
	if strings.Contains(strings.Join(kept, "\n"), "DISPLAY=:0") {
		t.Fatalf("DISPLAY set despite WAYLAND_DISPLAY: %v", kept)
	}
}
