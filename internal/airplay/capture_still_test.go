package airplay

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"testing"
)

func TestAccessUnitHasKeyframe(t *testing.T) {
	cases := []struct {
		name  string
		codec VideoCodec
		unit  []byte
		want  bool
	}{
		{"h264 idr", VideoCodecH264, []byte{0, 0, 0, 1, 0x09, 0xf0, 0, 0, 0, 1, 0x67, 0, 0, 0, 1, 0x65}, true},
		{"h264 p-slice", VideoCodecH264, []byte{0, 0, 0, 1, 0x41, 0x9a}, false},
		{"hevc idr_w_radl", VideoCodecHEVC, []byte{0, 0, 0, 1, 0x40, 0x01, 0, 0, 1, 19 << 1, 0x01}, true},
		{"hevc cra", VideoCodecHEVC, []byte{0, 0, 1, 21 << 1, 0x01}, true},
		{"hevc trail_r", VideoCodecHEVC, []byte{0, 0, 1, 1 << 1, 0x01}, false},
	}
	for _, tc := range cases {
		if got := AccessUnitHasKeyframe(tc.unit, tc.codec); got != tc.want {
			t.Errorf("%s: AccessUnitHasKeyframe = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFirstKeyframeAccessUnit(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 1}
	pps := []byte{0, 0, 0, 1, 0x68, 2}
	idr1 := []byte{0, 0, 0, 1, 0x65, 3}
	idr2 := []byte{0, 0, 0, 1, 0x65, 4}
	pSlice := []byte{0, 0, 0, 1, 0x41, 5}
	stream := bytes.Join([][]byte{
		pSlice, sps, pps, idr1, idr2, pSlice, sps, pps, idr1,
	}, nil)
	got, err := firstKeyframeAccessUnit(stream, VideoCodecH264)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Join([][]byte{sps, pps, idr1, idr2}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("keyframe unit = %x, want %x", got, want)
	}

	if _, err := firstKeyframeAccessUnit(pSlice, VideoCodecH264); err == nil {
		t.Fatal("stream without keyframe returned a unit")
	}
}

func requireGstElements(t *testing.T, elements ...string) {
	t.Helper()
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 not installed")
	}
	for _, element := range elements {
		if !hasGstElement(element) {
			t.Skipf("GStreamer element %s not installed", element)
		}
	}
}

func softwareStillConfig(t *testing.T) stillConfig {
	t.Helper()
	encoder, err := selectGstEncoderWithProbe(CaptureConfig{FPS: 30, HWAccel: "none"}, hasGstElement, false)
	if err != nil {
		t.Skipf("no software H.264 encoder: %v", err)
	}
	return stillConfig{encoder: encoder, fps: 30}
}

func TestEncodeBlackStill(t *testing.T) {
	requireGstElements(t, "videotestsrc", "x264enc", "h264parse")
	cfg := softwareStillConfig(t)

	unit, err := encodeBlackStill(context.Background(), cfg, 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	if !AccessUnitHasKeyframe(unit, VideoCodecH264) {
		t.Fatal("black still is not a keyframe")
	}
	if w, h, ok := accessUnitDimensions(unit, VideoCodecH264); !ok || w != 640 || h != 360 {
		t.Fatalf("black still size = %dx%d (ok=%v), want 640x360", w, h, ok)
	}
	// Limited-range black luma is 16.
	if mean := meanLuma(decodeLastPicture(t, [][]byte{unit}, 640, 360), 640, 360); mean > 20 {
		t.Fatalf("black still mean luma = %.1f, want black", mean)
	}
}

// decodeLastPicture decodes units and returns the last I420 picture.
func decodeLastPicture(t *testing.T, units [][]byte, width, height int) []byte {
	t.Helper()
	args := []string{"--quiet", "fdsrc", "fd=0"}
	for _, stage := range []gstStage{
		{"h264parse"}, {"avdec_h264"}, {"videoconvert"},
		{fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d", width, height)},
		{"fdsink", "fd=1"},
	} {
		args = appendGstStage(args, stage)
	}
	raw, err := runStillProcess(context.Background(), args, bytes.NewReader(bytes.Join(units, nil)))
	if err != nil {
		t.Fatal(err)
	}
	size := i420FrameSize(width, height)
	if len(raw) < size {
		t.Fatal("decoder produced no picture")
	}
	return raw[(len(raw)/size-1)*size:][:size]
}

func meanLuma(picture []byte, width, height int) float64 {
	var sum int
	for _, y := range picture[:width*height] {
		sum += int(y)
	}
	return float64(sum) / float64(width*height)
}

func meanAbsLumaDiff(a, b []byte, width, height int) float64 {
	var sum int
	for i := range width * height {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(width*height)
}

// liveTestGOP encodes a short moving test pattern and splits it into access
// units, as a capture group would see them.
func liveTestGOP(t *testing.T, cfg stillConfig, width, height, frames int) [][]byte {
	t.Helper()
	source := gstStage{"videotestsrc", "pattern=ball", fmt.Sprintf("num-buffers=%d", frames)}
	caps := gstStage{fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=30/1", width, height)}
	args := buildGstVideoPipeline(source, []gstStage{caps}, nil, cfg.encoder, 0, 0, false)
	out, err := runStillProcess(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	var current []byte
	for _, nal := range splitAnnexBAccessUnit(out) {
		raw := stripStartCode(nal)
		if isVCLNAL(VideoCodecH264, raw) {
			current = append(current, nal...)
			units = append(units, current)
			current = nil
			continue
		}
		current = append(current, nal...)
	}
	return units
}

func TestEncodeFrozenStill(t *testing.T) {
	requireGstElements(t, "videotestsrc", "x264enc", "h264parse", "avdec_h264", "rawvideoparse")
	cfg := softwareStillConfig(t)

	gop := liveTestGOP(t, cfg, 640, 360, 10)
	if len(gop) == 0 || !AccessUnitHasKeyframe(gop[0], VideoCodecH264) {
		t.Fatal("test GOP does not start with a keyframe")
	}
	unit, err := encodeFrozenStill(context.Background(), cfg, 640, 360, gop)
	if err != nil {
		t.Fatal(err)
	}
	if !AccessUnitHasKeyframe(unit, VideoCodecH264) {
		t.Fatal("frozen still is not a keyframe")
	}
	if w, h, ok := accessUnitDimensions(unit, VideoCodecH264); !ok || w != 640 || h != 360 {
		t.Fatalf("frozen still size = %dx%d (ok=%v), want 640x360", w, h, ok)
	}
	// The still must show the last live picture, not an earlier one.
	frozen := decodeLastPicture(t, [][]byte{unit}, 640, 360)
	last := decodeLastPicture(t, gop, 640, 360)
	first := decodeLastPicture(t, gop[:1], 640, 360)
	if diff := meanAbsLumaDiff(frozen, last, 640, 360); diff > 4 {
		t.Fatalf("frozen still differs from the last picture by %.1f luma", diff)
	}
	if meanAbsLumaDiff(first, last, 640, 360) <= meanAbsLumaDiff(frozen, last, 640, 360) {
		t.Fatal("test GOP does not move; cannot tell the last picture from the first")
	}

	if _, err := encodeFrozenStill(context.Background(), cfg, 640, 360, nil); err == nil {
		t.Fatal("freeze without a GOP succeeded")
	}
}
