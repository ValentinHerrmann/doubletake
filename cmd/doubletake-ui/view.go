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
	msgPickerHint = "Connecting to %s… choose what to share when the screen-sharing dialog appears."
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
	stopLabel  string                // "Cancel" while connecting, else "Stop casting"
	credential daemon.CredentialKind // non-empty while a receiver waits for a PIN/password
	credTarget string                // receiver IP the credential is for
	casting    bool                  // at least one receiver shows the stream
	castingTo  string                // names of the receivers showing the stream
	hasAudio   bool                  // the stream carries audio that can be muted
	muted      bool
	frozen     bool
	black      bool
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
		v.message = fmt.Sprintf(msgPickerHint, receiversIn(status, daemon.StateConnecting, v.devices))
	case daemon.StateStreaming:
		v.busy, v.canStop, v.casting = true, true, true
		v.hasAudio, v.muted = status.HasAudio, status.AudioMuted
		v.frozen, v.black = status.VideoFrozen, status.VideoBlack
		v.castingTo = receiversIn(status, daemon.StateStreaming, v.devices)
		v.message = fmt.Sprintf("Casting to %s.", v.castingTo)
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
	v.stopLabel = "Stop casting"
	if v.busy && !v.casting {
		v.stopLabel = "Cancel"
	}
	if actionErr != "" {
		v.message = actionErr
	}
	return v
}

// receiversIn names every receiver whose stream is in state. A stream that
// has not reported its name yet (AirPlay while connecting) is named from the
// discovered devices. Older daemons report only the legacy single-stream
// fields.
func receiversIn(status *daemon.Response, state daemon.State, devices []daemon.DeviceInfo) string {
	var names []string
	for _, s := range status.Streams {
		if s.State != state {
			continue
		}
		name := s.Device
		for _, d := range devices {
			if name == "" && d.IP == s.DeviceIP {
				name = d.Name
			}
		}
		names = append(names, firstNonEmpty(name, s.DeviceIP))
	}
	if len(names) == 0 {
		return firstNonEmpty(status.Device, status.DeviceIP, "the receiver")
	}
	return strings.Join(names, ", ")
}

// deviceDetail is the secondary line of a receiver row: its protocol, then
// the model and address for AirPlay. A Miracast target is not an address.
func deviceDetail(d daemon.DeviceInfo) string {
	if d.Protocol == "miracast" {
		return "Miracast"
	}
	parts := []string{"AirPlay"}
	if d.Model != "" {
		parts = append(parts, d.Model)
	}
	if d.IP != "" {
		parts = append(parts, d.IP)
	}
	return strings.Join(parts, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
