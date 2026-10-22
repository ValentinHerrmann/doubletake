package miracast

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doubletake/internal/airplay"
)

// TestVideoHoldStreamDecodesAsMiracastTS runs the Miracast video path without
// Wi-Fi: a Constrained Baseline test capture behind a backpressured sink, muxed
// to MPEG-TS, through live, blackout, freeze and live again. FFmpeg must
// decode the result without errors.
func TestVideoHoldStreamDecodesAsMiracastTS(t *testing.T) {
	for _, tool := range []string{"gst-launch-1.0", "gst-inspect-1.0", "ffmpeg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	for _, element := range []string{"x264enc", "avdec_h264", "rawvideoparse", "rtponviftimestamp"} {
		if exec.Command("gst-inspect-1.0", element).Run() != nil {
			t.Skipf("GStreamer %s not available", element)
		}
	}

	const fps = 15
	capture, err := airplay.StartTestCapture(context.Background(), airplay.CaptureConfig{
		FPS:         fps,
		Bitrate:     1000,
		HWAccel:     "none",
		VideoCodec:  airplay.VideoCodecH264,
		H264Profile: H264Profile,
		MaxWidth:    640,
		MaxHeight:   360,
	})
	if err != nil {
		t.Fatalf("start test capture: %v", err)
	}
	defer capture.Stop()
	broadcast := airplay.NewBroadcastCaptureWithFrameRate(capture, fps)
	sink, err := broadcast.AddBackpressuredSink()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	go broadcast.Run()

	var ts bytes.Buffer
	mux := newTSMuxer(false, 100*time.Millisecond, func(p []byte) error {
		ts.Write(p)
		return nil
	})
	phases := []struct {
		hold     airplay.VideoHold
		duration time.Duration
	}{
		{airplay.HoldNone, 1500 * time.Millisecond},
		{airplay.HoldBlack, 3 * time.Second},
		{airplay.HoldFreeze, 3 * time.Second},
		{airplay.HoldNone, 3 * time.Second},
	}
	for i, phase := range phases {
		if err := broadcast.SetVideoHold(phase.hold); err != nil {
			t.Fatalf("phase %d: %v", i, err)
		}
		start := time.Now()
		var frames, keyframes int
		for time.Since(start) < phase.duration {
			au, err := sink.ReadVideoAccessUnit()
			if err != nil {
				t.Fatalf("phase %d: read: %v", i, err)
			}
			keyframe := airplay.AccessUnitHasKeyframe(au.AnnexB, airplay.VideoCodecH264)
			if err := mux.WriteVideo(au.AnnexB, au.PTS, time.Now(), keyframe); err != nil {
				t.Fatal(err)
			}
			// Skip frames still in flight from the previous phase.
			if time.Since(start) > time.Second {
				frames++
				if keyframe {
					keyframes++
				}
			}
		}
		switch {
		case phase.hold != airplay.HoldNone && (frames < 2 || frames != keyframes):
			t.Fatalf("phase %d (%s): %d frames, %d keyframes; want only repeated stills", i, phase.hold, frames, keyframes)
		case phase.hold == airplay.HoldNone && frames <= keyframes+5:
			t.Fatalf("phase %d (live): %d frames, %d keyframes; want live video", i, frames, keyframes)
		}
	}

	path := filepath.Join(t.TempDir(), "hold.ts")
	if err := os.WriteFile(path, ts.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("ffmpeg decode: %v: %s", err, out)
	}
}
