package miracast

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCRC32MPEG2(t *testing.T) {
	// Check value of CRC-32/MPEG-2 for "123456789".
	if got := crc32MPEG2([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("crc32MPEG2 = %08x", got)
	}
}

func TestTSMuxerPacketStructure(t *testing.T) {
	var packets [][]byte
	m := newTSMuxer(true, 100*time.Millisecond, func(p []byte) error {
		packets = append(packets, append([]byte(nil), p...))
		return nil
	})
	now := time.Unix(1000, 0)
	au := append([]byte{0, 0, 0, 1, 0x65}, bytes.Repeat([]byte{0xaa}, 1000)...)
	if err := m.WriteVideo(au, now, now, true); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteAudio(make([]byte, lpcmFramesPerPES*4), now, now); err != nil {
		t.Fatal(err)
	}
	continuity := map[uint16]int{}
	for i, p := range packets {
		if len(p) != tsPacketSize || p[0] != 0x47 {
			t.Fatalf("packet %d: size %d sync %#x", i, len(p), p[0])
		}
		pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
		if p[3]&0x10 == 0 {
			continue // adaptation only: CC does not advance
		}
		cc := int(p[3] & 0x0f)
		if last, ok := continuity[pid]; ok && cc != (last+1)&0x0f {
			t.Fatalf("packet %d PID %#x: CC %d after %d", i, pid, cc, last)
		}
		continuity[pid] = cc
	}
	for _, pid := range []uint16{pidPAT, pidPMT, pidVideo, pidAudio} {
		if _, ok := continuity[pid]; !ok {
			t.Fatalf("no payload on PID %#x", pid)
		}
	}
}

func TestTSMuxerLPCMHeader(t *testing.T) {
	var packets [][]byte
	m := newTSMuxer(true, 0, func(p []byte) error {
		packets = append(packets, append([]byte(nil), p...))
		return nil
	})
	pcm := make([]byte, lpcmFramesPerPES*4)
	pcm[0], pcm[1] = 0x34, 0x12 // first sample 0x1234 in S16LE
	now := time.Unix(1000, 0)
	if err := m.WriteAudio(pcm, now, now); err != nil {
		t.Fatal(err)
	}
	first := packets[1] // packets[0] carries the PCR
	if pid := uint16(first[1]&0x1f)<<8 | uint16(first[2]); pid != pidAudio || first[1]&0x40 == 0 {
		t.Fatalf("first audio packet header % x", first[:4])
	}
	pes := first[4:]
	wantLength := 8 + 4 + len(pcm)
	if !bytes.Equal(pes[:4], []byte{0, 0, 1, pesStreamPrivate}) || int(pes[4])<<8|int(pes[5]) != wantLength {
		t.Fatalf("PES header % x, want length %d", pes[:6], wantLength)
	}
	if got := pes[14:20]; !bytes.Equal(got, []byte{0xa0, 6, 0, 0x11, 0x12, 0x34}) {
		t.Fatalf("LPCM header and first sample % x", got)
	}
}

// TestTSMuxerOutputPlays encodes real H.264 and checks that ffprobe sees both
// elementary streams with the expected parameters.
func TestTSMuxerOutputPlays(t *testing.T) {
	for _, tool := range []string{"gst-launch-1.0", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	h264 := filepath.Join(dir, "in.h264")
	gst := exec.Command("gst-launch-1.0", "-q", "videotestsrc", "num-buffers=30", "!",
		"video/x-raw,width=640,height=480,framerate=30/1", "!", "x264enc", "key-int-max=15", "!",
		"video/x-h264,profile=constrained-baseline,stream-format=byte-stream,alignment=au", "!",
		"filesink", "location="+h264)
	if out, err := gst.CombinedOutput(); err != nil {
		t.Skipf("cannot encode test video: %v: %s", err, out)
	}
	data, err := os.ReadFile(h264)
	if err != nil {
		t.Fatal(err)
	}
	units := splitAccessUnits(data)
	if len(units) < 10 {
		t.Fatalf("split %d access units", len(units))
	}

	var out bytes.Buffer
	m := newTSMuxer(true, 100*time.Millisecond, func(p []byte) error {
		out.Write(p)
		return nil
	})
	start := time.Unix(1000, 0)
	for i, au := range units {
		at := start.Add(time.Duration(i) * time.Second / 30)
		if err := m.WriteVideo(au, at, at, isIDR(au)); err != nil {
			t.Fatal(err)
		}
		for a := range 3 {
			audioAt := at.Add(time.Duration(a) * 10 * time.Millisecond)
			if err := m.WriteAudio(make([]byte, lpcmFramesPerPES*4), audioAt, audioAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	ts := filepath.Join(dir, "out.ts")
	if err := os.WriteFile(ts, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-of", "json", ts).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var result struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			CodecTag  string `json:"codec_tag"`
			ID        string `json:"id"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(probe, &result); err != nil {
		t.Fatal(err)
	}
	// FFmpeg lists WFD LPCM (stream type 0x83) without decoding it, so the
	// audio check stops at the PMT entry; TestTSMuxerLPCMHeader covers its PES.
	var video, audio bool
	for _, s := range result.Streams {
		switch {
		case s.CodecName == "h264":
			video = s.Width == 640 && s.Height == 480 && s.ID == "0x1011"
		case s.ID == "0x1100":
			audio = s.CodecTag == "0x0083"
		}
	}
	if !video || !audio {
		t.Fatalf("ffprobe streams = %s", probe)
	}
}

// splitAccessUnits cuts an x264 byte stream at its access unit delimiters.
func splitAccessUnits(data []byte) [][]byte {
	var units [][]byte
	delimiter := []byte{0, 0, 0, 1, 0x09}
	for {
		next := bytes.Index(data[1:], delimiter)
		if next < 0 {
			return append(units, data)
		}
		units = append(units, data[:next+1])
		data = data[next+1:]
	}
}

func isIDR(au []byte) bool {
	return bytes.Contains(au, []byte{0, 0, 1, 0x65}) || bytes.Contains(au, []byte{0, 0, 1, 0x25})
}
