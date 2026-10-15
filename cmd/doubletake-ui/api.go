package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed web
var webFiles embed.FS

var validSources = map[string]bool{"any": true, "monitor": true, "window": true}

type server struct {
	session *Session
	// lastSeen is the Unix time of the last request from a UI page; the app
	// exits once nobody is watching and nothing is being cast.
	lastSeen atomic.Int64
}

func newServer(session *Session) *server {
	s := &server{session: session}
	s.touch()
	return s
}

func (s *server) touch() { s.lastSeen.Store(time.Now().Unix()) }

func (s *server) idleFor() time.Duration {
	return time.Since(time.Unix(s.lastSeen.Load(), 0))
}

func (s *server) handler() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.session.Status())
	})
	mux.HandleFunc("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		s.session.Start()
		writeJSON(w, http.StatusOK, s.session.Status())
	})
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		s.session.Stop()
		writeJSON(w, http.StatusOK, s.session.Status())
	})
	mux.HandleFunc("POST /api/select", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Index int `json:"index"`
		}
		ok := decode(r, &body) && s.session.Choose(body.Index)
		writeJSON(w, statusFor(ok), s.session.Status())
	})
	mux.HandleFunc("POST /api/pin", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		ok := decode(r, &body) && s.session.Pin(body.Code)
		writeJSON(w, statusFor(ok), s.session.Status())
	})
	mux.HandleFunc("POST /api/source", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source string `json:"source"`
		}
		if !decode(r, &body) || !validSources[body.Source] {
			http.Error(w, "source must be any, monitor or window", http.StatusBadRequest)
			return
		}
		s.session.SetSource(body.Source)
		// Apply right away unless that would interrupt a connection.
		if !s.session.Busy() {
			s.session.Start()
		}
		writeJSON(w, http.StatusOK, s.session.Status())
	})
	return s.guard(mux)
}

// guard restricts the API to this machine's browser. The listener is bound to
// loopback; the Host check stops DNS-rebinding pages, and requiring a JSON
// content type on POST forces a CORS preflight that is never answered.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.touch()
		}
		next.ServeHTTP(w, r)
	})
}

func decode(r *http.Request, v any) bool {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(v) == nil
}

func statusFor(ok bool) int {
	if ok {
		return http.StatusOK
	}
	return http.StatusConflict
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
