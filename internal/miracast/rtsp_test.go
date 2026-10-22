package miracast

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSink plays the sink side of M1-M8 over a real TCP connection.
type fakeSink struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
	cseq   int
}

func (s *fakeSink) expectRequest(method string) *rtspMessage {
	s.t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msg, err := readRTSPMessage(s.reader)
	if err != nil {
		s.t.Errorf("sink: reading %s: %v", method, err)
		return nil
	}
	if msg.method != method {
		s.t.Errorf("sink: got %s %s, want %s", msg.method, msg.uri, method)
	}
	return msg
}

func (s *fakeSink) reply(req *rtspMessage, headers, body string) {
	if body != "" {
		headers += fmt.Sprintf("Content-Type: text/parameters\r\nContent-Length: %d\r\n", len(body))
	}
	fmt.Fprintf(s.conn, "RTSP/1.0 200 OK\r\nCSeq: %d\r\n%s\r\n%s", req.cseq(), headers, body)
}

func (s *fakeSink) request(method, uri, headers string, body ...string) *rtspMessage {
	s.t.Helper()
	s.cseq++
	text := strings.Join(body, "")
	if text != "" {
		headers += fmt.Sprintf("Content-Length: %d\r\n", len(text))
	}
	fmt.Fprintf(s.conn, "%s %s RTSP/1.0\r\nCSeq: %d\r\n%s\r\n%s", method, uri, s.cseq, headers, text)
	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := readRTSPMessage(s.reader)
	if err != nil || resp.status != 200 || resp.cseq() != s.cseq {
		s.t.Errorf("sink: %s response %+v, %v", method, resp, err)
		return nil
	}
	return resp
}

func TestNegotiateWFD(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	m4 := make(chan string, 1)
	sinkDone := make(chan struct{})
	go func() {
		defer close(sinkDone)
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		s := &fakeSink{t: t, conn: conn, reader: bufio.NewReader(conn)}

		m1 := s.expectRequest("OPTIONS")
		if m1 == nil || m1.header("Require") != "org.wfa.wfd1.0" {
			t.Errorf("M1 = %+v", m1)
			return
		}
		s.reply(m1, "Public: org.wfa.wfd1.0, GET_PARAMETER, SET_PARAMETER\r\n", "")
		if resp := s.request("OPTIONS", "*", "Require: org.wfa.wfd1.0\r\n"); resp == nil ||
			!strings.Contains(resp.header("Public"), "SETUP") {
			t.Errorf("M2 response = %+v", resp)
			return
		}
		m3 := s.expectRequest("GET_PARAMETER")
		if m3 == nil {
			return
		}
		s.reply(m3, "", "wfd_video_formats: 00 00 01 08 000001ff 00000000 00000000 00 0000 0000 00 none none\r\n"+
			"wfd_audio_codecs: LPCM 00000002 00\r\n"+
			"wfd_client_rtp_ports: RTP/AVP/UDP;unicast 19000 0 mode=play\r\n"+
			"wfd_content_protection: none\r\n")
		req := s.expectRequest("SET_PARAMETER")
		if req == nil {
			return
		}
		m4 <- req.body
		s.reply(req, "", "")
		m5 := s.expectRequest("SET_PARAMETER")
		if m5 == nil || !strings.Contains(m5.body, "wfd_trigger_method: SETUP") {
			t.Errorf("M5 = %+v", m5)
			return
		}
		s.reply(m5, "", "")
		setup := s.request("SETUP", "rtsp://127.0.0.1/wfd1.0/streamid=0",
			"Transport: RTP/AVP/UDP;unicast;client_port=19002\r\n")
		if setup == nil || !strings.Contains(setup.header("Session"), ";timeout=30") {
			t.Errorf("M6 response = %+v", setup)
			return
		}
		session, _, _ := strings.Cut(setup.header("Session"), ";")
		if s.request("PLAY", "rtsp://127.0.0.1/wfd1.0/streamid=0", "Session: "+session+"\r\n") == nil {
			return
		}
		// An IDR request mid-session is acknowledged.
		if s.request("SET_PARAMETER", wfdURI, "", "wfd_idr_request\r\n") == nil {
			return
		}
		teardown := s.expectRequest("SET_PARAMETER")
		if teardown == nil || !strings.Contains(teardown.body, "TEARDOWN") {
			t.Errorf("source teardown trigger = %+v", teardown)
			return
		}
		s.reply(teardown, "", "")
		s.request("TEARDOWN", "rtsp://127.0.0.1/wfd1.0/streamid=0", "Session: "+session+"\r\n")
	}()

	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	control, neg, err := negotiateWFD(ctx, conn, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	if neg.Mode.Width != 1920 || neg.Mode.FPS != 30 || !neg.Audio || neg.RTPAddr.Port != 19002 ||
		!neg.RTPAddr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("negotiated %+v %v", neg, neg.RTPAddr)
	}
	body := <-m4
	for _, want := range []string{
		"wfd_video_formats: 00 00 01 04 00000080",
		"wfd_audio_codecs: LPCM 00000002 00",
		"wfd_client_rtp_ports: RTP/AVP/UDP;unicast 19000 0 mode=play",
		"wfd_presentation_URL: rtsp://127.0.0.1/wfd1.0/streamid=0 none",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("M4 body %q lacks %q", body, want)
		}
	}
	time.Sleep(100 * time.Millisecond) // let the IDR request round-trip
	control.Close()
	<-sinkDone
	select {
	case <-control.Done():
	default:
		t.Fatal("control session still open after Close")
	}
}
