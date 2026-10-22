package miracast

import (
	"bytes"
	"encoding/binary"
	"time"
)

// MPEG-TS layout used by WFD sources (WFD spec, appendix: PIDs recommended
// for the single program).
const (
	tsPacketSize = 188
	pidPAT       = 0x0000
	pidPMT       = 0x0100
	pidPCR       = 0x1000
	pidVideo     = 0x1011
	pidAudio     = 0x1100

	streamTypeH264 = 0x1b
	streamTypeLPCM = 0x83 // WFD LPCM in private_stream_1

	pesStreamVideo   = 0xe0
	pesStreamPrivate = 0xbd

	// tableInterval bounds the time between PAT/PMT repetitions; sinks that
	// join mid-stream need them to locate the elementary streams.
	tableInterval = 100 * time.Millisecond
)

// lpcmFramesPerPES matches the 6 x 80-sample WFD LPCM access unit grouping
// (10 ms at 48 kHz); the mux accepts any whole number of stereo frames.
const lpcmFramesPerPES = 480

// tsMuxer packetizes H.264 access units and 48 kHz stereo LPCM into one
// MPEG-TS program. Timestamps are wall-clock times; the first one written
// becomes the program's zero point. It is not safe for concurrent use.
type tsMuxer struct {
	write      func(packet []byte) error
	audio      bool
	epoch      time.Time
	haveEpoch  bool
	delay      time.Duration // PTS lead over PCR: the sink's playout buffer
	lastTables time.Time
	cc         map[uint16]uint8
	packet     [tsPacketSize]byte
}

func newTSMuxer(audio bool, delay time.Duration, write func([]byte) error) *tsMuxer {
	return &tsMuxer{write: write, audio: audio, delay: delay, cc: make(map[uint16]uint8)}
}

// WriteVideo muxes one Annex-B access unit captured at pts.
func (m *tsMuxer) WriteVideo(annexB []byte, pts, now time.Time, keyframe bool) error {
	if keyframe || now.Sub(m.lastTables) >= tableInterval {
		if err := m.writeTables(); err != nil {
			return err
		}
		m.lastTables = now
	}
	if err := m.writePCR(now); err != nil {
		return err
	}
	payload := annexB
	if !startsWithAUD(annexB) {
		// H.264 in MPEG-TS requires an access unit delimiter in front of each
		// picture. Not every encoder emits one.
		payload = append(append(make([]byte, 0, len(audNAL)+len(annexB)), audNAL...), annexB...)
	}
	// PES_packet_length 0 (unbounded) is permitted only for video.
	return m.writePES(pidVideo, pesStreamVideo, m.pts(pts), payload, false)
}

// WriteAudio muxes interleaved stereo S16LE PCM captured at pts.
func (m *tsMuxer) WriteAudio(pcmS16LE []byte, pts, now time.Time) error {
	if err := m.writePCR(now); err != nil {
		return err
	}
	frames := len(pcmS16LE) / 4
	payload := make([]byte, 4+len(pcmS16LE))
	// WFD LPCM audio data header: sub_stream_id 0xa0, number_of_frame_header,
	// reserved/emphasis, then quantization (16 bit = 0) | sampling frequency
	// (48 kHz = 2) | channels (stereo = 1).
	payload[0] = 0xa0
	payload[1] = byte((frames + 79) / 80)
	payload[2] = 0
	payload[3] = 0<<6 | 2<<3 | 1
	for i := 0; i+1 < len(pcmS16LE); i += 2 {
		payload[4+i], payload[5+i] = pcmS16LE[i+1], pcmS16LE[i] // big-endian samples
	}
	return m.writePES(pidAudio, pesStreamPrivate, m.pts(pts), payload, true)
}

// pts converts a capture time to the 90 kHz program clock, delayed so the
// sink has the same buffer for every stream.
func (m *tsMuxer) pts(t time.Time) uint64 {
	return m.clock90k(t.Add(m.delay))
}

func (m *tsMuxer) clock90k(t time.Time) uint64 {
	if !m.haveEpoch {
		// Start the program clock a little before the first sample so early
		// audio captured slightly before the first video frame stays positive.
		m.epoch, m.haveEpoch = t.Add(-time.Second), true
	}
	d := t.Sub(m.epoch)
	if d < 0 {
		d = 0
	}
	return uint64(d/time.Microsecond) * 9 / 100 & (1<<33 - 1)
}

func (m *tsMuxer) writeTables() error {
	pat := []byte{
		0x00,       // table_id: program_association_section
		0xb0, 0x0d, // section_syntax_indicator, section_length 13
		0x00, 0x01, // transport_stream_id
		0xc1,       // version 0, current_next_indicator
		0x00, 0x00, // section_number, last_section_number
		0x00, 0x01, // program_number 1
		0xe0 | pidPMT>>8, pidPMT & 0xff,
	}
	if err := m.writeSection(pidPAT, pat); err != nil {
		return err
	}
	pmt := []byte{
		0x02,       // table_id: TS_program_map_section
		0xb0, 0x00, // section_length patched below
		0x00, 0x01, // program_number
		0xc1,
		0x00, 0x00,
		0xe0 | pidPCR>>8, pidPCR & 0xff,
		0xf0, 0x00, // program_info_length 0
		streamTypeH264, 0xe0 | pidVideo>>8, pidVideo & 0xff, 0xf0, 0x00,
	}
	if m.audio {
		pmt = append(pmt, streamTypeLPCM, 0xe0|pidAudio>>8, pidAudio&0xff, 0xf0, 0x00)
	}
	sectionLength := len(pmt) - 3 + 4 // after the length field, including CRC
	pmt[1] |= byte(sectionLength >> 8)
	pmt[2] = byte(sectionLength)
	return m.writeSection(pidPMT, pmt)
}

func (m *tsMuxer) writeSection(pid uint16, section []byte) error {
	crc := crc32MPEG2(section)
	payload := make([]byte, 0, 1+len(section)+4)
	payload = append(payload, 0x00) // pointer_field
	payload = append(payload, section...)
	payload = binary.BigEndian.AppendUint32(payload, crc)
	return m.writePayload(pid, payload, true)
}

// writePCR emits an adaptation-only packet on the PCR PID. One precedes every
// PES packet, which keeps PCR spacing well under the 100 ms limit.
func (m *tsMuxer) writePCR(now time.Time) error {
	p := m.packet[:]
	p[0] = 0x47
	p[1] = byte(pidPCR >> 8)
	p[2] = byte(pidPCR & 0xff)
	p[3] = 0x20 | m.cc[pidPCR] // adaptation field only; CC does not advance
	p[4] = 183
	p[5] = 0x10 // PCR_flag
	base := m.clock90k(now)
	p[6] = byte(base >> 25)
	p[7] = byte(base >> 17)
	p[8] = byte(base >> 9)
	p[9] = byte(base >> 1)
	p[10] = byte(base<<7) | 0x7e // reserved bits, extension 0
	p[11] = 0
	for i := 12; i < tsPacketSize; i++ {
		p[i] = 0xff
	}
	return m.write(p)
}

func (m *tsMuxer) writePES(pid uint16, streamID byte, pts uint64, data []byte, bounded bool) error {
	header := []byte{0x00, 0x00, 0x01, streamID, 0x00, 0x00, 0x84, 0x80, 0x05,
		byte(0x21 | (pts>>29)&0x0e), byte(pts >> 22), byte(0x01 | (pts>>14)&0xfe),
		byte(pts >> 7), byte(0x01 | (pts<<1)&0xfe)}
	if bounded {
		binary.BigEndian.PutUint16(header[4:6], uint16(len(header)-6+len(data)))
	}
	return m.writePayload(pid, append(header, data...), true)
}

// writePayload splits payload over TS packets on pid, stuffing the last one.
func (m *tsMuxer) writePayload(pid uint16, payload []byte, start bool) error {
	for len(payload) > 0 {
		p := m.packet[:]
		p[0] = 0x47
		p[1] = byte(pid>>8) & 0x1f
		if start {
			p[1] |= 0x40 // payload_unit_start_indicator
			start = false
		}
		p[2] = byte(pid)
		cc := m.cc[pid]
		m.cc[pid] = (cc + 1) & 0x0f
		n := min(len(payload), tsPacketSize-4)
		offset := 4
		if n < tsPacketSize-4 {
			p[3] = 0x30 | cc // adaptation field and payload
			afLength := tsPacketSize - 4 - 1 - n
			p[4] = byte(afLength)
			offset = 5
			if afLength > 0 {
				p[5] = 0x00 // no flags
				for i := 6; i < 5+afLength; i++ {
					p[i] = 0xff
				}
				offset = 5 + afLength
			}
		} else {
			p[3] = 0x10 | cc // payload only
		}
		copy(p[offset:], payload[:n])
		if err := m.write(p); err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

var audNAL = []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0xf0}

func startsWithAUD(annexB []byte) bool {
	for _, prefix := range [][]byte{{0, 0, 0, 1}, {0, 0, 1}} {
		if bytes.HasPrefix(annexB, prefix) && len(annexB) > len(prefix) {
			return annexB[len(prefix)]&0x1f == 9
		}
	}
	return false
}

// crc32MPEG2 is the CRC of ISO/IEC 13818-1 sections: polynomial 0x04c11db7,
// initial value 0xffffffff, no reflection and no final inversion.
func crc32MPEG2(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
