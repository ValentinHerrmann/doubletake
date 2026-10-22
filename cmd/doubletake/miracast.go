package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"doubletake/internal/airplay"
	"doubletake/internal/miracast"
)

const miracastFindDuration = 15 * time.Second

// runMiracast is the experimental Miracast path: "list" searches for displays,
// anything else is the P2P device address of the display to mirror to.
func runMiracast(ctx context.Context, target string, captureCfg airplay.CaptureConfig, testMode, noAudio bool) error {
	p2p, err := miracast.OpenP2P()
	if err != nil {
		return err
	}
	defer p2p.Close()

	if target == "list" {
		log.Printf("searching for Miracast displays for %s (put the display in screen-mirroring mode)", miracastFindDuration)
		peers, err := p2p.Find(ctx, miracastFindDuration, nil)
		if err != nil {
			return err
		}
		if len(peers) == 0 {
			fmt.Println("no Miracast displays found")
			return nil
		}
		for _, peer := range peers {
			fmt.Printf("%s  %-30s signal %d%%\n", peer.HwAddress, peer.Name, peer.Strength)
		}
		return nil
	}

	// Complete the screen-sharing dialog before the display is waiting on us;
	// sinks give up if media does not follow PLAY promptly.
	captureCfg.VideoCodec = airplay.VideoCodecH264
	captureCfg.H264Profile = miracast.H264Profile
	var preparation *airplay.CapturePreparation
	if testMode {
		preparation, err = airplay.PrepareTestCapture(ctx, captureCfg)
	} else {
		preparation, err = airplay.PrepareCapture(ctx, captureCfg)
	}
	if err != nil {
		return fmt.Errorf("prepare screen capture: %w", err)
	}
	defer preparation.Close()

	// A peer is only known to NetworkManager after a search has seen it.
	if err := p2p.WaitForPeer(ctx, target, 30*time.Second); err != nil {
		return err
	}
	session, err := miracast.Start(ctx, p2p, target, captureCfg.FPS, !noAudio)
	if err != nil {
		return err
	}
	defer session.Close()

	mode := session.Negotiated.Mode
	capture, err := preparation.Start(mode.Width, mode.Height)
	if err != nil {
		return fmt.Errorf("start screen capture: %w", err)
	}
	defer capture.Stop()

	var audio *airplay.AudioCapture
	if session.HasAudio() {
		audio, err = airplay.StartAudioCapture(ctx, testMode, airplay.AudioCodecPCM48)
		if err != nil {
			log.Printf("warning: audio capture failed: %v (continuing without audio)", err)
			audio = nil
		} else {
			defer audio.Stop()
		}
	}
	err = session.Stream(ctx, capture, audio)
	if ctx.Err() != nil {
		return nil
	}
	return err
}
