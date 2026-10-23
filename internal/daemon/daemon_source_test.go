package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"doubletake/internal/airplay"
)

func TestHandleConnectRejectsUnknownSource(t *testing.T) {
	d, err := New(Config{CredBackend: "file", CredFile: filepath.Join(t.TempDir(), "credentials.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)

	resp := d.handleConnect(Request{Cmd: "connect", Target: "192.0.2.1", Source: "bogus"})
	if resp.OK || !strings.Contains(resp.Error, "unknown source") {
		t.Fatalf("response = %+v, want unknown source error", resp)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.streams) != 0 {
		t.Fatalf("rejected connect left streams: %+v", d.streams)
	}
}

func TestRememberedScreenGatesRestoreToken(t *testing.T) {
	saved := &airplay.SavedCredentials{RestoreToken: "screen"}
	for _, test := range []struct {
		sourceTypes uint32
		want        string
	}{
		{0, "screen"},
		{airplay.SourceMonitor, "screen"},
		{airplay.SourceMonitor | airplay.SourceWindow, ""},
		{airplay.SourceWindow, ""},
	} {
		entry := &activeStream{sourceTypes: test.sourceTypes}
		if got := entry.rememberedScreen(saved); got != test.want {
			t.Errorf("rememberedScreen(types %d) = %q, want %q", test.sourceTypes, got, test.want)
		}
	}
	if got := (&activeStream{}).rememberedScreen(nil); got != "" {
		t.Errorf("rememberedScreen(nil) = %q", got)
	}
}

func TestCaptureGroupsAreSeparatedBySource(t *testing.T) {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 is required for the headless capture integration test")
	}
	if err := exec.Command("gst-inspect-1.0", "openh264enc").Run(); err != nil {
		t.Skip("GStreamer openh264enc is required for the headless capture integration test")
	}

	d, err := New(Config{
		CredBackend: "file",
		CredFile:    filepath.Join(t.TempDir(), "credentials.json"),
		FPS:         10,
		Bitrate:     500,
		HWAccel:     "openh264",
		TestMode:    true,
		NoAudio:     true,
	})
	if err != nil {
		t.Fatalf("create daemon: %v", err)
	}
	t.Cleanup(d.Shutdown)

	targets := map[string]uint32{
		"192.0.2.10": airplay.SourceMonitor,
		"192.0.2.11": airplay.SourceWindow,
	}
	for target, sourceTypes := range targets {
		_, cancel := context.WithCancel(context.Background())
		entry := &activeStream{deviceIP: target, state: StateConnecting, cancelFn: cancel, sourceTypes: sourceTypes}
		d.mu.Lock()
		d.streams[target] = entry
		d.mu.Unlock()
		if _, err := d.getOrStartCaptureGroup(entry, "", "", 640, 480); err != nil {
			t.Fatalf("start capture for %s: %v", target, err)
		}
	}

	d.mu.Lock()
	groups := len(d.captureGroups)
	d.mu.Unlock()
	if groups != len(targets) {
		t.Fatalf("capture groups = %d, want one per source (%d)", groups, len(targets))
	}

	for target := range targets {
		d.mu.Lock()
		cleanup := d.detachStreamLocked(target)
		d.mu.Unlock()
		cleanup.run()
	}
}
