package miracast

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"doubletake/internal/airplay"
)

// H264Profile is the encoder profile every Miracast stream uses: Constrained
// Baseline is the one profile all WFD sinks must decode.
const H264Profile = "constrained-baseline"

const (
	// sinkConnectTimeout bounds the wait for the sink's RTSP connection after
	// the P2P group is up.
	sinkConnectTimeout = 20 * time.Second
	// playoutDelay is the default PTS lead over PCR: the buffer the sink keeps
	// to absorb Wi-Fi Direct jitter. -target-latency-ms overrides it.
	playoutDelay      = 150 * time.Millisecond
	groupPollInterval = 2 * time.Second
)

// VideoSource yields encoded H.264 access units; airplay.ScreenCapture and
// airplay.BroadcastSink both satisfy it.
type VideoSource interface {
	ReadVideoAccessUnit() (airplay.VideoAccessUnit, error)
}

// Session is one Miracast mirroring session with one sink.
type Session struct {
	Negotiated Negotiated
	group      *Group
	control    *wfdControl
	udp        *net.UDPConn
	muted      atomic.Bool
	closeOnce  sync.Once
}

// Start forms the P2P group with the sink at hwAddress and runs the WFD
// negotiation. The caller then starts an encoder at Negotiated.Mode and passes
// it to Stream.
func Start(ctx context.Context, p2p *P2P, hwAddress string, maxFPS int, wantAudio bool) (*Session, error) {
	// Listen before the group exists: the sink connects as soon as it has an
	// address, and may give up quickly if nobody answers.
	listener, err := net.Listen("tcp4", ":"+strconv.Itoa(DefaultControlPort))
	if err != nil {
		return nil, fmt.Errorf("listen for the display on TCP %d: %w", DefaultControlPort, err)
	}
	defer listener.Close()

	log.Printf("[MIRACAST] connecting to %s over Wi-Fi Direct (accept the request on the display if it asks)", hwAddress)
	group, err := p2p.Connect(ctx, hwAddress)
	if err != nil {
		return nil, err
	}
	log.Printf("[MIRACAST] Wi-Fi Direct group up, local address %s; waiting for the display", group.LocalIP)

	conn, err := acceptSink(ctx, listener, group.LocalIP)
	if err != nil {
		group.Close()
		return nil, err
	}
	control, neg, err := negotiateWFD(ctx, conn, maxFPS, wantAudio)
	if err != nil {
		group.Close()
		return nil, fmt.Errorf("Miracast negotiation: %w", err)
	}
	udp, err := net.DialUDP("udp4", &net.UDPAddr{IP: group.LocalIP}, neg.RTPAddr)
	if err != nil {
		control.Close()
		group.Close()
		return nil, fmt.Errorf("open media socket: %w", err)
	}
	log.Printf("[MIRACAST] streaming %dx%dp%d to %s (audio %v)", neg.Mode.Width, neg.Mode.Height, neg.Mode.FPS, neg.RTPAddr, neg.Audio)
	return &Session{Negotiated: neg, group: group, control: control, udp: udp}, nil
}

// acceptSink waits for the sink's control connection on the group address and
// drops connections arriving on any other interface.
func acceptSink(ctx context.Context, listener net.Listener, groupIP net.IP) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				accepted <- result{err: err}
				return
			}
			if local, ok := conn.LocalAddr().(*net.TCPAddr); ok && local.IP.Equal(groupIP) {
				accepted <- result{conn: conn}
				return
			}
			conn.Close()
		}
	}()
	select {
	case r := <-accepted:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(sinkConnectTimeout):
		return nil, fmt.Errorf("the display joined Wi-Fi Direct but did not open its control connection to TCP %d within %s; "+
			"if an earlier session ended without a proper stop (crash, kill, second Ctrl+C), the display usually needs a power cycle; "+
			"otherwise check that no firewall blocks inbound TCP %d on the Wi-Fi Direct interface",
			DefaultControlPort, sinkConnectTimeout, DefaultControlPort)
	}
}

// HasAudio reports whether the sink accepted the LPCM stream.
func (s *Session) HasAudio() bool { return s.Negotiated.Audio }

// SetAudioMuted replaces audio with silence while muted. It never fails; the
// error result matches the daemon's stream interface.
func (s *Session) SetAudioMuted(muted bool) error {
	s.muted.Store(muted)
	return nil
}

// Stream sends video (and audio, when negotiated and non-nil) until ctx ends,
// the sink tears down or the P2P group drops.
func (s *Session) Stream(ctx context.Context, video VideoSource, audio *airplay.AudioCapture) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var muxMu sync.Mutex
	rtp := newRTPTSWriter(s.udp)
	delay := playoutDelay
	if airplay.HasExplicitTargetLatency() {
		// -target-latency-ms sets the sink's buffer for Miracast as well.
		delay = airplay.TargetLatency()
	}
	dbg("[MIRACAST] playout delay %s", delay)
	mux := newTSMuxer(s.Negotiated.Audio && audio != nil, delay, rtp.WritePacket)

	errs := make(chan error, 3)
	go func() {
		for {
			au, err := video.ReadVideoAccessUnit()
			if err != nil {
				errs <- fmt.Errorf("video capture: %w", err)
				return
			}
			now := time.Now()
			pts := au.PTS
			if pts.IsZero() {
				pts = now
			}
			muxMu.Lock()
			err = mux.WriteVideo(au.AnnexB, pts, now, hasIDR(au.AnnexB))
			if err == nil {
				err = rtp.Flush()
			}
			muxMu.Unlock()
			if err != nil {
				errs <- fmt.Errorf("send video: %w", err)
				return
			}
		}
	}()
	if mux.audio {
		go func() {
			buf := make([]byte, lpcmFramesPerPES*4)
			for {
				n, pts, err := audio.ReadFrameAt(buf)
				if err != nil {
					errs <- fmt.Errorf("audio capture: %w", err)
					return
				}
				now := time.Now()
				if pts.IsZero() {
					pts = now
				}
				pcm := buf[:n]
				if s.muted.Load() {
					clear(pcm)
				}
				muxMu.Lock()
				err = mux.WriteAudio(pcm, pts, now)
				if err == nil {
					err = rtp.Flush()
				}
				muxMu.Unlock()
				if err != nil {
					errs <- fmt.Errorf("send audio: %w", err)
					return
				}
			}
		}()
	}

	ticker := time.NewTicker(groupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-errs:
			return err
		case <-s.control.Done():
			if err := s.control.failure(); !errors.Is(err, errTornDown) {
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.group.Down() {
				return fmt.Errorf("the Wi-Fi Direct connection to the display dropped")
			}
		}
	}
}

// Close ends the WFD session, then the P2P group.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.control.Close()
		s.udp.Close()
		err = s.group.Close()
	})
	return err
}

// hasIDR reports whether an Annex-B access unit contains an IDR slice.
func hasIDR(annexB []byte) bool {
	for i := 0; i+3 < len(annexB); i++ {
		if annexB[i] == 0 && annexB[i+1] == 0 && annexB[i+2] == 1 && annexB[i+3]&0x1f == 5 {
			return true
		}
	}
	return false
}
