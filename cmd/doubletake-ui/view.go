package main

import (
	"fmt"
	"sort"
	"strings"

	"doubletake/internal/daemon"
)

const (
	msgStarting   = "Starting doubletake…"
	msgSearching  = "Searching for receivers…"
	msgChoose     = "Choose a receiver."
	msgPickerHint = "Connecting… choose what to share when the screen-sharing dialog appears."
)

var stateLabels = map[daemon.State]string{
	daemon.StateIdle:        "Idle",
	daemon.StateDiscovering: "Searching",
	daemon.StateConnecting:  "Connecting",
	daemon.StateStreaming:   "Casting",
	daemon.StatePINRequired: "PIN",
}

// view is everything the window shows, derived from one daemon poll.
type view struct {
	badge      string
	message    string
	devices    []daemon.DeviceInfo
	canChoose  bool // a receiver can be selected
	busy       bool // a connection is being set up or running
	canStop    bool
	credential daemon.CredentialKind // non-empty while a receiver waits for a PIN/password
	credTarget string                // receiver IP the credential is for
}

// buildView maps a daemon status and device list to the window state.
// actionErr is the error of the user's last request, shown until the next one.
func buildView(status, devices *daemon.Response, pollErr error, actionErr string) view {
	if pollErr != nil {
		return view{badge: "Offline", message: "Cannot reach doubletake: " + pollErr.Error()}
	}
	v := view{badge: stateLabels[status.State]}
	if v.badge == "" {
		v.badge = string(status.State)
	}
	if devices != nil {
		v.devices = append(v.devices, devices.Devices...)
		sort.Slice(v.devices, func(i, j int) bool {
			return strings.ToLower(v.devices[i].Name) < strings.ToLower(v.devices[j].Name)
		})
	}

	switch status.State {
	case daemon.StatePINRequired:
		v.busy, v.canStop = true, true
		v.credential = status.CredentialKind
		v.credTarget = status.DeviceIP
		name := firstNonEmpty(status.Device, status.DeviceIP, "the receiver")
		if v.credential == daemon.CredentialKindPassword {
			v.message = fmt.Sprintf("Enter the password for %s.", name)
		} else {
			v.credential = daemon.CredentialKindPIN
			v.message = fmt.Sprintf("Enter the PIN shown on %s.", name)
		}
	case daemon.StateConnecting:
		v.busy, v.canStop = true, true
		v.message = msgPickerHint
	case daemon.StateStreaming:
		v.busy, v.canStop = true, true
		v.message = fmt.Sprintf("Casting to %s.", firstNonEmpty(status.Device, status.DeviceIP, "the receiver"))
	default:
		v.canChoose = len(v.devices) > 0
		switch {
		case status.Error != "":
			v.message = status.Error
		case len(v.devices) == 0:
			v.message = msgSearching
		default:
			v.message = msgChoose
		}
	}
	if actionErr != "" {
		v.message = actionErr
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
