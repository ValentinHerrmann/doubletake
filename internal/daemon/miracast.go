package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"doubletake/internal/airplay"
	"doubletake/internal/miracast"
)

const (
	miracastTargetPrefix = "p2p:"
	miracastProtocol     = "miracast"
	airplayProtocol      = "airplay"

	// miracastSearchDuration is one search window started by "discover".
	miracastSearchDuration = 20 * time.Second
	// miracastPeerTTL keeps displays listed between searches.
	miracastPeerTTL = 2 * time.Minute
	// miracastPeerWait bounds the search for the chosen display before connecting.
	miracastPeerWait = 30 * time.Second
	// p2pRetryInterval spaces out attempts to open a missing P2P device, which
	// can appear later: Wi-Fi switched on, or the card back after resume.
	p2pRetryInterval = 30 * time.Second
)

func isMiracastTarget(target string) bool {
	return strings.HasPrefix(target, miracastTargetPrefix)
}

func streamProtocol(target string) string {
	if isMiracastTarget(target) {
		return miracastProtocol
	}
	return airplayProtocol
}

// p2pLocked opens the Wi-Fi P2P device on first use. A failed open is retried
// after p2pRetryInterval, not on every poll. Must be called with d.mu held.
func (d *Daemon) p2pLocked() (*miracast.P2P, error) {
	if d.p2p == nil && time.Since(d.p2pOpenedAt) >= p2pRetryInterval {
		d.p2pOpenedAt = time.Now()
		d.p2p, d.p2pErr = miracast.OpenP2P()
		if d.p2pErr != nil {
			log.Printf("[daemon] Miracast unavailable: %v", d.p2pErr)
		}
	}
	if d.p2p == nil {
		return nil, d.p2pErr
	}
	return d.p2p, nil
}

func (d *Daemon) closeP2P() {
	d.mu.Lock()
	p2p := d.p2p
	d.p2p = nil
	if d.p2pSearchCancel != nil {
		d.p2pSearchCancel()
	}
	d.mu.Unlock()
	if p2p != nil {
		_ = p2p.Close()
	}
}

// startMiracastSearch runs one background search for Miracast displays unless
// one is running or a Miracast stream would be disturbed by it.
func (d *Daemon) startMiracastSearch() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shuttingDown || d.p2pSearching || d.miracastStreamLocked() != nil {
		return
	}
	p2p, err := d.p2pLocked()
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.p2pSearching = true
	d.p2pSearchCancel = cancel
	d.streamWorkers.Add(1)
	go func() {
		defer d.streamWorkers.Done()
		peers, err := p2p.Find(ctx, miracastSearchDuration, func(found []miracast.Peer) {
			d.mu.Lock()
			d.miracastPeers = found
			d.miracastPeersAt = time.Now()
			d.mu.Unlock()
		})
		d.mu.Lock()
		defer d.mu.Unlock()
		cancel()
		d.p2pSearching = false
		d.p2pSearchCancel = nil
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[daemon] Miracast search failed: %v", err)
			}
			return
		}
		d.miracastPeers = peers
		d.miracastPeersAt = time.Now()
	}()
}

// miracastDeviceInfosLocked lists recently found displays. Must be called with d.mu held.
func (d *Daemon) miracastDeviceInfosLocked() []DeviceInfo {
	if time.Since(d.miracastPeersAt) > miracastPeerTTL {
		return nil
	}
	infos := make([]DeviceInfo, 0, len(d.miracastPeers))
	for _, peer := range d.miracastPeers {
		infos = append(infos, DeviceInfo{
			Name:     peer.Name,
			Model:    "Miracast",
			IP:       miracastTargetPrefix + strings.ToUpper(peer.HwAddress),
			DeviceID: peer.HwAddress,
			Protocol: miracastProtocol,
		})
	}
	return infos
}

// miracastStreamLocked returns the active Miracast stream: the single P2P
// device carries at most one group. Must be called with d.mu held.
func (d *Daemon) miracastStreamLocked() *activeStream {
	for target, entry := range d.streams {
		if isMiracastTarget(target) {
			return entry
		}
	}
	return nil
}

// connectMiracastLocked publishes a Miracast stream entry and starts its
// worker. It is called from handleConnect with d.mu held and releases it.
func (d *Daemon) connectMiracastLocked(target string, sourceTypes uint32) Response {
	if _, err := d.p2pLocked(); err != nil {
		state := d.overallStateLocked()
		d.mu.Unlock()
		return Response{OK: false, State: state, Error: err.Error()}
	}
	if existing := d.miracastStreamLocked(); existing != nil {
		state := d.overallStateLocked()
		d.mu.Unlock()
		return Response{OK: false, State: state, Error: "already casting to a Miracast display (" + existing.device + ")"}
	}
	name := strings.TrimPrefix(target, miracastTargetPrefix)
	for _, peer := range d.miracastPeers {
		if strings.EqualFold(peer.HwAddress, name) {
			name = peer.Name
		}
	}
	// A search ending mid-connection would stop P2P discovery during group
	// formation; WaitForPeer starts its own.
	if d.p2pSearchCancel != nil {
		d.p2pSearchCancel()
	}
	connCtx, cancel := context.WithCancel(context.Background())
	entry := &activeStream{
		device:      name,
		deviceIP:    target,
		deviceID:    target,
		state:       StateConnecting,
		cancelFn:    cancel,
		sourceTypes: sourceTypes,
		h264Profile: miracast.H264Profile,
	}
	d.clearLastErrorForTargetLocked(target)
	d.streams[target] = entry
	d.streamWorkers.Add(1)
	state := d.overallStateLocked()
	d.mu.Unlock()

	go func() {
		defer d.streamWorkers.Done()
		d.connectAndStreamMiracast(connCtx, entry, target)
	}()
	return Response{OK: true, State: state, Device: target}
}

// connectAndStreamMiracast mirrors to one Miracast display: screen-sharing
// dialog first, then the Wi-Fi Direct group and WFD negotiation, then the
// encoder at the negotiated mode, shared with other receivers using it.
func (d *Daemon) connectAndStreamMiracast(ctx context.Context, entry *activeStream, target string) {
	hwAddress := strings.TrimPrefix(target, miracastTargetPrefix)
	fail := func(msg string) {
		d.mu.Lock()
		if d.streams[target] != entry {
			d.mu.Unlock()
			return
		}
		d.lastError = fmt.Sprintf("%s: %s", entry.device, msg)
		d.lastErrorTarget = target
		cleanup := d.detachStreamLocked(target)
		d.mu.Unlock()
		log.Printf("[daemon] %s", d.lastErrorFor(target))
		cleanup.run()
	}

	restoreToken := entry.rememberedScreen(d.credStore.Lookup(target))
	preparation, err := d.prepareVideoCapture(ctx, entry.sourceTypes, restoreToken, target, miracast.H264Profile, airplay.VideoCodecH264)
	if err != nil {
		fail(fmt.Sprintf("prepare capture failed: %v", err))
		return
	}
	defer preparation.Close()

	d.mu.Lock()
	p2p := d.p2p
	d.mu.Unlock()
	if p2p == nil {
		fail(miracast.ErrNoP2PDevice.Error())
		return
	}
	if err := p2p.WaitForPeer(ctx, hwAddress, miracastPeerWait); err != nil {
		fail(err.Error())
		return
	}
	session, err := miracast.Start(ctx, p2p, hwAddress, d.cfg.FPS, !d.cfg.NoAudio)
	if err != nil {
		if ctx.Err() == nil {
			fail(err.Error())
		}
		return
	}
	mode := session.Negotiated.Mode
	broadcast, _, err := d.getOrStartPreparedCaptureGroup(ctx, entry, preparation, mode.Width, mode.Height, airplay.VideoCodecH264)
	if err != nil {
		_ = session.Close()
		if !errors.Is(err, context.Canceled) {
			fail(fmt.Sprintf("start capture failed: %v", err))
		}
		return
	}

	d.mu.Lock()
	if d.streams[target] != entry {
		d.mu.Unlock()
		_ = session.Close()
		d.mu.Lock()
		cleanup := d.detachCaptureGroupIfUnusedLocked(entry.captureGroup)
		d.mu.Unlock()
		cleanup.run()
		return
	}
	// Miracast owns its Constrained Baseline encoder, so it can push back on
	// it: during Wi-Fi channel switching the encoder's leaky input queue skips
	// frames, instead of a shared fan-out queue filling up and dropping the
	// display.
	sink, err := broadcast.AddBackpressuredSink()
	if err != nil {
		d.mu.Unlock()
		_ = session.Close()
		fail(fmt.Sprintf("attach to capture: %v", err))
		return
	}
	entry.state = StateStreaming
	entry.wfd = session
	entry.sink = sink
	entry.audioMuted = false
	d.mu.Unlock()
	log.Printf("[daemon] streaming to %s (%s) at %dx%dp%d over Miracast", entry.device, target, mode.Width, mode.Height, mode.FPS)

	var audio *airplay.AudioCapture
	if session.HasAudio() {
		audio, err = airplay.StartAudioCapture(ctx, d.cfg.TestMode, airplay.AudioCodecPCM48)
		if err != nil {
			log.Printf("[daemon] audio capture failed: %v (continuing without audio)", err)
			audio = nil
		}
	}
	streamErr := session.Stream(ctx, sink, audio)
	if audio != nil {
		audio.Stop()
	}
	if streamErr != nil && ctx.Err() == nil {
		fail(streamErr.Error())
		return
	}

	d.mu.Lock()
	cleanup := daemonCleanup{}
	if d.streams[target] == entry {
		cleanup = d.detachStreamLocked(target)
	}
	d.mu.Unlock()
	cleanup.run()
	log.Printf("[daemon] stream ended for %s", target)
}

func (d *Daemon) lastErrorFor(target string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastErrorTarget == target {
		return d.lastError
	}
	return target + ": stream failed"
}
