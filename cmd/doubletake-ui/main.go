// Command doubletake-ui is a small GTK4 desktop UI for doubletake: it lists
// the AirPlay receivers on the network, answers PIN prompts and starts/stops
// screen mirroring. It drives the doubletake daemon over its control socket
// and starts the daemon when none is running, so casting continues after the
// window is closed.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const appID = "org.doubletake.UI"

type options struct {
	doubletake string
	source     string
	sourceSet  bool
	noAudio    bool
	hidden     bool
}

// parseOptions parses a command line. It runs in the primary instance for
// its own launch and for every later launch, which GApplication forwards.
func parseOptions(args []string) (options, string, error) {
	var opts options
	var out strings.Builder
	fs := flag.NewFlagSet("doubletake-ui", flag.ContinueOnError)
	fs.SetOutput(&out)
	fs.StringVar(&opts.doubletake, "doubletake", "", "Path to the doubletake binary used to start the daemon (default: next to this binary, then doubletake-src or doubletake on PATH)")
	fs.StringVar(&opts.source, "source", "any", "What the screen-sharing dialog offers: any (screens and windows), monitor or window")
	fs.BoolVar(&opts.noAudio, "no-audio", false, "Do not stream audio (applies when this UI starts the daemon)")
	fs.BoolVar(&opts.hidden, "hidden", false, "Start in the system tray without showing the window (shows it if no tray appears)")
	if err := fs.Parse(args); err != nil {
		return opts, out.String(), err
	}
	if !slices.Contains([]string{"any", "monitor", "window"}, opts.source) {
		return opts, "", fmt.Errorf("unknown source %q (want any, monitor or window)", opts.source)
	}
	fs.Visit(func(f *flag.Flag) { opts.sourceSet = opts.sourceSet || f.Name == "source" })
	return opts, out.String(), nil
}

func main() {
	// gotk4 routes GLib logging through slog; keep GLib's default of hiding
	// info and debug messages.
	slog.SetLogLoggerLevel(slog.LevelWarn)
	app := gtk.NewApplication(appID, gio.ApplicationHandlesCommandLine)
	var win *window
	app.ConnectCommandLine(func(cmdline *gio.ApplicationCommandLine) int {
		opts, usage, err := parseOptions(cmdline.Arguments()[1:])
		if err != nil {
			cmdline.PrinterrLiteral(usage + fmt.Sprintln(err))
			return 2
		}
		if win == nil {
			win = newWindow(app, opts)
			startTray(app, win)
			if opts.hidden {
				w := win
				go func() {
					if !waitForTray(trayWait) {
						glib.IdleAdd(w.Present)
					}
				}()
				return 0
			}
		} else if opts.sourceSet {
			win.source = opts.source
		}
		win.Present()
		return 0
	})
	os.Exit(app.Run(os.Args))
}
