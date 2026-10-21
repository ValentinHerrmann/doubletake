package main

import (
	"strings"
	"sync/atomic"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"doubletake/internal/daemon"
	"doubletake/internal/daemon/daemonclient"
)

const pollMillis = 1000

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
	stop    *gtk.Button

	ready     bool // the daemon is up
	polling   atomic.Bool
	timer     glib.SourceHandle
	view      view
	lastState string
	shownKey  string
	shown     []daemon.DeviceInfo
	actionErr string
}

func newWindow(app *gtk.Application, opts options) *window {
	w := &window{
		ApplicationWindow: gtk.NewApplicationWindow(app),
		client:            daemonclient.NewDefault(),
		source:            opts.source,
		toggles:           map[string]*gtk.ToggleButton{},
	}
	w.SetTitle("Doubletake")
	w.SetDefaultSize(360, 480)

	header := gtk.NewHeaderBar()
	w.badge = gtk.NewLabel("…")
	w.badge.AddCSSClass("dim-label")
	header.PackEnd(w.badge)
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

	w.stop = gtk.NewButtonWithLabel("Stop casting")
	w.stop.SetSensitive(false)
	w.stop.ConnectClicked(func() { w.act(w.client.Disconnect) })
	body.Append(w.stop)

	w.SetChild(body)

	w.timer = glib.TimeoutAdd(pollMillis, func() bool {
		w.poll()
		return true
	})
	w.ConnectCloseRequest(func() bool {
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

// setSource selects a capture source for the next connection.
func (w *window) setSource(source string) {
	if btn := w.toggles[source]; btn != nil {
		btn.SetActive(true)
	}
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
	w.renderDevices(v)

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
		meta := gtk.NewLabel(firstNonEmpty(d.Model+" · "+d.IP, d.IP))
		meta.SetXAlign(0)
		meta.AddCSSClass("dim-label")
		meta.AddCSSClass("caption")
		row.Append(name)
		row.Append(meta)
		w.list.Append(row)
	}
}
