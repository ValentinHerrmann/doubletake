package airplay

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var (
	holdTestSPS    = []byte{0, 0, 0, 1, 0x67, 0x42}
	holdTestBlack  = []byte{0, 0, 0, 1, 0x65, 0xbb}
	holdTestFrozen = []byte{0, 0, 0, 1, 0x65, 0xff}
)

func holdTestIDR(id byte) []byte {
	return append(append([]byte(nil), holdTestSPS...), 0, 0, 0, 1, 0x65, id)
}
func holdTestP(id byte) []byte { return []byte{0, 0, 0, 1, 0x41, id} }

// holdTestCapture is a running broadcast fed by the test, with a fake still
// encoder that records the GOP it was asked to freeze.
type holdTestCapture struct {
	broadcast *BroadcastCapture
	frames    chan VideoAccessUnit
	mu        sync.Mutex
	frozenGOP [][]byte
	freezeErr error
	lastPTS   time.Time
}

func newHoldTestCapture(t *testing.T) *holdTestCapture {
	t.Helper()
	h := &holdTestCapture{frames: make(chan VideoAccessUnit)}
	capture := &ScreenCapture{
		frames: &channelVideoAccessUnitReader{frames: h.frames},
		waitCh: make(chan struct{}),
		still:  &stillConfig{fps: 30, maxWidth: 1280, maxHeight: 720},
	}
	h.broadcast = NewBroadcastCaptureWithFrameRate(capture, 30)
	h.broadcast.stillInterval = 20 * time.Millisecond
	h.broadcast.encodeStillFn = func(_ context.Context, hold VideoHold, width, height int, gop [][]byte) ([]byte, error) {
		if width != 1280 || height != 720 {
			t.Errorf("still size = %dx%d, want 1280x720", width, height)
		}
		if hold == HoldBlack {
			return holdTestBlack, nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.frozenGOP = gop
		if h.freezeErr != nil {
			return nil, h.freezeErr
		}
		return holdTestFrozen, nil
	}
	return h
}

// start runs the broadcast. Sinks added before it see the first frame; a
// sink added later starts at a frame boundary chosen by Run.
func (h *holdTestCapture) start(t *testing.T) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.broadcast.Run() }()
	t.Cleanup(func() {
		close(h.frames)
		<-done
	})
}

// send feeds one live unit. PTS advances a second per unit, far more than
// the stills sent in between, so only the cutover rule decides delivery.
func (h *holdTestCapture) send(unit []byte) {
	if h.lastPTS.IsZero() {
		h.lastPTS = time.Now()
	}
	h.lastPTS = h.lastPTS.Add(time.Second)
	h.frames <- VideoAccessUnit{AnnexB: unit, PTS: h.lastPTS}
}

// sync waits until runFrames has processed every frame sent so far.
func (h *holdTestCapture) sync() {
	for {
		h.broadcast.holdMu.Lock()
		done := h.broadcast.lastPTS.Equal(h.lastPTS)
		h.broadcast.holdMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func readUnit(t *testing.T, sink *BroadcastSink) []byte {
	t.Helper()
	got := make(chan VideoAccessUnit, 1)
	go func() {
		frame, err := sink.ReadVideoAccessUnit()
		if err != nil {
			t.Errorf("read: %v", err)
		}
		got <- frame
	}()
	select {
	case frame := <-got:
		return frame.AnnexB
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading an access unit")
		return nil
	}
}

func expectUnit(t *testing.T, sink *BroadcastSink, want []byte) {
	t.Helper()
	if got := readUnit(t, sink); !bytes.Equal(got, want) {
		t.Fatalf("access unit = %x, want %x", got, want)
	}
}

// skipStills reads past repeated stills and returns the next other unit.
func skipStills(t *testing.T, sink *BroadcastSink, still []byte) []byte {
	t.Helper()
	for {
		got := readUnit(t, sink)
		if !bytes.Equal(got, still) {
			return got
		}
	}
}

func TestVideoHoldBlackoutRepeatsStillAndResumesAtKeyframe(t *testing.T) {
	h := newHoldTestCapture(t)
	sink := h.broadcast.AddSink()
	defer sink.Close()
	h.start(t)

	h.send(holdTestIDR(1))
	expectUnit(t, sink, holdTestIDR(1))

	if err := h.broadcast.SetVideoHold(HoldBlack); err != nil {
		t.Fatal(err)
	}
	h.send(holdTestP(2)) // dropped while held
	expectUnit(t, sink, holdTestBlack)
	expectUnit(t, sink, holdTestBlack) // repeated

	if err := h.broadcast.SetVideoHold(HoldNone); err != nil {
		t.Fatal(err)
	}
	h.send(holdTestP(3)) // references pictures the sink never got
	h.send(holdTestIDR(4))
	h.send(holdTestP(5))
	if got := skipStills(t, sink, holdTestBlack); !bytes.Equal(got, holdTestIDR(4)) {
		t.Fatalf("first live unit after release = %x, want keyframe %x", got, holdTestIDR(4))
	}
	expectUnit(t, sink, holdTestP(5))

	h.sync()
	time.Sleep(5 * h.broadcast.stillInterval)
	sink.mu.Lock()
	queued := len(sink.frameQueue)
	sink.mu.Unlock()
	if queued != 0 {
		t.Fatalf("%d units queued after live video resumed, want no further stills", queued)
	}
}

func TestVideoHoldFreezeUsesGOPSinceLastKeyframe(t *testing.T) {
	h := newHoldTestCapture(t)
	sink := h.broadcast.AddSink()
	defer sink.Close()
	h.start(t)

	for _, unit := range [][]byte{holdTestP(0), holdTestIDR(1), holdTestP(2), holdTestIDR(3), holdTestP(4)} {
		h.send(unit)
	}
	for _, want := range [][]byte{holdTestP(0), holdTestIDR(1), holdTestP(2), holdTestIDR(3), holdTestP(4)} {
		expectUnit(t, sink, want)
	}
	h.sync()

	if err := h.broadcast.SetVideoHold(HoldFreeze); err != nil {
		t.Fatal(err)
	}
	expectUnit(t, sink, holdTestFrozen)
	h.mu.Lock()
	gop := h.frozenGOP
	h.mu.Unlock()
	if len(gop) != 2 || !bytes.Equal(gop[0], holdTestIDR(3)) || !bytes.Equal(gop[1], holdTestP(4)) {
		t.Fatalf("frozen GOP = %x, want [IDR 3, P 4]", gop)
	}

	// Blackout over a freeze, then back: the same frozen picture returns.
	if err := h.broadcast.SetVideoHold(HoldBlack); err != nil {
		t.Fatal(err)
	}
	if got := skipStills(t, sink, holdTestFrozen); !bytes.Equal(got, holdTestBlack) {
		t.Fatalf("unit after blackout = %x, want black", got)
	}
	h.send(holdTestIDR(5))
	h.sync()
	if err := h.broadcast.SetVideoHold(HoldFreeze); err != nil {
		t.Fatal(err)
	}
	if got := skipStills(t, sink, holdTestBlack); !bytes.Equal(got, holdTestFrozen) {
		t.Fatalf("unit after leaving blackout = %x, want frozen picture", got)
	}
	h.mu.Lock()
	gop = h.frozenGOP
	h.mu.Unlock()
	if len(gop) != 2 {
		t.Fatalf("freeze re-encoded the picture from GOP %x instead of reusing it", gop)
	}
}

func TestVideoHoldFreezeFallsBackToBlack(t *testing.T) {
	h := newHoldTestCapture(t)
	h.freezeErr = errors.New("decoder missing")
	sink := h.broadcast.AddSink()
	defer sink.Close()
	h.start(t)

	h.send(holdTestIDR(1))
	expectUnit(t, sink, holdTestIDR(1))
	if err := h.broadcast.SetVideoHold(HoldFreeze); err != nil {
		t.Fatal(err)
	}
	expectUnit(t, sink, holdTestBlack)
}

func TestVideoHoldSinkAttachingDuringHoldGetsStill(t *testing.T) {
	h := newHoldTestCapture(t)
	h.broadcast.stillInterval = time.Hour // only the attach path can deliver it
	first := h.broadcast.AddSink()
	defer first.Close()
	h.start(t)

	h.send(holdTestIDR(1))
	expectUnit(t, first, holdTestIDR(1))
	if err := h.broadcast.SetVideoHold(HoldBlack); err != nil {
		t.Fatal(err)
	}
	expectUnit(t, first, holdTestBlack)

	late := h.broadcast.AddSink()
	defer late.Close()
	expectUnit(t, late, holdTestBlack)
}

func TestVideoHoldGOPIsBounded(t *testing.T) {
	h := newHoldTestCapture(t)
	h.start(t)
	h.send(holdTestIDR(1))
	for i := 0; i < 4*30+1; i++ {
		h.send(holdTestP(byte(i)))
	}
	h.sync()
	h.broadcast.holdMu.Lock()
	gop := h.broadcast.gop
	h.broadcast.holdMu.Unlock()
	if gop != nil {
		t.Fatalf("GOP kept %d units past its bound", len(gop))
	}
	h.send(holdTestIDR(2))
	h.sync()
	h.broadcast.holdMu.Lock()
	gop = h.broadcast.gop
	h.broadcast.holdMu.Unlock()
	if len(gop) != 1 {
		t.Fatalf("GOP after a keyframe has %d units, want 1", len(gop))
	}
}

func TestVideoHoldNeedsTimestampedCapture(t *testing.T) {
	broadcast := NewBroadcastCapture(&ScreenCapture{waitCh: make(chan struct{})})
	if err := broadcast.SetVideoHold(HoldBlack); !errors.Is(err, errVideoHoldUnsupported) {
		t.Fatalf("SetVideoHold on byte stream = %v, want %v", err, errVideoHoldUnsupported)
	}
}

func TestVideoHoldResumeKeepsSinkTimelineIncreasing(t *testing.T) {
	h := newHoldTestCapture(t)
	sink := h.broadcast.AddSink()
	defer sink.Close()
	h.start(t)

	h.send(holdTestIDR(1))
	expectUnit(t, sink, holdTestIDR(1))
	if err := h.broadcast.SetVideoHold(HoldBlack); err != nil {
		t.Fatal(err)
	}
	expectUnit(t, sink, holdTestBlack)
	if err := h.broadcast.SetVideoHold(HoldNone); err != nil {
		t.Fatal(err)
	}

	// A keyframe stamped within a frame interval of the last still would not
	// advance the sinks' timeline: live video waits for the next keyframe.
	h.broadcast.holdMu.Lock()
	h.lastPTS = h.broadcast.lastSentPTS.Add(time.Millisecond)
	h.broadcast.holdMu.Unlock()
	h.frames <- VideoAccessUnit{AnnexB: holdTestIDR(2), PTS: h.lastPTS}
	h.send(holdTestIDR(3))

	var last time.Time
	for {
		frame, err := sink.ReadVideoAccessUnit()
		if err != nil {
			t.Fatal(err)
		}
		if !frame.PTS.After(last) {
			t.Fatalf("PTS %v does not follow %v", frame.PTS, last)
		}
		last = frame.PTS
		if bytes.Equal(frame.AnnexB, holdTestBlack) {
			continue
		}
		if !bytes.Equal(frame.AnnexB, holdTestIDR(3)) {
			t.Fatalf("first live unit after release = %x, want the later keyframe %x", frame.AnnexB, holdTestIDR(3))
		}
		return
	}
}
