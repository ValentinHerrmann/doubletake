package main

import (
	"os"
	"strings"
	"testing"
)

func TestExecArg(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.local/bin/doubletake-ui": "/home/u/.local/bin/doubletake-ui",
		"/home/u/My Apps/doubletake-ui":    `"/home/u/My Apps/doubletake-ui"`,
		`/opt/a"b$c/doubletake-ui`:         `"/opt/a\\"b\\$c/doubletake-ui"`,
		"/opt/100%/doubletake-ui":          "/opt/100%%/doubletake-ui",
	} {
		if got := execArg(in); got != want {
			t.Errorf("execArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAutostartEntry(t *testing.T) {
	entry := autostartEntry("/usr/bin/doubletake-ui")
	if !strings.Contains(entry, "\nExec=/usr/bin/doubletake-ui -hidden\n") {
		t.Fatalf("entry lacks hidden Exec line:\n%s", entry)
	}
}

func TestSetAutostart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if autostartEnabled() {
		t.Fatal("enabled before any entry was written")
	}
	if err := setAutostart(true); err != nil {
		t.Fatal(err)
	}
	if !autostartEnabled() {
		t.Fatal("not enabled after setAutostart(true)")
	}
	path, _ := autostartPath()
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nHidden=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if autostartEnabled() {
		t.Fatal("enabled although the entry is Hidden=true")
	}
	if err := setAutostart(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("entry still present: %v", err)
	}
	if err := setAutostart(false); err != nil {
		t.Fatalf("removing a missing entry: %v", err)
	}
}
