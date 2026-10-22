package miracast

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"time"
)

const (
	rtpPayloadTypeMP2T = 33
	tsPacketsPerRTP    = 7 // 7 x 188 + 12 bytes fits a 1500-byte MTU
)

// rtpTSWriter groups TS packets into RTP/MP2T datagrams (RFC 2250).
type rtpTSWriter struct {
	conn     io.Writer // a connected UDP socket
	now      func() time.Time
	start    time.Time
	sequence uint16
	ssrc     uint32
	buf      []byte
	packets  int
}

func newRTPTSWriter(conn io.Writer) *rtpTSWriter {
	var random [6]byte
	_, _ = rand.Read(random[:])
	w := &rtpTSWriter{
		conn:     conn,
		now:      time.Now,
		sequence: binary.BigEndian.Uint16(random[0:2]),
		ssrc:     binary.BigEndian.Uint32(random[2:6]),
		buf:      make([]byte, 12, 12+tsPacketsPerRTP*tsPacketSize),
	}
	w.start = w.now()
	return w
}

// WritePacket appends one TS packet, sending a datagram once it is full.
func (w *rtpTSWriter) WritePacket(packet []byte) error {
	w.buf = append(w.buf, packet...)
	w.packets++
	if w.packets == tsPacketsPerRTP {
		return w.Flush()
	}
	return nil
}

// Flush sends any buffered TS packets without waiting for a full datagram.
func (w *rtpTSWriter) Flush() error {
	if w.packets == 0 {
		return nil
	}
	h := w.buf[:12]
	h[0] = 0x80
	h[1] = rtpPayloadTypeMP2T
	binary.BigEndian.PutUint16(h[2:4], w.sequence)
	binary.BigEndian.PutUint32(h[4:8], uint32(w.now().Sub(w.start)/time.Microsecond*9/100))
	binary.BigEndian.PutUint32(h[8:12], w.ssrc)
	w.sequence++
	_, err := w.conn.Write(w.buf)
	w.buf = w.buf[:12]
	w.packets = 0
	return err
}
