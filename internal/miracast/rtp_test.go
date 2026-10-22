package miracast

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

type datagrams [][]byte

func (d *datagrams) Write(p []byte) (int, error) {
	*d = append(*d, append([]byte(nil), p...))
	return len(p), nil
}

func TestRTPTSWriterGroupsSevenPackets(t *testing.T) {
	var sent datagrams
	w := newRTPTSWriter(&sent)
	start := w.start
	w.now = func() time.Time { return start.Add(time.Second) }
	packet := bytes.Repeat([]byte{0x47}, tsPacketSize)
	for range 9 {
		if err := w.WritePacket(packet); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || len(sent[0]) != 12+7*tsPacketSize || len(sent[1]) != 12+2*tsPacketSize {
		t.Fatalf("datagram sizes: %d datagrams", len(sent))
	}
	h := sent[0]
	if h[0] != 0x80 || h[1] != rtpPayloadTypeMP2T || binary.BigEndian.Uint32(h[4:8]) != 90000 {
		t.Fatalf("RTP header % x", h[:12])
	}
	if binary.BigEndian.Uint16(sent[1][2:4]) != binary.BigEndian.Uint16(h[2:4])+1 {
		t.Fatal("sequence number did not advance")
	}
	if err := w.Flush(); err != nil || len(sent) != 2 {
		t.Fatal("empty flush sent a datagram")
	}
}
