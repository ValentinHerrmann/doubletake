package main

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"doubletake/internal/daemon"
	"doubletake/internal/daemon/daemonclient"
)

const pollMillis = 1000

// miracastSearchEvery spaces Miracast searches while the window is open: the
// daemon searches for 20 s, then Wi-Fi gets 10 s undisturbed. A search moves
// the radio off its channel, so none runs while hidden in the tray or casting.
const miracastSearchEvery = 30 * time.Second

// sources lists the capture-source toggles in display order.
var sources = []struct{ id, label string }{
	{"monitor", "Screen"},
	{"window", "Window"},
	{"any", "Ask"},
}

// window shows the daemon state and turns clicks into daemon requests. Its
// fields are touched only on the GTK main loop; daemon calls run in
// goroutines and hand their results back with glib.IdleAdd.
type window struct {
	*gtk.ApplicationWindow
	client *daemonclient.Client
	source string

	badge   *gtk.Label
	status  *gtk.Label
	toggles map[string]*gtk.ToggleButton
	list    *gtk.ListBox
	pinBox  *gtk.Box
	pin     *gtk.Entry
	mute    *gtk.ToggleButton
	stop    *gtk.Button

	app        *gtk.Application
	tray       *tray // nil until the tray icon is exported
	autostart  *gio.SimpleAction
	keepAwake  *gio.SimpleAction
	casting    map[string]*gio.SimpleAction // freeze, blackout and mute
	settings   settings
	inhibit    uint      // gtk inhibit cookie while keeping the screen awake
	inhibitErr bool      // the last inhibit request failed; retry after casting stops
	ready      bool      // the daemon is up
	nextSearch time.Time // earliest next Miracast search
	polling    atomic.Bool
	timer      glib.SourceHandle
	view       view
	lastState  string
	shownKey   string
	shown      []daemon.DeviceInfo
	actionErr  string
}

func newWindow(app *gtk.Application, opts options) *window {
	w := &window{
		ApplicationWindow: gtk.NewApplicationWindow(app),
		client:            daemonclient.NewDefault(),
		source:            opts.source,
		toggles:           map[string]*gtk.ToggleButton{},
		app:               app,
	}
	w.SetTitle("Doubletake")
	w.SetDefaultSize(360, 480)

	header := gtk.NewHeaderBar()
	w.badge = gtk.NewLabel("…")
	w.badge.AddCSSClass("dim-label")
	header.PackEnd(w.badge)
	w.addAutostartAction(app)
	w.addKeepAwakeAction(app)
	w.addCastingActions(app)
	menu := gio.NewMenu()
	menu.Append("Start on login", "app.autostart")
	menu.Append("Keep screen awake while casting", "app.keepawake")
	menuButton := gtk.NewMenuButton()
	menuButton.SetIconName("open-menu-symbolic")
	menuButton.SetMenuModel(menu)
	header.PackStart(menuButton)
	w.SetTitlebar(header)

	body := gtk.NewBox(gtk.OrientationVertical, 12)
	body.SetMarginTop(12)
	body.SetMarginBottom(12)
	body.SetMarginStart(12)
	body.SetMarginEnd(12)

	sourceBox := gtk.NewBox(gtk.OrientationHorizontal, 0)
	sourceBox.AddCSSClass("linked")
	sourceBox.SetHomogeneous(true)
	var group *gtk.ToggleButton
	for _, s := range sources {
		id := s.id
		btn := gtk.NewToggleButtonWithLabel(s.label)
		btn.SetGroup(group)
		group = btn
		btn.SetActive(id == w.source)
		btn.ConnectToggled(func() {
			if btn.Active() {
				w.source = id
			}
		})
		w.toggles[id] = btn
		sourceBox.Append(btn)
	}
	body.Append(sourceBox)

	w.status = gtk.NewLabel(msgStarting)
	w.status.SetWrap(true)
	w.status.SetXAlign(0)
	body.Append(w.status)

	w.list = gtk.NewListBox()
	w.list.SetSelectionMode(gtk.SelectionNone)
	w.list.AddCSSClass("frame")
	w.list.ConnectRowActivated(func(row *gtk.ListBoxRow) {
		if i := row.Index(); i >= 0 && i < len(w.shown) {
			dev, source := w.shown[i], w.source
			w.act(func() (*daemon.Response, error) {
				return w.client.ConnectWithSource(dev.IP, dev.Port, "", source)
			})
		}
	})
	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetVExpand(true)
	scroll.SetChild(w.list)
	body.Append(scroll)

	w.pinBox = gtk.NewBox(gtk.OrientationHorizontal, 6)
	w.pin = gtk.NewEntry()
	w.pin.SetHExpand(true)
	send := gtk.NewButtonWithLabel("Send")
	send.AddCSSClass("suggested-action")
	w.pin.ConnectActivate(w.sendCredential)
	send.ConnectClicked(w.sendCredential)
	w.pinBox.Append(w.pin)
	w.pinBox.Append(send)
	w.pinBox.SetVisible(false)
	body.Append(w.pinBox)

	controls := gtk.NewBox(gtk.OrientationHorizontal, 6)
	controls.SetHomogeneous(true)
	for _, c := range castingControls {
		btn := gtk.NewToggleButtonWithLabel(c.label)
		btn.SetTooltipText(c.tooltip)
		btn.SetActionName("app." + c.action)
		controls.Append(btn)
		if c.action == "mute" {
			w.mute = btn
		}
	}
	body.Append(controls)

	w.stop = gtk.NewButtonWithLabel("Stop casting")
	w.stop.SetSensitive(false)
	w.stop.ConnectClicked(func() { w.act(w.client.Disconnect) })
	body.Append(w.stop)

	w.SetChild(body)

	w.timer = glib.TimeoutAdd(pollMillis, func() bool {
		w.poll()
		return true
	})
	// With a tray host the window only hides: the app keeps polling so the
	// tray menu stays current. Without one, closing quits as before.
	w.ConnectCloseRequest(func() bool {
		if w.tray != nil && trayAvailable() {
			w.SetVisible(false)
			return true
		}
		glib.SourceRemove(w.timer)
		return false
	})

	go func() {
		err := ensureDaemon(w.client, findDoubletake(opts.doubletake), opts.noAudio)
		glib.IdleAdd(func() {
			if err != nil {
				w.render(buildView(nil, nil, err, ""))
				return
			}
			w.ready = true
			w.poll()
		})
	}()
	return w
}

// addAutostartAction registers app.autostart, a boolean shared by the window
// menu and the tray checkbox. Activating it toggles the login entry.
func (w *window) addAutostartAction(app *gtk.Application) {
	w.autostart = gio.NewSimpleActionStateful("autostart", nil, glibv2.NewVariantBoolean(autostartEnabled()))
	w.autostart.ConnectChangeState(func(value *glibv2.Variant) {
		on := value.Boolean()
		if err := setAutostart(on); err != nil {
			w.actionErr = "Start on login: " + err.Error()
			w.poll()
			on = !on
		}
		w.autostart.SetState(glibv2.NewVariantBoolean(on))
		if w.tray != nil {
			w.tray.setAutostart(on)
		}
	})
	app.AddAction(w.autostart)
}

// castingControls are the toggles that change a running cast. Each is a
// boolean app action shared by the window buttons and the tray menu.
var castingControls = []struct{ action, label, tooltip string }{
	{"freeze", "Freeze", "Keep showing the current picture on the receivers"},
	{"blackout", "Blackout", "Show a black picture on the receivers"},
	{"mute", "Mute", "Mute the cast's audio"},
}

// addCastingActions registers app.freeze, app.blackout and app.mute. They are
// enabled only while casting; render keeps their state in sync with the
// daemon, which applies them to every receiver.
func (w *window) addCastingActions(app *gtk.Application) {
	requests := map[string][2]func() (*daemon.Response, error){
		"freeze":   {w.client.Freeze, w.client.Unfreeze},
		"blackout": {w.client.Blackout, w.client.Unblackout},
		"mute":     {w.client.Mute, w.client.Unmute},
	}
	w.casting = map[string]*gio.SimpleAction{}
	for _, c := range castingControls {
		on, off := requests[c.action][0], requests[c.action][1]
		action := gio.NewSimpleActionStateful(c.action, nil, glibv2.NewVariantBoolean(false))
		action.SetEnabled(false)
		action.ConnectChangeState(func(value *glibv2.Variant) {
			// Show the change at once; the next poll reports the daemon's state.
			action.SetState(value)
			if value.Boolean() {
				w.act(on)
			} else {
				w.act(off)
			}
		})
		app.AddAction(action)
		w.casting[c.action] = action
	}
}

// renderCasting syncs the casting actions with the daemon state.
func (w *window) renderCasting(v view) {
	for name, on := range map[string]bool{"freeze": v.frozen, "blackout": v.black, "mute": v.muted} {
		action := w.casting[name]
		action.SetEnabled(v.casting)
		if action.State().Boolean() != on {
			action.SetState(glibv2.NewVariantBoolean(on))
		}
	}
	// A cast without audio has nothing to mute.
	w.mute.SetVisible(!v.casting || v.hasAudio)
}

// addKeepAwakeAction registers app.keepawake, a saved boolean shared by the
// window menu and the tray checkbox.
func (w *window) addKeepAwakeAction(app *gtk.Application) {
	w.settings = loadSettings()
	w.keepAwake = gio.NewSimpleActionStateful("keepawake", nil, glibv2.NewVariantBoolean(w.settings.KeepAwake))
	w.keepAwake.ConnectChangeState(func(value *glibv2.Variant) {
		next := w.settings
		next.KeepAwake = value.Boolean()
		if err := saveSettings(next); err != nil {
			w.actionErr = "Keep screen awake: " + err.Error()
			w.poll()
			return
		}
		w.settings = next
		w.keepAwake.SetState(value)
		if w.tray != nil {
			w.tray.setKeepAwake(next.KeepAwake)
		}
		w.updateInhibit()
	})
	app.AddAction(w.keepAwake)
}

// updateInhibit blocks the screensaver, screen lock and suspend while casting
// with keep awake on, and lifts the block otherwise. The block ends with this
// app: a cast that continues after Quit no longer keeps the screen awake.
func (w *window) updateInhibit() {
	want := w.settings.KeepAwake && w.view.casting
	switch {
	case want && w.inhibit == 0 && !w.inhibitErr:
		w.inhibit = w.app.Inhibit(nil, gtk.ApplicationInhibitIdle|gtk.ApplicationInhibitSuspend, "Casting the screen")
		w.inhibitErr = w.inhibit == 0
	case !want:
		if w.inhibit != 0 {
			w.app.Uninhibit(w.inhibit)
			w.inhibit = 0
		}
		w.inhibitErr = false
	}
}

// toggleVisible shows a hidden window and hides a shown one (tray click).
func (w *window) toggleVisible() {
	if w.IsVisible() {
		w.SetVisible(false)
	} else {
		w.Present()
	}
}

// setSource selects a capture source for the next connection.
func (w *window) setSource(source string) {
	if btn := w.toggles[source]; btn != nil {
		btn.SetActive(true)
	}
}

// searchMiracast starts a daemon Miracast search when one is due, the window
// is shown and nothing is casting. Displays appear in the list as the search
// finds them; AirPlay discovery runs all the time.
func (w *window) searchMiracast(v view) {
	if !w.ready || v.busy || !w.IsVisible() || time.Now().Before(w.nextSearch) {
		return
	}
	w.nextSearch = time.Now().Add(miracastSearchEvery)
	go func() { _, _ = w.client.Discover() }()
}

// poll fetches the daemon state in the background and renders it.
func (w *window) poll() {
	if !w.ready || !w.polling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		status, err := w.client.Status()
		var devices *daemon.Response
		if err == nil {
			devices, err = w.client.Devices()
		}
		glib.IdleAdd(func() {
			w.polling.Store(false)
			if err == nil && string(status.State) != w.lastState {
				w.lastState = string(status.State)
				w.actionErr = ""
			}
			w.render(buildView(status, devices, err, w.actionErr))
		})
	}()
}

// act sends one request in the background, keeps its error for display and
// refreshes the view.
func (w *window) act(request func() (*daemon.Response, error)) {
	go func() {
		resp, err := request()
		msg := ""
		if err != nil {
			msg = err.Error()
		} else if !resp.OK {
			msg = resp.Error
		}
		glib.IdleAdd(func() {
			w.actionErr = msg
			w.poll()
		})
	}()
}

func (w *window) sendCredential() {
	code := strings.TrimSpace(w.pin.Text())
	if code == "" || w.view.credential == "" {
		return
	}
	target := w.view.credTarget
	w.pin.SetText("")
	w.act(func() (*daemon.Response, error) {
		return w.client.ConnectWithSource(target, 0, code, "")
	})
}

func (w *window) render(v view) {
	prev := w.view
	w.view = v

	w.badge.SetText(v.badge)
	w.status.SetText(v.message)
	if v.badge == stateLabels[daemon.StateStreaming] {
		w.SetTitle("Doubletake – Casting")
	} else {
		w.SetTitle("Doubletake")
	}
	for _, btn := range w.toggles {
		btn.SetSensitive(!v.busy)
	}
	w.stop.SetSensitive(v.canStop)
	w.renderCasting(v)
	w.updateInhibit()
	w.searchMiracast(v)
	w.renderDevices(v)
	if w.tray != nil {
		w.tray.update(v)
	}

	w.pinBox.SetVisible(v.credential != "")
	if v.credential != "" && v.credential != prev.credential {
		password := v.credential == daemon.CredentialKindPassword
		w.pin.SetText("")
		w.pin.SetVisibility(!password)
		if password {
			w.pin.SetInputPurpose(gtk.InputPurposePassword)
			w.pin.SetMaxLength(0)
			w.pin.SetPlaceholderText("Password")
		} else {
			w.pin.SetInputPurpose(gtk.InputPurposeDigits)
			w.pin.SetMaxLength(4)
			w.pin.SetPlaceholderText("PIN")
		}
		w.pin.GrabFocus()
	}
}

// renderDevices rebuilds the receiver list only when it changed, so rows do
// not flicker or lose focus on every poll.
func (w *window) renderDevices(v view) {
	var key strings.Builder
	for _, d := range v.devices {
		key.WriteString(d.IP + "|" + d.Name + "|" + d.Model + "\n")
	}
	w.list.SetSensitive(v.canChoose)
	if key.String() == w.shownKey {
		return
	}
	w.shownKey = key.String()
	w.shown = v.devices
	w.list.RemoveAll()
	for _, d := range v.devices {
		row := gtk.NewBox(gtk.OrientationVertical, 2)
		row.SetMarginTop(6)
		row.SetMarginBottom(6)
		row.SetMarginStart(8)
		row.SetMarginEnd(8)
		name := gtk.NewLabel(d.Name)
		name.SetXAlign(0)
		meta := gtk.NewLabel(deviceDetail(d))
		meta.SetXAlign(0)
		meta.AddCSSClass("dim-label")
		meta.AddCSSClass("caption")
		row.Append(name)
		row.Append(meta)
		w.list.Append(row)
	}
}
