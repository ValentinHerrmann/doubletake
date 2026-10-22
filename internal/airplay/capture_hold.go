package airplay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// VideoHold replaces live video with a still picture without stopping the
// stream. Receivers keep decoding: the still is an ordinary keyframe that is
// repeated, so neither AirPlay nor Miracast sinks see a gap in video.
type VideoHold uint8

const (
	HoldNone   VideoHold = iota // live video
	HoldFreeze                  // the last live picture
	HoldBlack                   // a black picture
)

func (h VideoHold) String() string {
	switch h {
	case HoldFreeze:
		return "freeze"
	case HoldBlack:
		return "blackout"
	default:
		return "live"
	}
}

const (
	// stillRepeatInterval keeps sinks that need a steady video stream, and
	// sinks that attach during a hold, supplied with the still picture.
	stillRepeatInterval = time.Second

	// A freeze decodes the access units since the last keyframe. Two keyframe
	// intervals of frames or 64 MiB bound that buffer if keyframes go missing.
	maxHoldGOPBytes = 64 << 20
)

var errVideoHoldUnsupported = errors.New("freeze and blackout need timestamped video capture")

// stillJob is one hold transition to encode and repeat.
type stillJob struct {
	gen           uint64
	hold          VideoHold
	width, height int
	gop           [][]byte
}

// recordLiveFrameLocked tracks the stream state a still needs and reports
// whether a live access unit may reach the sinks. Must hold bc.holdMu.
func (bc *BroadcastCapture) recordLiveFrameLocked(frame VideoAccessUnit) bool {
	keyframe := AccessUnitHasKeyframe(frame.AnnexB, bc.codec)
	bc.lastPTS = frame.PTS
	bc.lastArrival = time.Now()
	if keyframe {
		// Access units are immutable, so a new slice keeps earlier snapshots valid.
		bc.gop = [][]byte{frame.AnnexB}
		bc.gopBytes = len(frame.AnnexB)
		if w, h, ok := accessUnitDimensions(frame.AnnexB, bc.codec); ok {
			bc.width, bc.height = w, h
		}
	} else if bc.gop != nil {
		bc.gop = append(bc.gop, frame.AnnexB)
		bc.gopBytes += len(frame.AnnexB)
		if bc.gopBytes > maxHoldGOPBytes || len(bc.gop) > 4*bc.fps() {
			bc.gop, bc.gopBytes = nil, 0
		}
	}

	switch {
	case bc.hold != HoldNone:
		return false
	case bc.awaitKeyframe:
		// Live P-frames reference pictures the sinks never decoded: resume only
		// at a keyframe, and stop the still first so none follows it. A
		// keyframe stamped too close to the last still would leave the sinks'
		// timeline without a frame interval, so it waits for the next one.
		if !keyframe || frame.PTS.Before(bc.lastSentPTS.Add(bc.frameDuration)) {
			return false
		}
		bc.awaitKeyframe = false
		bc.stillGen++
		bc.stillUnit = nil
	}
	if frame.PTS.After(bc.lastSentPTS) {
		bc.lastSentPTS = frame.PTS
	}
	return true
}

func (bc *BroadcastCapture) fps() int {
	if bc.still != nil && bc.still.fps > 0 {
		return bc.still.fps
	}
	return int(time.Second / bc.frameDuration)
}

// SetVideoHold switches every sink of this capture between live video, a
// frozen picture and a black picture. Releasing a hold resumes live video at
// the encoder's next keyframe; the still stays on screen until then.
func (bc *BroadcastCapture) SetVideoHold(hold VideoHold) error {
	if !bc.frames || (bc.still == nil && bc.encodeStillFn == nil) {
		return errVideoHoldUnsupported
	}
	bc.holdMu.Lock()
	defer bc.holdMu.Unlock()
	if hold == bc.hold {
		return nil
	}
	previous := bc.hold
	bc.hold = hold
	if hold == HoldNone {
		bc.awaitKeyframe = true
		bc.frozenUnit = nil
		return nil
	}
	bc.awaitKeyframe = false
	if hold == HoldFreeze && previous == HoldNone {
		bc.frozenUnit = nil
	}
	width, height := bc.width, bc.height
	if bc.still != nil {
		if w, h, ok := bc.still.outputSize(); ok {
			width, height = w, h
		}
	}
	bc.stillGen++
	job := stillJob{
		gen:    bc.stillGen,
		hold:   hold,
		width:  width,
		height: height,
		gop:    append([][]byte(nil), bc.gop...),
	}
	go bc.runStill(job)
	return nil
}

// runStill encodes the still for job, then sends it to every sink at once and
// every stillRepeatInterval until another transition or the live cutover.
func (bc *BroadcastCapture) runStill(job stillJob) {
	unit, err := bc.stillUnitFor(job)
	if err != nil && job.hold == HoldFreeze {
		log.Printf("[capture] freeze picture unavailable (%v); showing black instead", err)
		unit, err = bc.stillUnitFor(stillJob{gen: job.gen, hold: HoldBlack, width: job.width, height: job.height})
	}
	if err != nil {
		log.Printf("[capture] %s picture unavailable: %v", job.hold, err)
		return
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-bc.done:
			return
		case <-timer.C:
		}
		bc.holdMu.Lock()
		if bc.stillGen != job.gen {
			bc.holdMu.Unlock()
			return
		}
		bc.stillUnit = unit
		bc.sendStillLocked(unit)
		bc.holdMu.Unlock()
		timer.Reset(bc.stillInterval)
	}
}

// stillUnitFor returns the cached still for job or encodes a new one.
func (bc *BroadcastCapture) stillUnitFor(job stillJob) ([]byte, error) {
	bc.holdMu.Lock()
	switch {
	case job.hold == HoldBlack && bc.blackUnit != nil && bc.blackWidth == job.width && bc.blackHeight == job.height:
		unit := bc.blackUnit
		bc.holdMu.Unlock()
		return unit, nil
	case job.hold == HoldFreeze && bc.frozenUnit != nil:
		unit := bc.frozenUnit
		bc.holdMu.Unlock()
		return unit, nil
	}
	bc.holdMu.Unlock()

	if job.width <= 0 || job.height <= 0 {
		return nil, errors.New("video size is not known yet")
	}
	encode := bc.encodeStillFn
	if encode == nil {
		encode = bc.encodeStillWithCapture
	}
	unit, err := encode(bc.stillCtx, job.hold, job.width, job.height, job.gop)
	if err != nil {
		return nil, err
	}

	bc.holdMu.Lock()
	defer bc.holdMu.Unlock()
	if job.hold == HoldBlack {
		bc.blackUnit, bc.blackWidth, bc.blackHeight = unit, job.width, job.height
	} else if bc.stillGen == job.gen {
		bc.frozenUnit = unit
	}
	return unit, nil
}

func (bc *BroadcastCapture) encodeStillWithCapture(ctx context.Context, hold VideoHold, width, height int, gop [][]byte) ([]byte, error) {
	switch hold {
	case HoldBlack:
		return encodeBlackStill(ctx, *bc.still, width, height)
	case HoldFreeze:
		return encodeFrozenStill(ctx, *bc.still, width, height, gop)
	default:
		return nil, fmt.Errorf("no still for %s", hold)
	}
}

// stillPTSLocked stamps a still on the capture clock: the capture time a live
// frame arriving now would carry, and at least one frame interval after the
// last unit sent. Must hold bc.holdMu.
func (bc *BroadcastCapture) stillPTSLocked() time.Time {
	pts := time.Now()
	if !bc.lastPTS.IsZero() {
		pts = bc.lastPTS.Add(time.Since(bc.lastArrival))
	}
	if floor := bc.lastSentPTS.Add(bc.frameDuration); !bc.lastSentPTS.IsZero() && pts.Before(floor) {
		pts = floor
	}
	bc.lastSentPTS = pts
	return pts
}

// sendStillLocked queues unit on every sink. Must hold bc.holdMu.
func (bc *BroadcastCapture) sendStillLocked(unit []byte) {
	frame := VideoAccessUnit{AnnexB: unit, PTS: bc.stillPTSLocked()}

	bc.mu.Lock()
	sinks := append([]*BroadcastSink(nil), bc.sinks...)
	bc.mu.Unlock()
	for _, sink := range sinks {
		if err := sink.enqueueFrame(frame); err != nil {
			bc.RemoveSink(sink)
		}
	}
}
