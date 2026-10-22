package daemon

import (
	"context"
	"io"
	"log"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"doubletake/internal/airplay"
)

// TestDaemonVideoHoldOverAirPlay casts the synthetic source to the in-process
// receiver, then checks that blackout and freeze send only repeated
// keyframes, and that live video returns after each.
func TestDaemonVideoHoldOverAirPlay(t *testing.T) {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 is required for the headless capture integration test")
	}
	for _, element := range []string{"openh264enc", "avdec_h264", "rawvideoparse"} {
		if err := exec.Command("gst-inspect-1.0", element).Run(); err != nil {
			t.Skipf("GStreamer %s is required for the video hold integration test", element)
		}
	}

	receiverCtx, cancelReceiver := context.WithCancel(context.Background())
	server, err := airplay.NewReceiverServer(airplay.ReceiverConfig{
		ListenAddress: "127.0.0.1:0",
		Profile:       airplay.ReceiverProfileRoku,
		Auth:          airplay.ReceiverAuthNone,
		Name:          "Hold TV",
		Logger:        log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("start receiver: %v", err)
	}
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- server.Serve(receiverCtx) }()
	t.Cleanup(func() {
		cancelReceiver()
		_ = server.Close()
		<-receiverDone
	})

	socketPath := filepath.Join(t.TempDir(), "doubletake.sock")
	d, err := New(Config{
		SocketPath:  socketPath,
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
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- d.Run(daemonCtx) }()
	t.Cleanup(func() {
		cancelDaemon()
		d.Shutdown()
		<-daemonDone
	})
	waitForDaemonSocket(t, socketPath)

	addr := server.Addr().(*net.TCPAddr)
	if resp := sendDaemonSocketRequest(t, socketPath, Request{Cmd: "connect", Target: addr.IP.String(), Port: addr.Port}); !resp.OK {
		t.Fatalf("connect: %+v", resp)
	}
	waitForDaemonSocketStatus(t, socketPath, 15*time.Second, func(resp Response) bool {
		return resp.State == StateStreaming
	})
	expectLiveVideo(t, server)

	for _, step := range []struct{ on, off string }{
		{"blackout", "unblackout"},
		{"freeze", "unfreeze"},
	} {
		resp := sendDaemonSocketRequest(t, socketPath, Request{Cmd: step.on})
		if !resp.OK || (step.on == "blackout") != resp.VideoBlack || (step.on == "freeze") != resp.VideoFrozen {
			t.Fatalf("%s: %+v", step.on, resp)
		}
		// Let live frames queued before the switch drain, then count.
		time.Sleep(time.Second)
		before := server.Stats()
		time.Sleep(3 * time.Second)
		after := server.Stats()
		frames := after.VideoFrames - before.VideoFrames
		keyframes := after.VideoKeyFrames - before.VideoKeyFrames
		if frames < 2 || frames != keyframes {
			t.Fatalf("%s: receiver got %d frames, %d of them keyframes; want only repeated stills", step.on, frames, keyframes)
		}

		if resp := sendDaemonSocketRequest(t, socketPath, Request{Cmd: step.off}); !resp.OK || resp.VideoBlack || resp.VideoFrozen {
			t.Fatalf("%s: %+v", step.off, resp)
		}
		expectLiveVideo(t, server)
	}

	if stats := server.Stats(); stats.VideoMalformed != 0 || stats.VideoCryptoErrors != 0 {
		t.Fatalf("receiver saw malformed video: %+v", stats)
	}
}

// expectLiveVideo waits for a burst of non-key frames, which only the live
// encoder produces.
func expectLiveVideo(t *testing.T, server *airplay.ReceiverServer) {
	t.Helper()
	start := server.Stats()
	deadline := time.Now().Add(5 * time.Second)
	for {
		now := server.Stats()
		if now.VideoFrames-now.VideoKeyFrames >= start.VideoFrames-start.VideoKeyFrames+5 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live video: %+v", now)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
