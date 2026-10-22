package airplay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// stillEncodeTimeout bounds one still-frame decode or encode process.
const stillEncodeTimeout = 10 * time.Second

// maxStillOutputBytes bounds what a still-frame process may write to stdout.
// It holds a few encoded frames, or one decoded GOP of raw 4K frames.
const maxStillOutputBytes = 512 << 20

// stillConfig is the encoder configuration of a running capture. Still frames
// use the same encoder so receivers see the same codec and profile.
type stillConfig struct {
	encoder   encoderResult
	fps       int
	maxWidth  int
	maxHeight int
}

// outputSize returns the encoded size when the capture scales to a fixed
// receiver canvas. Unscaled captures report ok=false; their size comes from
// the stream's SPS instead.
func (c stillConfig) outputSize() (width, height int, ok bool) {
	if c.maxWidth <= 1 || c.maxHeight <= 1 {
		return 0, 0, false
	}
	return c.maxWidth &^ 1, c.maxHeight &^ 1, true
}

// stillRepeatFrames is how many copies of a still go into the encoder. More
// than one lets hardware encoders with internal delay flush on EOS.
func (c stillConfig) stillRepeatFrames() int {
	return c.fps/4 + 1
}

func nalPayloadType(codec VideoCodec, raw []byte) byte {
	if normalizeVideoCodec(codec) == VideoCodecHEVC {
		return hevcNALType(raw)
	}
	if len(raw) == 0 {
		return 0
	}
	return raw[0] & 0x1f
}

// isKeyframeNAL reports an H.264 IDR slice or an HEVC IRAP picture.
func isKeyframeNAL(codec VideoCodec, raw []byte) bool {
	t := nalPayloadType(codec, raw)
	if normalizeVideoCodec(codec) == VideoCodecHEVC {
		return t >= 16 && t <= 21
	}
	return t == 5
}

func isVCLNAL(codec VideoCodec, raw []byte) bool {
	t := nalPayloadType(codec, raw)
	if normalizeVideoCodec(codec) == VideoCodecHEVC {
		return t <= 31
	}
	return t >= 1 && t <= 5
}

// AccessUnitHasKeyframe reports whether an Annex-B access unit starts a new
// decodable picture sequence: an H.264 IDR or an HEVC IRAP picture.
func AccessUnitHasKeyframe(annexB []byte, codec VideoCodec) bool {
	for _, nal := range splitAnnexBAccessUnit(annexB) {
		if isKeyframeNAL(codec, stripStartCode(nal)) {
			return true
		}
	}
	return false
}

// accessUnitDimensions returns the coded picture size from the SPS in an
// Annex-B access unit.
func accessUnitDimensions(annexB []byte, codec VideoCodec) (width, height int, ok bool) {
	hevc := normalizeVideoCodec(codec) == VideoCodecHEVC
	for _, nal := range splitAnnexBAccessUnit(annexB) {
		raw := stripStartCode(nal)
		if hevc {
			if hevcNALType(raw) == 33 {
				if info, ok := parseHEVCSPS(raw); ok {
					return info.width, info.height, true
				}
			}
		} else if len(raw) > 0 && raw[0]&0x1f == 7 {
			return spsDimensions(raw)
		}
	}
	return 0, 0, false
}

// firstKeyframeAccessUnit returns the parameter sets and slices of the first
// keyframe in an Annex-B byte stream. The encoder repeats parameter sets
// before every keyframe, so a parameter set, AUD or non-keyframe slice after
// the keyframe slices ends the access unit.
func firstKeyframeAccessUnit(stream []byte, codec VideoCodec) ([]byte, error) {
	var prefix, unit []byte
	for _, nal := range splitAnnexBAccessUnit(stream) {
		raw := stripStartCode(nal)
		if len(unit) == 0 {
			if isKeyframeNAL(codec, raw) {
				unit = append(append(unit, prefix...), nal...)
				continue
			}
			if isVCLNAL(codec, raw) {
				// A slice before any keyframe belongs to no usable picture.
				prefix = prefix[:0]
				continue
			}
			prefix = append(prefix, nal...)
			continue
		}
		if !isKeyframeNAL(codec, raw) {
			break
		}
		unit = append(unit, nal...)
	}
	if len(unit) == 0 {
		return nil, errors.New("encoder produced no keyframe")
	}
	return unit, nil
}

// runStillProcess runs one short gst-launch-1.0 and returns its stdout.
func runStillProcess(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, stillEncodeTimeout)
	defer cancel()
	dbg("[STILL] gst-launch-1.0 %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "gst-launch-1.0", args...)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gst-launch-1.0: %w", err)
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxStillOutputBytes))
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", waitErr, msg)
		}
		return nil, waitErr
	}
	return out, nil
}

// encodeStill encodes frames from source with the capture's encoder and
// returns the first keyframe access unit.
func encodeStill(ctx context.Context, cfg stillConfig, source gstStage, beforeConvert []gstStage, stdin io.Reader) ([]byte, error) {
	args := buildGstVideoPipeline(source, beforeConvert, nil, cfg.encoder, 0, 0, false)
	out, err := runStillProcess(ctx, args, stdin)
	if err != nil {
		return nil, fmt.Errorf("encode still frame: %w", err)
	}
	return firstKeyframeAccessUnit(out, cfg.encoder.codec)
}

// encodeBlackStill encodes one black keyframe at the given size.
func encodeBlackStill(ctx context.Context, cfg stillConfig, width, height int) ([]byte, error) {
	source := gstStage{"videotestsrc", "pattern=black", fmt.Sprintf("num-buffers=%d", cfg.stillRepeatFrames())}
	caps := gstStage{fmt.Sprintf("video/x-raw,width=%d,height=%d,framerate=%d/1", width, height, cfg.fps)}
	return encodeStill(ctx, cfg, source, []gstStage{caps}, nil)
}

func i420FrameSize(width, height int) int {
	chroma := ((width + 1) / 2) * ((height + 1) / 2)
	return width*height + 2*chroma
}

// encodeFrozenStill decodes gop, the access units since the last keyframe,
// and encodes its last picture as a keyframe at the given size.
func encodeFrozenStill(ctx context.Context, cfg stillConfig, width, height int, gop [][]byte) ([]byte, error) {
	if len(gop) == 0 {
		return nil, errors.New("no picture to freeze yet")
	}
	parser, decoder := "h264parse", "avdec_h264"
	if normalizeVideoCodec(cfg.encoder.codec) == VideoCodecHEVC {
		parser, decoder = "h265parse", "avdec_h265"
	}
	readers := make([]io.Reader, len(gop))
	for i, unit := range gop {
		readers[i] = bytes.NewReader(unit)
	}
	decodeArgs := []string{"--quiet", "fdsrc", "fd=0"}
	for _, stage := range []gstStage{
		{parser},
		{decoder},
		{"videoconvert"},
		{"videoscale"},
		{fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d", width, height)},
		{"fdsink", "fd=1"},
	} {
		decodeArgs = appendGstStage(decodeArgs, stage)
	}
	raw, err := runStillProcess(ctx, decodeArgs, io.MultiReader(readers...))
	if err != nil {
		return nil, fmt.Errorf("decode frozen picture: %w", err)
	}
	frameSize := i420FrameSize(width, height)
	if len(raw) < frameSize {
		return nil, errors.New("decoder produced no picture")
	}
	last := raw[(len(raw)/frameSize-1)*frameSize:][:frameSize]

	repeats := cfg.stillRepeatFrames()
	frames := make([]io.Reader, repeats)
	for i := range frames {
		frames[i] = bytes.NewReader(last)
	}
	source := gstStage{"fdsrc", "fd=0", fmt.Sprintf("blocksize=%d", frameSize)}
	parse := gstStage{
		"rawvideoparse", "format=i420",
		fmt.Sprintf("width=%d", width), fmt.Sprintf("height=%d", height),
		fmt.Sprintf("framerate=%d/1", cfg.fps),
	}
	return encodeStill(ctx, cfg, source, []gstStage{parse}, io.MultiReader(frames...))
}
