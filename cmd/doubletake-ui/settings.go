package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// settings are the app's own preferences. Start on login lives in the XDG
// autostart entry instead, where the desktop's startup settings can see it.
type settings struct {
	// KeepAwake blocks the screensaver, screen lock and suspend while casting.
	KeepAwake bool `json:"keep_awake"`
}

func settingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "doubletake", "ui.json"), nil
}

// loadSettings returns the saved settings, or the defaults when none are
// saved or the file cannot be read.
func loadSettings() settings {
	var s settings
	path, err := settingsPath()
	if err != nil {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return settings{}
	}
	return s
}

func saveSettings(s settings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
