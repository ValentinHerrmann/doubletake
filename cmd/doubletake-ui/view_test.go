package main

import (
	"errors"
	"strings"
	"testing"

	"doubletake/internal/daemon"
)

func TestBuildViewIdle(t *testing.T) {
	devices := &daemon.Response{Devices: []daemon.DeviceInfo{
		{Name: "living room", IP: "192.0.2.2"},
		{Name: "Bedroom", IP: "192.0.2.1"},
	}}
	v := buildView(&daemon.Response{OK: true, State: daemon.StateIdle}, devices, nil, "")
	if !v.canChoose || v.busy || v.canStop {
		t.Fatalf("idle view flags = %+v", v)
	}
	if v.devices[0].Name != "Bedroom" || v.message != msgChoose {
		t.Fatalf("idle view = %+v", v)
	}

	empty := buildView(&daemon.Response{OK: true, State: daemon.StateIdle}, &daemon.Response{}, nil, "")
	if empty.canChoose || empty.message != msgSearching {
		t.Fatalf("empty view = %+v", empty)
	}

	failed := buildView(&daemon.Response{OK: true, State: daemon.StateIdle, Error: "192.0.2.1: boom"}, devices, nil, "")
	if failed.message != "192.0.2.1: boom" || !failed.canChoose {
		t.Fatalf("failed view = %+v", failed)
	}
}

func TestBuildViewCredential(t *testing.T) {
	pin := buildView(&daemon.Response{State: daemon.StatePINRequired, Device: "TV", DeviceIP: "192.0.2.1"}, nil, nil, "")
	if pin.credential != daemon.CredentialKindPIN || pin.credTarget != "192.0.2.1" || !strings.Contains(pin.message, "PIN shown on TV") {
		t.Fatalf("pin view = %+v", pin)
	}
	if !pin.busy || pin.canChoose {
		t.Fatalf("pin view flags = %+v", pin)
	}

	password := buildView(&daemon.Response{State: daemon.StatePINRequired, DeviceIP: "192.0.2.1", CredentialKind: daemon.CredentialKindPassword}, nil, nil, "")
	if password.credential != daemon.CredentialKindPassword || !strings.Contains(password.message, "password for 192.0.2.1") {
		t.Fatalf("password view = %+v", password)
	}
}

func TestBuildViewNamesEveryReceiver(t *testing.T) {
	status := &daemon.Response{State: daemon.StateStreaming, Device: "TV", Streams: []daemon.StreamInfo{
		{Device: "TV", DeviceIP: "192.0.2.1", State: daemon.StateStreaming},
		{Device: "Projector", DeviceIP: "p2p:AA", State: daemon.StateStreaming},
		{Device: "Kitchen", DeviceIP: "192.0.2.3", State: daemon.StateConnecting},
	}}
	v := buildView(status, nil, nil, "")
	if v.castingTo != "TV, Projector" || v.message != "Casting to TV, Projector." {
		t.Fatalf("multi-receiver view = %+v", v)
	}
}

func TestBuildViewStreamingAndErrors(t *testing.T) {
	streaming := buildView(&daemon.Response{State: daemon.StateStreaming, Device: "TV"}, nil, nil, "")
	if streaming.badge != "Casting" || streaming.message != "Casting to TV." || !streaming.canStop || streaming.canChoose {
		t.Fatalf("streaming view = %+v", streaming)
	}

	offline := buildView(nil, nil, errors.New("connect to daemon: refused"), "")
	if offline.badge != "Offline" || offline.canChoose || offline.canStop {
		t.Fatalf("offline view = %+v", offline)
	}

	action := buildView(&daemon.Response{State: daemon.StateIdle}, nil, nil, "already connected")
	if action.message != "already connected" {
		t.Fatalf("action error view = %+v", action)
	}
}

func TestBuildViewCastingControls(t *testing.T) {
	status := &daemon.Response{
		State:       daemon.StateStreaming,
		HasAudio:    true,
		AudioMuted:  true,
		VideoFrozen: true,
		VideoBlack:  true,
	}
	v := buildView(status, nil, nil, "")
	if !v.casting || !v.hasAudio || !v.muted || !v.frozen || !v.black {
		t.Fatalf("casting view = %+v", v)
	}

	// Stale flags of a stream that is still connecting control nothing.
	status.State = daemon.StateConnecting
	connecting := buildView(status, nil, nil, "")
	if connecting.casting || connecting.muted || connecting.frozen || connecting.black {
		t.Fatalf("connecting view = %+v", connecting)
	}
}

func TestDeviceDetail(t *testing.T) {
	for _, test := range []struct {
		device daemon.DeviceInfo
		want   string
	}{
		{daemon.DeviceInfo{Model: "AppleTV14,1", IP: "192.168.1.7"}, "AirPlay · AppleTV14,1 · 192.168.1.7"},
		{daemon.DeviceInfo{IP: "192.168.1.8", Protocol: "airplay"}, "AirPlay · 192.168.1.8"},
		{daemon.DeviceInfo{Model: "Miracast", IP: "p2p:DA:C0:BF:B4:E3:48", Protocol: "miracast"}, "Miracast"},
	} {
		if got := deviceDetail(test.device); got != test.want {
			t.Errorf("deviceDetail(%+v) = %q, want %q", test.device, got, test.want)
		}
	}
}
