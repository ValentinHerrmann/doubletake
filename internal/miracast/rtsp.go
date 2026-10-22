package miracast

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	wfdURI             = "rtsp://localhost/wfd1.0"
	sessionTimeout     = 30 * time.Second
	keepaliveInterval  = 20 * time.Second
	rtspRequestTimeout = 10 * time.Second
	maxRTSPBody        = 64 << 10
)

// errTornDown means the sink ended the session (TEARDOWN or closed socket).
var errTornDown = errors.New("miracast: sink ended the session")

type rtspMessage struct {
	method  string // request method; empty for a response
	uri     string
	status  int
	headers map[string]string // lower-case names
	body    string
}

func (m *rtspMessage) header(name string) string { return m.headers[strings.ToLower(name)] }

func (m *rtspMessage) cseq() int {
	n, _ := strconv.Atoi(m.header("CSeq"))
	return n
}

func readRTSPMessage(r *bufio.Reader) (*rtspMessage, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	msg := &rtspMessage{headers: make(map[string]string)}
	fields := strings.SplitN(line, " ", 3)
	if len(fields) < 2 {
		return nil, fmt.Errorf("malformed RTSP start line %q", line)
	}
	if strings.HasPrefix(fields[0], "RTSP/") {
		if msg.status, err = strconv.Atoi(fields[1]); err != nil {
			return nil, fmt.Errorf("malformed RTSP status line %q", line)
		}
	} else {
		msg.method, msg.uri = fields[0], fields[1]
	}
	for {
		h, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		h = strings.TrimRight(h, "\r\n")
		if h == "" {
			break
		}
		name, value, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("malformed RTSP header %q", h)
		}
		msg.headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	if lengthText := msg.header("Content-Length"); lengthText != "" {
		length, err := strconv.Atoi(lengthText)
		if err != nil || length < 0 || length > maxRTSPBody {
			return nil, fmt.Errorf("invalid RTSP Content-Length %q", lengthText)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		msg.body = string(body)
	}
	return msg, nil
}

// parseParameters decodes a text/parameters body into name/value pairs.
func parseParameters(body string) map[string]string {
	params := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok {
			params[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return params
}

// Negotiated is the outcome of capability negotiation with a sink.
type Negotiated struct {
	Mode    VideoMode
	Audio   bool
	RTPAddr *net.UDPAddr // where the sink receives the MPEG-TS stream
}

// wfdControl is the source side of one WFD RTSP session (M1-M16).
type wfdControl struct {
	conn    net.Conn
	reader  *bufio.Reader
	writeMu sync.Mutex

	mu      sync.Mutex
	cseq    int
	pending map[int]chan *rtspMessage

	session string
	m2      chan struct{}
	setup   chan int // sink's RTP port from its SETUP
	play    chan struct{}
	done    chan struct{}
	errMu   sync.Mutex
	err     error
	m2Once  sync.Once
}

func newWFDControl(conn net.Conn) *wfdControl {
	var id [8]byte
	_, _ = rand.Read(id[:])
	c := &wfdControl{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		pending: make(map[int]chan *rtspMessage),
		session: strings.ToUpper(hex.EncodeToString(id[:])),
		m2:      make(chan struct{}),
		setup:   make(chan int, 1),
		play:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// negotiateWFD runs M1-M7 on an accepted sink connection: capability exchange,
// format selection and the sink's SETUP and PLAY.
func negotiateWFD(ctx context.Context, conn net.Conn, maxFPS int, wantAudio bool) (*wfdControl, Negotiated, error) {
	c := newWFDControl(conn)
	neg, err := c.negotiate(ctx, maxFPS, wantAudio)
	if err != nil {
		c.Close()
		return nil, Negotiated{}, err
	}
	go c.keepaliveLoop()
	return c, neg, nil
}

func (c *wfdControl) negotiate(ctx context.Context, maxFPS int, wantAudio bool) (Negotiated, error) {
	var neg Negotiated
	// M1: the source opens with OPTIONS.
	if _, err := c.request(ctx, "OPTIONS", "*", []string{"Require: org.wfa.wfd1.0"}, ""); err != nil {
		return neg, fmt.Errorf("M1 OPTIONS: %w", err)
	}
	// M2: the sink's OPTIONS. Some sinks skip it; continue after a short wait.
	select {
	case <-c.m2:
	case <-time.After(3 * time.Second):
	case <-c.done:
		return neg, c.failure()
	case <-ctx.Done():
		return neg, ctx.Err()
	}

	// M3: query sink capabilities.
	resp, err := c.request(ctx, "GET_PARAMETER", wfdURI, []string{"Content-Type: text/parameters"},
		"wfd_video_formats\r\nwfd_audio_codecs\r\nwfd_client_rtp_ports\r\nwfd_content_protection\r\n")
	if err != nil {
		return neg, fmt.Errorf("M3 GET_PARAMETER: %w", err)
	}
	caps, err := ParseSinkCapabilities(parseParameters(resp.body))
	if err != nil {
		return neg, fmt.Errorf("M3 GET_PARAMETER: %w", err)
	}
	if caps.HDCPRequired {
		// Most sinks accept unprotected content when no protection is
		// selected in M4. Those that do not will reject M4 or stay blank.
		log.Printf("[MIRACAST] sink advertises HDCP; sending unprotected content")
	}
	if neg.Mode, err = caps.SelectVideoMode(maxFPS); err != nil {
		return neg, err
	}
	neg.Audio = wantAudio && caps.HasLPCM48k()
	dbg("[MIRACAST] sink caps %+v; selected %dx%dp%d audio=%v", caps, neg.Mode.Width, neg.Mode.Height, neg.Mode.FPS, neg.Audio)

	// M4: select the formats.
	localIP := c.conn.LocalAddr().(*net.TCPAddr).IP
	params := []string{
		"wfd_video_formats: " + VideoFormatsParam(neg.Mode),
		fmt.Sprintf("wfd_presentation_URL: rtsp://%s/wfd1.0/streamid=0 none", localIP),
		fmt.Sprintf("wfd_client_rtp_ports: %s %d 0 mode=play", caps.RTPProfile, caps.RTPPort),
	}
	if neg.Audio {
		params = append(params, "wfd_audio_codecs: "+AudioCodecsParam())
	}
	if _, err := c.request(ctx, "SET_PARAMETER", wfdURI, []string{"Content-Type: text/parameters"},
		strings.Join(params, "\r\n")+"\r\n"); err != nil {
		return neg, fmt.Errorf("M4 SET_PARAMETER: %w", err)
	}

	// M5: ask the sink to SETUP, then wait for M6 SETUP and M7 PLAY.
	if err := c.trigger(ctx, "SETUP"); err != nil {
		return neg, err
	}
	timeout := time.NewTimer(rtspRequestTimeout)
	defer timeout.Stop()
	rtpPort := caps.RTPPort
	select {
	case port := <-c.setup:
		if port > 0 {
			rtpPort = port
		}
	case <-c.done:
		return neg, fmt.Errorf("waiting for sink SETUP: %w", c.failure())
	case <-ctx.Done():
		return neg, ctx.Err()
	case <-timeout.C:
		return neg, fmt.Errorf("sink sent no SETUP within %s", rtspRequestTimeout)
	}
	select {
	case <-c.play:
	case <-c.done:
		return neg, fmt.Errorf("waiting for sink PLAY: %w", c.failure())
	case <-ctx.Done():
		return neg, ctx.Err()
	case <-timeout.C:
		return neg, fmt.Errorf("sink sent no PLAY within %s", rtspRequestTimeout)
	}
	sinkIP := c.conn.RemoteAddr().(*net.TCPAddr).IP
	neg.RTPAddr = &net.UDPAddr{IP: sinkIP, Port: rtpPort}
	return neg, nil
}

// trigger sends an M5 SET_PARAMETER wfd_trigger_method.
func (c *wfdControl) trigger(ctx context.Context, method string) error {
	_, err := c.request(ctx, "SET_PARAMETER", wfdURI, []string{"Content-Type: text/parameters"},
		"wfd_trigger_method: "+method+"\r\n")
	if err != nil {
		return fmt.Errorf("M5 trigger %s: %w", method, err)
	}
	return nil
}

func (c *wfdControl) keepaliveLoop() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			// M16: keep the session alive.
			ctx, cancel := context.WithTimeout(context.Background(), rtspRequestTimeout)
			_, err := c.request(ctx, "GET_PARAMETER", wfdURI, []string{"Session: " + c.session}, "")
			cancel()
			if err != nil {
				c.fail(fmt.Errorf("M16 keepalive: %w", err))
				return
			}
		}
	}
}

// request sends a source-initiated request and waits for its response.
func (c *wfdControl) request(ctx context.Context, method, uri string, headers []string, body string) (*rtspMessage, error) {
	c.mu.Lock()
	c.cseq++
	cseq := c.cseq
	ch := make(chan *rtspMessage, 1)
	c.pending[cseq] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, cseq)
		c.mu.Unlock()
	}()

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\n", method, uri, cseq)
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	if body != "" {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	}
	b.WriteString("\r\n" + body)
	if err := c.send(b.String()); err != nil {
		return nil, err
	}

	timer := time.NewTimer(rtspRequestTimeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.status != 200 {
			return resp, fmt.Errorf("sink answered %d", resp.status)
		}
		return resp, nil
	case <-c.done:
		return nil, c.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("no response within %s", rtspRequestTimeout)
	}
}

func (c *wfdControl) send(text string) error {
	dbg("[MIRACAST] >>> %s", strings.TrimSpace(text))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(rtspRequestTimeout))
	_, err := io.WriteString(c.conn, text)
	return err
}

func (c *wfdControl) respond(req *rtspMessage, headers ...string) {
	var b strings.Builder
	fmt.Fprintf(&b, "RTSP/1.0 200 OK\r\nCSeq: %d\r\n", req.cseq())
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	if err := c.send(b.String()); err != nil {
		c.fail(err)
	}
}

func (c *wfdControl) readLoop() {
	for {
		msg, err := readRTSPMessage(c.reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				err = errTornDown
			}
			c.fail(err)
			return
		}
		dbg("[MIRACAST] <<< %s%d %s %v %q", msg.method, msg.status, msg.uri, msg.headers, msg.body)
		if msg.method == "" {
			c.mu.Lock()
			ch := c.pending[msg.cseq()]
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		c.handleRequest(msg)
	}
}

// handleRequest answers sink-initiated requests (M2, M6, M7, M8, M13, ...).
func (c *wfdControl) handleRequest(req *rtspMessage) {
	switch req.method {
	case "OPTIONS":
		c.respond(req, "Public: org.wfa.wfd1.0, SETUP, TEARDOWN, PLAY, PAUSE, GET_PARAMETER, SET_PARAMETER")
		c.m2Once.Do(func() { close(c.m2) })
	case "SETUP":
		transport := req.header("Transport")
		c.respond(req,
			fmt.Sprintf("Session: %s;timeout=%d", c.session, int(sessionTimeout/time.Second)),
			"Transport: "+transport)
		select {
		case c.setup <- clientPort(transport):
		default:
		}
	case "PLAY":
		c.respond(req, "Session: "+c.session, "Range: npt=now-")
		select {
		case c.play <- struct{}{}:
		default:
		}
	case "TEARDOWN":
		c.respond(req, "Session: "+c.session)
		c.fail(errTornDown)
	default:
		// GET_PARAMETER keepalives, SET_PARAMETER wfd_idr_request (the 2 s GOP
		// supplies the next IDR) and PAUSE are acknowledged.
		c.respond(req)
	}
}

// clientPort extracts client_port from an RTSP Transport header.
func clientPort(transport string) int {
	for _, part := range strings.Split(transport, ";") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(part), "client_port="); ok {
			first, _, _ := strings.Cut(value, "-")
			port, _ := strconv.Atoi(first)
			return port
		}
	}
	return 0
}

func (c *wfdControl) fail(err error) {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	_ = c.conn.Close()
}

func (c *wfdControl) failure() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Done is closed when the RTSP session ends; Err then reports why.
func (c *wfdControl) Done() <-chan struct{} { return c.done }

// Close asks the sink to tear down (M5 TEARDOWN), then closes the connection.
func (c *wfdControl) Close() {
	select {
	case <-c.done:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := c.trigger(ctx, "TEARDOWN"); err == nil {
		select {
		case <-c.done:
		case <-ctx.Done():
		}
	}
	cancel()
	c.fail(errTornDown)
}
