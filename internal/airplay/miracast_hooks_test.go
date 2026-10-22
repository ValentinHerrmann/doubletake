package airplay

import (
	"slices"
	"strings"
	"testing"
)

func TestH264ProfileConstrainsEncoderOutput(t *testing.T) {
	hasX264 := func(element string) bool { return element == "x264enc" }
	cfg := CaptureConfig{FPS: 30, Bitrate: 4000, HWAccel: "auto", VideoCodec: VideoCodecH264, H264Profile: "constrained-baseline"}
	encoder, err := selectGstEncoderWithProbe(cfg, hasX264, false)
	if err != nil {
		t.Fatal(err)
	}
	args := appendGstVideoEncoding(nil, encoder, true)
	at := slices.Index(args, "video/x-h264,profile=constrained-baseline")
	if at < 2 || args[at-1] != "!" || !slices.Contains(args[:at], "x264enc") || slices.Contains(args[:at], "h264parse") {
		t.Fatalf("profile caps must follow the encoder directly: %s", strings.Join(args, " "))
	}

	cfg.H264Profile = ""
	encoder, _ = selectGstEncoderWithProbe(cfg, hasX264, false)
	if joined := strings.Join(appendGstVideoEncoding(nil, encoder, true), " "); strings.Contains(joined, "profile=") {
		t.Fatalf("no profile requested, but the pipeline constrains one: %s", joined)
	}
}

func TestPCM48CaptureUses48kHzTenMillisecondFrames(t *testing.T) {
	args := strings.Join(audioCapturePipelineArgs([]string{"audiotestsrc"}, AudioCodecPCM48, true), " ")
	for _, want := range []string{"rate=48000,channels=2,format=S16BE", "output-buffer-duration=480/48000"} {
		if !strings.Contains(args, want) {
			t.Errorf("PCM48 pipeline lacks %q: %s", want, args)
		}
	}
	if got := audioCaptureFragmentTime(AudioCodecPCM48); got.Microseconds() != 10000 {
		t.Errorf("PCM48 fragment time = %v, want 10ms", got)
	}
	if args := strings.Join(audioCapturePipelineArgs([]string{"audiotestsrc"}, AudioCodecALAC, true), " "); !strings.Contains(args, "rate=44100") {
		t.Errorf("ALAC capture must stay at 44.1 kHz: %s", args)
	}
}

func TestRTPL16ReaderUsesConfiguredRate(t *testing.T) {
	if got := samplesDurationAt(480, 48000); got.Microseconds() != 10000 {
		t.Fatalf("samplesDurationAt(480, 48000) = %v", got)
	}
	if got := durationToSamplesAt(samplesDurationAt(48000, 48000), 48000); got != 48000 {
		t.Fatalf("round trip = %d samples", got)
	}
	reader := newRTPL16PCMFrameReaderAt(nil, 48000).(*rtpL16PCMFrameReader)
	if reader.rate() != 48000 || newRTPL16PCMFrameReaderWithNow(nil, nil).rate() != audioSampleRate {
		t.Fatal("reader rate defaults are wrong")
	}
}
