package airplay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// While casting, audio is routed like a plugged-in HDMI display: a virtual
// "Doubletake" output becomes the system default, the local speakers go
// quiet, and the system volume fader and mute control what receivers play.
// A sink monitor taps the mix before the sink's volume, so the capture
// applies that volume itself.

const (
	audioRouteSinkName = "doubletake"
	// paVolumeNorm is PulseAudio's 100 % volume (PA_VOLUME_NORM).
	paVolumeNorm = 65536
)

// pactlRunner runs one pactl command and returns its stdout.
type pactlRunner func(args ...string) (string, error)

func runPactl(args ...string) (string, error) {
	cmd := exec.Command("pactl", args...)
	// Untranslated output: some locales print "-8,56 dB".
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("pactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("pactl %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// audioRouteState survives a crash so the next cast can restore the output
// the user had before.
type audioRouteState struct {
	Module          string `json:"module"`
	PreviousDefault string `json:"previous_default"`
}

// audioRoute owns the virtual output. It is shared by every capture and
// reference counted: the first capture creates it, the last one restores the
// previous output.
type audioRoute struct {
	run       pactlRunner
	statePath string
	// subscribe streams `pactl subscribe` lines until ctx ends; nil in tests.
	subscribe func(ctx context.Context, lines chan<- string) error

	mu       sync.Mutex
	refs     int
	state    audioRouteState
	cancel   context.CancelFunc
	watching sync.WaitGroup

	// gain holds each channel's linear gain as float32 bits.
	gain [audioChannels]atomic.Uint32
}

var defaultAudioRoute = newAudioRoute(runPactl, audioRouteStatePath(), subscribePactl)

func newAudioRoute(run pactlRunner, statePath string, subscribe func(context.Context, chan<- string) error) *audioRoute {
	r := &audioRoute{run: run, statePath: statePath, subscribe: subscribe}
	r.setGain([audioChannels]float32{1, 1})
	return r
}

func audioRouteStatePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "doubletake-audio.json")
}

// acquire makes the virtual output the default and returns the monitor
// source to capture.
func (r *audioRoute) acquire() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refs > 0 {
		r.refs++
		return audioRouteSinkName + ".monitor", nil
	}
	if err := r.setUpLocked(); err != nil {
		return "", err
	}
	r.refs = 1
	if r.subscribe != nil {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		r.watching.Add(1)
		go func() {
			defer r.watching.Done()
			r.watch(ctx)
		}()
	}
	return audioRouteSinkName + ".monitor", nil
}

func (r *audioRoute) setUpLocked() error {
	saved := r.loadState()
	current, err := r.run("get-default-sink")
	if err != nil {
		return err
	}
	current = strings.TrimSpace(current)
	previous := current
	if current == audioRouteSinkName || current == "" {
		// Left over from a crash: the saved output is the user's.
		previous = saved.PreviousDefault
	}

	module, exists, err := r.findSinkModule()
	if err != nil {
		return err
	}
	if !exists {
		out, err := r.run("load-module", "module-null-sink",
			"sink_name="+audioRouteSinkName,
			"sink_properties=device.description=Doubletake",
			"rate=48000", "channels=2")
		if err != nil {
			return err
		}
		module = strings.TrimSpace(out)
	}
	r.state = audioRouteState{Module: module, PreviousDefault: previous}
	r.saveState()

	if previous != "" && previous != audioRouteSinkName {
		// Start where the fader was, not at 100 %.
		if out, err := r.run("get-sink-volume", previous); err == nil {
			if volumes, ok := parsePactlVolumes(out); ok {
				args := []string{"set-sink-volume", audioRouteSinkName}
				for _, v := range volumes {
					args = append(args, strconv.Itoa(v))
				}
				_, _ = r.run(args...)
			}
		}
		if out, err := r.run("get-sink-mute", previous); err == nil {
			_, _ = r.run("set-sink-mute", audioRouteSinkName, strconv.FormatBool(parsePactlMute(out)))
		}
	}
	if _, err := r.run("set-default-sink", audioRouteSinkName); err != nil {
		r.tearDownLocked()
		return err
	}
	if previous != "" {
		r.moveInputs(previous, audioRouteSinkName)
	}
	r.refreshGain()
	return nil
}

// release drops one reference; the last restores the previous output.
func (r *audioRoute) release() {
	r.mu.Lock()
	if r.refs == 0 {
		r.mu.Unlock()
		return
	}
	r.refs--
	if r.refs > 0 {
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	r.cancel = nil
	r.tearDownLocked()
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		r.watching.Wait()
	}
	r.setGain([audioChannels]float32{1, 1})
}

func (r *audioRoute) tearDownLocked() {
	previous := r.state.PreviousDefault
	current, err := r.run("get-default-sink")
	// A user who picked another output mid-cast keeps it.
	if err == nil && strings.TrimSpace(current) == audioRouteSinkName && previous != "" && r.sinkExists(previous) {
		if _, err := r.run("set-default-sink", previous); err != nil {
			log.Printf("[AUDIO] restore default output %s: %v", previous, err)
		}
		r.moveInputs(audioRouteSinkName, previous)
	}
	if r.state.Module != "" {
		if _, err := r.run("unload-module", r.state.Module); err != nil {
			log.Printf("[AUDIO] remove Doubletake output: %v", err)
		}
	}
	r.state = audioRouteState{}
	_ = os.Remove(r.statePath)
}

// findSinkModule returns the module that owns an existing doubletake sink.
func (r *audioRoute) findSinkModule() (module string, exists bool, err error) {
	out, err := r.run("list", "short", "modules")
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "module-null-sink" {
			continue
		}
		for _, arg := range fields[2:] {
			if arg == "sink_name="+audioRouteSinkName {
				return fields[0], true, nil
			}
		}
	}
	return "", false, nil
}

func (r *audioRoute) sinkExists(name string) bool {
	out, err := r.run("list", "short", "sinks")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

// moveInputs moves every playing stream on sink from to sink to.
func (r *audioRoute) moveInputs(from, to string) {
	sinks, err := r.run("list", "short", "sinks")
	if err != nil {
		return
	}
	fromIndex := ""
	for _, line := range strings.Split(sinks, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == from {
			fromIndex = fields[0]
		}
	}
	if fromIndex == "" {
		return
	}
	inputs, err := r.run("list", "short", "sink-inputs")
	if err != nil {
		return
	}
	for _, line := range strings.Split(inputs, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == fromIndex {
			_, _ = r.run("move-sink-input", fields[0], to)
		}
	}
}

func (r *audioRoute) loadState() audioRouteState {
	var s audioRouteState
	if data, err := os.ReadFile(r.statePath); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func (r *audioRoute) saveState() {
	if data, err := json.Marshal(r.state); err == nil {
		_ = os.WriteFile(r.statePath, data, 0o600)
	}
}

// watch follows sink changes so fader and mute apply within a frame.
func (r *audioRoute) watch(ctx context.Context) {
	lines := make(chan string, 16)
	go func() {
		if err := r.subscribe(ctx, lines); err != nil && ctx.Err() == nil {
			log.Printf("[AUDIO] volume watch stopped: %v", err)
		}
		close(lines)
	}()
	for line := range lines {
		if strings.Contains(line, "on sink #") {
			r.refreshGain()
		}
	}
}

func subscribePactl(ctx context.Context, lines chan<- string) error {
	cmd := exec.CommandContext(ctx, "pactl", "subscribe")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		select {
		case lines <- scanner.Text():
		case <-ctx.Done():
		}
	}
	return cmd.Wait()
}

// refreshGain reads the virtual output's volume and mute into the gain.
func (r *audioRoute) refreshGain() {
	out, err := r.run("get-sink-volume", audioRouteSinkName)
	if err != nil {
		return
	}
	volumes, ok := parsePactlVolumes(out)
	if !ok {
		return
	}
	var gain [audioChannels]float32
	muted := false
	if mute, err := r.run("get-sink-mute", audioRouteSinkName); err == nil {
		muted = parsePactlMute(mute)
	}
	for ch := range gain {
		v := volumes[0]
		if ch < len(volumes) {
			v = volumes[ch]
		}
		if !muted {
			gain[ch] = paVolumeToLinear(v)
		}
	}
	r.setGain(gain)
}

func (r *audioRoute) setGain(gain [audioChannels]float32) {
	for ch, g := range gain {
		r.gain[ch].Store(math.Float32bits(g))
	}
}

// apply scales interleaved S16LE stereo PCM by the current gain.
func (r *audioRoute) apply(pcm []byte) {
	var gain [audioChannels]float32
	unity := true
	for ch := range gain {
		gain[ch] = math.Float32frombits(r.gain[ch].Load())
		unity = unity && gain[ch] == 1
	}
	if unity {
		return
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		ch := (i / 2) % audioChannels
		sample := float32(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8)) * gain[ch]
		v := math.Round(float64(sample))
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		s := int16(v)
		pcm[i], pcm[i+1] = byte(uint16(s)), byte(uint16(s)>>8)
	}
}

// paVolumeToLinear follows PulseAudio's cubic software volume curve.
func paVolumeToLinear(v int) float32 {
	if v <= 0 {
		return 0
	}
	f := float64(v) / paVolumeNorm
	return float32(f * f * f)
}

// parsePactlVolumes reads the raw per-channel values from
// "Volume: front-left: 47184 /  72% / -8.56 dB,   front-right: 47184 / ...".
func parsePactlVolumes(out string) ([]int, bool) {
	var volumes []int
	fields := strings.Fields(out)
	for i := 0; i+1 < len(fields); i++ {
		if strings.HasSuffix(fields[i], ":") && fields[i] != "Volume:" {
			if v, err := strconv.Atoi(fields[i+1]); err == nil {
				volumes = append(volumes, v)
			}
		}
	}
	return volumes, len(volumes) > 0
}

func parsePactlMute(out string) bool {
	return strings.Contains(strings.ToLower(out), "mute: yes")
}
