package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// autostartPath is the XDG autostart entry, honoured by GNOME and KDE alike.
func autostartPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "doubletake-ui.desktop"), nil
}

// autostartEnabled reports whether an autostart entry exists and is not
// switched off by the desktop's own startup-apps settings.
func autostartEnabled() bool {
	path, err := autostartPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case "Hidden=true", "X-GNOME-Autostart-enabled=false":
			return false
		}
	}
	return true
}

// setAutostart writes or removes the autostart entry. The entry starts this
// binary in the tray.
func setAutostart(on bool) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(autostartEntry(exe)), 0o644)
}

func autostartEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Doubletake\n" +
		"Comment=Start Doubletake in the system tray\n" +
		"Exec=" + execArg(exe) + " -hidden\n" +
		"Icon=doubletake\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// execArg quotes one Exec argument per the Desktop Entry spec: reserved
// characters force double quotes, inside which " ` $ \ take a backslash. The
// value is then a desktop-entry string, where a backslash is written \\.
func execArg(arg string) string {
	arg = strings.ReplaceAll(arg, "%", "%%")
	if !strings.ContainsAny(arg, " \t\n\"'\\><~|&;$*?#()`") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		if strings.ContainsRune("\"`$\\", r) {
			b.WriteString(`\\`)
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
