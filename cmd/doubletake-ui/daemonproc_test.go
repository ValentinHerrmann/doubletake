package main

import (
	"strings"
	"testing"
)

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
