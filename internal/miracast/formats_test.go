package miracast

import (
	"bytes"
	"testing"
)

func TestSourceIEsRoundTrip(t *testing.T) {
	ies := SourceIEs(DefaultControlPort)
	want := []byte{0x00, 0x00, 0x06, 0x00, 0x10, 0x1c, 0x44, 0x00, 0xc8}
	if !bytes.Equal(ies, want) {
		t.Fatalf("SourceIEs = % x, want % x", ies, want)
	}
	info, err := ParseDeviceInfo(ies)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsSink() || !info.SessionAvailable || info.ControlPort != DefaultControlPort {
		t.Fatalf("ParseDeviceInfo = %+v", info)
	}
}

func TestParseDeviceInfoSkipsOtherSubelements(t *testing.T) {
	// A coupled-sink subelement (ID 6) before a primary sink's device info.
	ies := []byte{0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x01, 0x11, 0x1c, 0x44, 0x00, 0x32}
	info, err := ParseDeviceInfo(ies)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsSink() || !info.SessionAvailable {
		t.Fatalf("ParseDeviceInfo = %+v, want an available sink", info)
	}
	if _, err := ParseDeviceInfo([]byte{0x00, 0x00, 0x06, 0x01}); err == nil {
		t.Fatal("truncated subelement was accepted")
	}
}

func TestParseSinkCapabilities(t *testing.T) {
	params := parseParameters("wfd_video_formats: 00 00 02 04 000001ff 00000000 00000000 00 0000 0000 00 none none, 01 04 000000ff 00000000 00000000 00 0000 0000 00 none none\r\n" +
		"wfd_audio_codecs: LPCM 00000003 00, AAC 00000001 00\r\n" +
		"wfd_client_rtp_ports: RTP/AVP/UDP;unicast 19000 0 mode=play\r\n" +
		"wfd_content_protection: none\r\n")
	caps, err := ParseSinkCapabilities(params)
	if err != nil {
		t.Fatal(err)
	}
	// The first entry is CHP only; the CBP entry supplies the bitmap.
	if caps.CEA != 0xff || caps.MaxLevel != level40 || caps.RTPPort != 19000 ||
		caps.RTPProfile != "RTP/AVP/UDP;unicast" || !caps.HasLPCM48k() || caps.HDCPRequired {
		t.Fatalf("caps = %+v", caps)
	}
	mode, _ := caps.SelectVideoMode(60)
	if mode.Width != 1920 || mode.FPS != 30 {
		t.Fatalf("SelectVideoMode(60) = %+v, want 1080p30 (1080p60 needs level 4.2)", mode)
	}
	if got := VideoFormatsParam(mode); got != "00 00 01 04 00000080 00000000 00000000 00 0000 0000 00 none none" {
		t.Fatalf("VideoFormatsParam = %q", got)
	}
}

func TestSelectVideoModeHonoursFPSAndFallsBack(t *testing.T) {
	caps := SinkCapabilities{CEA: 1<<6 | 1<<5 | 1, MaxLevel: level42}
	if mode, _ := caps.SelectVideoMode(30); mode.Index != 5 {
		t.Fatalf("SelectVideoMode(30) = %+v, want 720p30", mode)
	}
	if mode, _ := caps.SelectVideoMode(60); mode.Index != 6 {
		t.Fatalf("SelectVideoMode(60) = %+v, want 720p60", mode)
	}
	if mode, _ := (SinkCapabilities{}).SelectVideoMode(30); mode.Index != 0 {
		t.Fatalf("empty caps = %+v, want mandatory 640x480", mode)
	}
}

func TestParseSinkCapabilitiesRejectsMissingCBP(t *testing.T) {
	params := map[string]string{
		"wfd_video_formats":    "00 00 02 04 000001ff 00000000 00000000 00 0000 0000 00 none none",
		"wfd_client_rtp_ports": "RTP/AVP/UDP;unicast 19000 0 mode=play",
	}
	if _, err := ParseSinkCapabilities(params); err == nil {
		t.Fatal("sink without Constrained Baseline was accepted")
	}
}
