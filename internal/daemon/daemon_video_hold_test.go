package daemon

import (
	"strings"
	"testing"

	"doubletake/internal/airplay"
)

func TestVideoHoldRequiresStreaming(t *testing.T) {
	d := &Daemon{streams: map[string]*activeStream{
		"one": {deviceIP: "one", state: StateConnecting},
	}}
	resp := d.handleRequest(Request{Cmd: "freeze"})
	if resp.OK || !strings.Contains(resp.Error, "not currently streaming") {
		t.Fatalf("freeze while connecting = %+v, want not-streaming error", resp)
	}
	if d.videoFrozen {
		t.Fatal("refused freeze was recorded")
	}
}

func TestVideoHoldFlagsAndPrecedence(t *testing.T) {
	d := &Daemon{streams: map[string]*activeStream{
		"one": {deviceIP: "one", state: StateStreaming},
	}}
	steps := []struct {
		cmd           string
		frozen, black bool
		hold          airplay.VideoHold
	}{
		{"freeze", true, false, airplay.HoldFreeze},
		{"blackout", true, true, airplay.HoldBlack},
		{"unblackout", true, false, airplay.HoldFreeze},
		{"unfreeze", false, false, airplay.HoldNone},
	}
	for _, step := range steps {
		resp := d.handleRequest(Request{Cmd: step.cmd})
		if !resp.OK {
			t.Fatalf("%s: %s", step.cmd, resp.Error)
		}
		if resp.VideoFrozen != step.frozen || resp.VideoBlack != step.black {
			t.Fatalf("%s: status frozen=%v black=%v, want %v %v", step.cmd, resp.VideoFrozen, resp.VideoBlack, step.frozen, step.black)
		}
		if got := d.videoHoldLocked(); got != step.hold {
			t.Fatalf("%s: hold = %s, want %s", step.cmd, got, step.hold)
		}
	}
}

func TestVideoHoldFailureRollsBack(t *testing.T) {
	// A byte-stream capture cannot hold video.
	key := videoCaptureKey{maxWidth: 1280, maxHeight: 720}
	group := &videoCaptureGroup{key: key, broadcast: airplay.NewBroadcastCapture(&airplay.ScreenCapture{})}
	d := &Daemon{
		streams: map[string]*activeStream{
			"one": {deviceIP: "one", state: StateStreaming, captureGroup: group},
		},
		captureGroups: map[videoCaptureKey]*videoCaptureGroup{key: group},
	}
	resp := d.handleRequest(Request{Cmd: "blackout"})
	if resp.OK || resp.VideoBlack || d.videoBlack {
		t.Fatalf("failed blackout = %+v (recorded %v), want error and no blackout", resp, d.videoBlack)
	}
}

func TestVideoHoldClearsWhenCastingEnds(t *testing.T) {
	key := videoCaptureKey{maxWidth: 1280, maxHeight: 720}
	group := &videoCaptureGroup{key: key}
	d := &Daemon{
		streams: map[string]*activeStream{
			"one": {deviceIP: "one", state: StateStreaming, captureGroup: group},
		},
		captureGroups: map[videoCaptureKey]*videoCaptureGroup{key: group},
		videoFrozen:   true,
		videoBlack:    true,
	}
	cleanup := d.detachStreamLocked("one")
	cleanup.run()
	if d.videoFrozen || d.videoBlack {
		t.Fatal("freeze or blackout survived the end of casting")
	}
}
