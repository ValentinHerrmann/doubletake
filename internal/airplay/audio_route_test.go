package airplay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// fakePulse is a minimal pactl: sinks, one stream, a default and volumes.
type fakePulse struct {
	defaultSink string
	sinks       []string // index = position
	modules     map[string]string
	volume      map[string]int
	mute        map[string]bool
	inputSink   int // sink index the one stream plays on
	failLoad    bool
	nextModule  int
	calls       []string
}

func newFakePulse() *fakePulse {
	return &fakePulse{
		defaultSink: "speakers",
		sinks:       []string{"speakers", "hdmi"},
		modules:     map[string]string{},
		volume:      map[string]int{"speakers": 47184, "hdmi": paVolumeNorm},
		mute:        map[string]bool{},
		nextModule:  536870913,
	}
}

func (p *fakePulse) index(name string) int {
	for i, s := range p.sinks {
		if s == name {
			return i
		}
	}
	return -1
}

func (p *fakePulse) run(args ...string) (string, error) {
	p.calls = append(p.calls, strings.Join(args, " "))
	switch args[0] {
	case "get-default-sink":
		return p.defaultSink + "\n", nil
	case "set-default-sink":
		if p.index(args[1]) < 0 {
			return "", errors.New("no such sink")
		}
		p.defaultSink = args[1]
		return "", nil
	case "load-module":
		if p.failLoad {
			return "", errors.New("module load failed")
		}
		id := fmt.Sprint(p.nextModule)
		p.nextModule++
		p.modules[id] = strings.Join(args[1:], " ")
		p.sinks = append(p.sinks, audioRouteSinkName)
		p.volume[audioRouteSinkName] = paVolumeNorm
		return id + "\n", nil
	case "unload-module":
		delete(p.modules, args[1])
		if i := p.index(audioRouteSinkName); i >= 0 {
			p.sinks[i] = "(removed)"
		}
		return "", nil
	case "list":
		var b strings.Builder
		switch args[2] {
		case "modules":
			for id, a := range p.modules {
				fmt.Fprintf(&b, "%s\tmodule-null-sink\t%s\n", id, a)
			}
		case "sinks":
			for i, s := range p.sinks {
				fmt.Fprintf(&b, "%d\t%s\tPipeWire\ts16le 2ch 48000Hz\tRUNNING\n", i, s)
			}
		case "sink-inputs":
			fmt.Fprintf(&b, "77\t%d\t42\tprotocol-native.c\tfloat32le 2ch 48000Hz\n", p.inputSink)
		}
		return b.String(), nil
	case "move-sink-input":
		p.inputSink = p.index(args[2])
		return "", nil
	case "get-sink-volume":
		v := p.volume[args[1]]
		return fmt.Sprintf("Volume: front-left: %d /  72%% / -8.56 dB,   front-right: %d /  72%% / -8.56 dB\n        balance 0.00\n", v, v), nil
	case "set-sink-volume":
		var v int
		fmt.Sscan(args[2], &v)
		p.volume[args[1]] = v
		return "", nil
	case "get-sink-mute":
		if p.mute[args[1]] {
			return "Mute: yes\n", nil
		}
		return "Mute: no\n", nil
	case "set-sink-mute":
		p.mute[args[1]] = args[2] == "true"
		return "", nil
	}
	return "", fmt.Errorf("unexpected pactl %v", args)
}

func newTestRoute(t *testing.T, p *fakePulse) *audioRoute {
	return newAudioRoute(p.run, filepath.Join(t.TempDir(), "audio.json"), nil)
}

func TestAudioRouteMakesCastTheOutputAndRestores(t *testing.T) {
	p := newFakePulse()
	r := newTestRoute(t, p)

	monitor, err := r.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if monitor != "doubletake.monitor" || p.defaultSink != audioRouteSinkName {
		t.Fatalf("monitor %q, default %q; want the Doubletake output as default", monitor, p.defaultSink)
	}
	if p.inputSink != p.index(audioRouteSinkName) {
		t.Fatal("playing stream was not moved to the Doubletake output")
	}
	if p.volume[audioRouteSinkName] != 47184 {
		t.Fatalf("Doubletake output volume = %d, want the speakers' 47184", p.volume[audioRouteSinkName])
	}

	// A second cast shares the output.
	if _, err := r.acquire(); err != nil {
		t.Fatal(err)
	}
	r.release()
	if p.defaultSink != audioRouteSinkName || len(p.modules) != 1 {
		t.Fatal("output removed while a cast still runs")
	}
	r.release()
	if p.defaultSink != "speakers" || len(p.modules) != 0 || p.inputSink != p.index("speakers") {
		t.Fatalf("after the last cast: default %q, modules %v, stream on %d", p.defaultSink, p.modules, p.inputSink)
	}
}

func TestAudioRouteKeepsOutputChosenMidCast(t *testing.T) {
	p := newFakePulse()
	r := newTestRoute(t, p)
	if _, err := r.acquire(); err != nil {
		t.Fatal(err)
	}
	p.defaultSink = "hdmi"
	r.release()
	if p.defaultSink != "hdmi" {
		t.Fatalf("default = %q, want the user's choice hdmi", p.defaultSink)
	}
}

func TestAudioRouteRecoversAfterCrash(t *testing.T) {
	p := newFakePulse()
	statePath := filepath.Join(t.TempDir(), "audio.json")
	crashed := newAudioRoute(p.run, statePath, nil)
	if _, err := crashed.acquire(); err != nil {
		t.Fatal(err)
	}
	// The process dies: no release. A new one starts.
	r := newAudioRoute(p.run, statePath, nil)
	if _, err := r.acquire(); err != nil {
		t.Fatal(err)
	}
	if len(p.modules) != 1 {
		t.Fatalf("%d null sinks after recovery, want the leftover reused", len(p.modules))
	}
	r.release()
	if p.defaultSink != "speakers" || len(p.modules) != 0 {
		t.Fatalf("after recovery: default %q, modules %v; want speakers restored", p.defaultSink, p.modules)
	}
}

func TestAudioRouteFailureLeavesOutputAlone(t *testing.T) {
	p := newFakePulse()
	p.failLoad = true
	r := newTestRoute(t, p)
	if _, err := r.acquire(); err == nil {
		t.Fatal("acquire succeeded without an output")
	}
	if p.defaultSink != "speakers" {
		t.Fatalf("default changed to %q", p.defaultSink)
	}
}

func TestAudioRouteGainFollowsVolumeAndMute(t *testing.T) {
	p := newFakePulse()
	r := newTestRoute(t, p)
	if _, err := r.acquire(); err != nil {
		t.Fatal(err)
	}
	defer r.release()

	pcm := func() []byte {
		b := make([]byte, 8)
		for i := 0; i < 4; i++ {
			binary.LittleEndian.PutUint16(b[i*2:], uint16(int16(10000)))
		}
		return b
	}
	sample := func(b []byte) int16 { return int16(binary.LittleEndian.Uint16(b)) }

	p.volume[audioRouteSinkName] = paVolumeNorm / 2
	r.refreshGain()
	b := pcm()
	r.apply(b)
	if got := sample(b); got != 1250 { // (1/2)^3
		t.Fatalf("half fader sample = %d, want 1250", got)
	}

	p.mute[audioRouteSinkName] = true
	r.refreshGain()
	b = pcm()
	r.apply(b)
	if got := sample(b); got != 0 {
		t.Fatalf("muted sample = %d, want 0", got)
	}

	p.mute[audioRouteSinkName] = false
	p.volume[audioRouteSinkName] = paVolumeNorm * 2 // 200 %: clips
	r.refreshGain()
	b = pcm()
	r.apply(b)
	if got := sample(b); got != math.MaxInt16 {
		t.Fatalf("amplified sample = %d, want clipped %d", got, math.MaxInt16)
	}
}

func TestParsePactlVolumes(t *testing.T) {
	out := "Volume: front-left: 47184 /  72% / -8,56 dB,   front-right: 32768 /  50% / -18,06 dB\n        balance -0,30\n"
	got, ok := parsePactlVolumes(out)
	if !ok || len(got) != 2 || got[0] != 47184 || got[1] != 32768 {
		t.Fatalf("parsePactlVolumes = %v, %v", got, ok)
	}
	if !parsePactlMute("Mute: yes\n") || parsePactlMute("Mute: no\n") {
		t.Fatal("parsePactlMute")
	}
}
