package main

import (
	"os"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if loadSettings().KeepAwake {
		t.Fatal("keep awake is on by default")
	}
	if err := saveSettings(settings{KeepAwake: true}); err != nil {
		t.Fatal(err)
	}
	if !loadSettings().KeepAwake {
		t.Fatal("saved keep awake was not loaded")
	}

	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loadSettings().KeepAwake {
		t.Fatal("unreadable settings did not fall back to defaults")
	}
}
