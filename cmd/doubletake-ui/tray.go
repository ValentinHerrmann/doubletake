package main

import (
	_ "embed"
	"time"

	"fyne.io/systray"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/godbus/dbus/v5"
)

// trayIcon is the app icon as a pixmap, which StatusNotifierItem hosts take:
//
//	rsvg-convert -w 64 -h 64 share/icons/hicolor/scalable/apps/doubletake.svg -o cmd/doubletake-ui/tray.png
//
//go:embed tray.png
var trayIcon []byte

// trayWait bounds how long a -hidden start waits for a tray host. At login the
// host (GNOME's AppIndicator extension, plasmashell) may come up after us.
const trayWait = 5 * time.Second

// trayAvailable reports whether a StatusNotifierItem host is running. Plain
// GNOME has none; it needs the AppIndicator/KStatusNotifierItem extension.
func trayAvailable() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func waitForTray(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !trayAvailable() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return true
}

// tray holds the menu items the window updates. Its methods run on the GTK
// main loop; systray locks internally.
type tray struct {
	stop, autostart *systray.MenuItem
	tooltip         string
}

// update mirrors the window state. It runs on every poll, so it only touches
// systray (which signals the tray host over D-Bus) when something changed.
func (t *tray) update(v view) {
	if v.canStop == t.stop.Disabled() {
		if v.canStop {
			t.stop.Enable()
		} else {
			t.stop.Disable()
		}
	}
	if tip := "Doubletake – " + v.message; tip != t.tooltip {
		t.tooltip = tip
		systray.SetTooltip(tip)
	}
}

func (t *tray) setAutostart(on bool) {
	if on {
		t.autostart.Check()
	} else {
		t.autostart.Uncheck()
	}
}

// startTray exports the tray icon on the session bus. systray registers with
// the tray host now or whenever one appears later. Menu clicks arrive on
// systray goroutines and hop to the GTK main loop.
func startTray(app *gtk.Application, w *window) {
	if _, err := dbus.SessionBus(); err != nil {
		return
	}
	systray.SetOnTapped(func() { glib.IdleAdd(w.toggleVisible) })
	// The end function is not needed: the bus connection closes with the
	// process, and systray's end dereferences a connection it may not have.
	start, _ := systray.RunWithExternalLoop(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Doubletake")
		show := systray.AddMenuItem("Show Doubletake", "")
		stop := systray.AddMenuItem("Stop casting", "")
		systray.AddSeparator()
		auto := systray.AddMenuItemCheckbox("Start on login", "", autostartEnabled())
		quit := systray.AddMenuItem("Quit", "")
		t := &tray{stop: stop, autostart: auto}
		glib.IdleAdd(func() {
			w.tray = t
			t.update(w.view)
		})
		// systray serves the menu only after this callback returns.
		go func() {
			for {
				select {
				case <-show.ClickedCh:
					glib.IdleAdd(w.Present)
				case <-stop.ClickedCh:
					glib.IdleAdd(func() { w.act(w.client.Disconnect) })
				case <-auto.ClickedCh:
					glib.IdleAdd(func() { app.ActivateAction("autostart", nil) })
				case <-quit.ClickedCh:
					glib.IdleAdd(app.Quit)
				}
			}
		}()
	}, nil)
	start()
}
