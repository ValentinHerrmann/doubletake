package miracast

import (
	"fmt"
	"strconv"
	"strings"
)

// VideoMode is one CEA resolution/refresh entry of wfd_video_formats.
type VideoMode struct {
	Index  int // bit position in the CEA support bitmap
	Width  int
	Height int
	FPS    int
	// Level is the minimum H.264 level bit (wfd_video_formats level bitmap)
	// required to carry the mode.
	Level uint8
}

// H.264 level bits of wfd_video_formats.
const (
	level31 uint8 = 0x01
	level32 uint8 = 0x02
	level40 uint8 = 0x04
	level41 uint8 = 0x08
	level42 uint8 = 0x10
)

// profileCBP is the Constrained Baseline bit of the profile bitmap. Every WFD
// sink must decode it, so the source always encodes it.
const profileCBP uint8 = 0x01

// ceaModes lists the progressive CEA modes the source can produce, in order of
// preference. Interlaced modes are never offered.
var ceaModes = []VideoMode{
	{Index: 8, Width: 1920, Height: 1080, FPS: 60, Level: level42},
	{Index: 7, Width: 1920, Height: 1080, FPS: 30, Level: level40},
	{Index: 6, Width: 1280, Height: 720, FPS: 60, Level: level32},
	{Index: 5, Width: 1280, Height: 720, FPS: 30, Level: level31},
	{Index: 0, Width: 640, Height: 480, FPS: 60, Level: level31},
}

// SinkCapabilities is the part of an M3 GET_PARAMETER reply the source needs.
type SinkCapabilities struct {
	CEA          uint32 // CEA support bitmap of the first Constrained Baseline entry
	MaxLevel     uint8  // highest level bit of that entry
	AudioLPCM    uint32 // LPCM modes bitmap; zero when the sink takes no LPCM
	RTPPort      int    // sink's RTP port 0 from wfd_client_rtp_ports
	RTPProfile   string // transport profile from wfd_client_rtp_ports
	HDCPRequired bool   // wfd_content_protection names an HDCP version
}

// lpcm48kStereo is the LPCM modes bit for 48 kHz, 16-bit, 2 channels, which
// every sink taking LPCM must support.
const lpcm48kStereo = 0x2

// ParseSinkCapabilities decodes a text/parameters body from the sink.
func ParseSinkCapabilities(params map[string]string) (SinkCapabilities, error) {
	var caps SinkCapabilities
	video, ok := params["wfd_video_formats"]
	if !ok || video == "none" {
		return caps, fmt.Errorf("sink reports no wfd_video_formats")
	}
	if err := caps.parseVideo(video); err != nil {
		return caps, err
	}
	if audio := params["wfd_audio_codecs"]; audio != "" && audio != "none" {
		caps.parseAudio(audio)
	}
	ports, ok := params["wfd_client_rtp_ports"]
	if !ok {
		return caps, fmt.Errorf("sink reports no wfd_client_rtp_ports")
	}
	fields := strings.Fields(ports)
	if len(fields) < 2 {
		return caps, fmt.Errorf("malformed wfd_client_rtp_ports %q", ports)
	}
	port, err := strconv.Atoi(fields[1])
	if err != nil || port <= 0 || port > 65535 {
		return caps, fmt.Errorf("malformed RTP port in wfd_client_rtp_ports %q", ports)
	}
	caps.RTPProfile, caps.RTPPort = fields[0], port
	if protection := params["wfd_content_protection"]; protection != "" && protection != "none" {
		caps.HDCPRequired = true
	}
	return caps, nil
}

// parseVideo reads "native preferred codec[, codec]*", where each codec is
// "profile level CEA VESA HH latency min-slice slice-enc frame-rate-ctl max-hres max-vres".
func (c *SinkCapabilities) parseVideo(value string) error {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return fmt.Errorf("malformed wfd_video_formats %q", value)
	}
	for _, codec := range strings.Split(strings.Join(fields[2:], " "), ",") {
		parts := strings.Fields(codec)
		if len(parts) < 3 {
			return fmt.Errorf("malformed wfd_video_formats codec %q", codec)
		}
		profile, err1 := strconv.ParseUint(parts[0], 16, 8)
		level, err2 := strconv.ParseUint(parts[1], 16, 8)
		cea, err3 := strconv.ParseUint(parts[2], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			return fmt.Errorf("malformed wfd_video_formats codec %q", codec)
		}
		if uint8(profile)&profileCBP == 0 {
			continue
		}
		c.CEA, c.MaxLevel = uint32(cea), uint8(level)
		return nil
	}
	return fmt.Errorf("sink does not offer H.264 Constrained Baseline in %q", value)
}

// parseAudio reads "codec modes latency[, codec modes latency]*".
func (c *SinkCapabilities) parseAudio(value string) {
	for _, entry := range strings.Split(value, ",") {
		parts := strings.Fields(entry)
		if len(parts) < 2 || parts[0] != "LPCM" {
			continue
		}
		if modes, err := strconv.ParseUint(parts[1], 16, 32); err == nil {
			c.AudioLPCM = uint32(modes)
		}
	}
}

// SelectVideoMode picks the best CEA mode the sink supports at its level,
// skipping modes faster than maxFPS.
func (c SinkCapabilities) SelectVideoMode(maxFPS int) (VideoMode, error) {
	for _, mode := range ceaModes {
		if mode.FPS > maxFPS || c.CEA&(1<<mode.Index) == 0 || mode.Level > c.MaxLevel {
			continue
		}
		return mode, nil
	}
	// CEA 640x480p60 is mandatory for every sink.
	return ceaModes[len(ceaModes)-1], nil
}

// HasLPCM48k reports whether the sink accepts the source's only audio format.
func (c SinkCapabilities) HasLPCM48k() bool {
	return c.AudioLPCM&lpcm48kStereo != 0
}

// VideoFormatsParam is the wfd_video_formats value the source sends in M4.
func VideoFormatsParam(mode VideoMode) string {
	return fmt.Sprintf("00 00 %02x %02x %08x 00000000 00000000 00 0000 0000 00 none none",
		profileCBP, mode.Level, uint32(1)<<mode.Index)
}

// AudioCodecsParam is the wfd_audio_codecs value the source sends in M4.
func AudioCodecsParam() string {
	return fmt.Sprintf("LPCM %08x 00", lpcm48kStereo)
}
