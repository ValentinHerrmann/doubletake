// Command doubletake-ui is a small desktop UI for doubletake: it lists the
// AirPlay receivers on the network, answers PIN prompts and starts/stops
// screen mirroring. It drives the doubletake CLI and shows its UI in a
// Chrome/Chromium app window served from 127.0.0.1.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// The app exits when no window has polled for this long and nothing is
	// being cast, so closing the window ends it.
	idleExit     = 30 * time.Second
	idleCheck    = 5 * time.Second
	probeTimeout = time.Second
)

var browsers = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}

type instanceInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

func main() {
	doubletakeFlag := flag.String("doubletake", "", "Path to the doubletake binary (default: next to this binary, then doubletake-src or doubletake on PATH)")
	source := flag.String("source", "any", "Capture source: any (picker offers screens and windows), monitor or window")
	noAudio := flag.Bool("no-audio", false, "Do not stream audio")
	noWindow := flag.Bool("no-window", false, "Do not open an app window; print the URL instead")
	flag.Parse()

	if !validSources[*source] {
		log.Fatalf("invalid -source %q (want any, monitor or window)", *source)
	}
	sourceSet := false
	flag.Visit(func(f *flag.Flag) { sourceSet = sourceSet || f.Name == "source" })

	stateFile := instanceFile()
	if url, ok := runningInstance(stateFile); ok {
		if sourceSet {
			if err := postJSON(url+"api/source", map[string]string{"source": *source}); err != nil {
				log.Printf("could not update running instance: %v", err)
			}
		}
		if !*noWindow {
			openWindow(url)
		}
		fmt.Println(url)
		return
	}

	binary, err := findDoubletake(*doubletakeFlag)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if err := writeInstance(stateFile, instanceInfo{Port: port, PID: os.Getpid()}); err != nil {
		log.Printf("warning: %v", err)
	}
	defer os.Remove(stateFile)

	session := NewSession(binary, *source, *noAudio)
	srv := newServer(session)
	httpServer := &http.Server{Handler: srv.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	session.Start()
	fmt.Println(url)
	if !*noWindow {
		openWindow(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(idleCheck)
	defer ticker.Stop()
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-ticker.C:
			running = session.Busy() || srv.idleFor() < idleExit
		}
	}
	session.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
}

// findDoubletake prefers a doubletake built alongside this binary, since only
// the fork's build has the -source flag.
func findDoubletake(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, name := range []string{"doubletake", "doubletake-src"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	for _, name := range []string{"doubletake-src", "doubletake"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("doubletake not found; pass -doubletake <path>")
}

func instanceFile() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "doubletake-ui.json")
}

func writeInstance(path string, info instanceInfo) error {
	data, _ := json.Marshal(info)
	return os.WriteFile(path, data, 0o600)
}

// runningInstance returns the URL of an already running doubletake-ui.
func runningInstance(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var info instanceInfo
	if json.Unmarshal(data, &info) != nil || info.Port == 0 {
		return "", false
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", info.Port)
	client := http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url + "api/status")
	if err != nil {
		return "", false
	}
	resp.Body.Close()
	return url, resp.StatusCode == http.StatusOK
}

func postJSON(url string, body any) error {
	data, _ := json.Marshal(body)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

// openWindow shows the UI in a Chromium app window with its own profile, or
// falls back to the default browser.
func openWindow(url string) {
	env := sessionEnv(os.Environ())
	var name string
	var args []string
	for _, b := range browsers {
		if path, err := exec.LookPath(b); err == nil {
			name = path
			break
		}
	}
	if name != "" {
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			home, _ := os.UserHomeDir()
			dataHome = filepath.Join(home, ".local", "share")
		}
		args = []string{
			"--user-data-dir=" + filepath.Join(dataHome, "doubletake-ui", "profile"),
			"--no-first-run",
			"--no-default-browser-check",
			"--app=" + url,
			"--window-size=480,640",
		}
		// Without XDG_SESSION_TYPE (e.g. from a system service) Chromium assumes X11.
		if envHas(env, "WAYLAND_DISPLAY") {
			args = append([]string{"--ozone-platform=wayland"}, args...)
		}
	} else if path, err := exec.LookPath("xdg-open"); err == nil {
		name, args = path, []string{url}
	} else {
		log.Printf("no browser found; open %s", url)
		return
	}
	name, args = withoutCapabilities(name, args)
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("open window: %v; open %s", err, url)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func envHas(env []string, key string) bool {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key && v != "" {
			return true
		}
	}
	return false
}
