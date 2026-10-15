package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIGuardsAndStatus(t *testing.T) {
	bin, _ := fakeDoubletake(t, "exec sleep 30\n")
	session := NewSession(bin, "window", false)
	t.Cleanup(session.Stop)
	h := newServer(session).handler()

	do := func(method, path, host, contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = host
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("GET", "/api/status", "127.0.0.1:1234", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != StateIdle || st.Source != "window" {
		t.Fatalf("status = %+v", st)
	}

	if rec := do("GET", "/api/status", "evil.example:1234", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding host code = %d", rec.Code)
	}
	if rec := do("POST", "/api/stop", "127.0.0.1:1234", "text/plain", ""); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON POST code = %d", rec.Code)
	}
	if rec := do("POST", "/api/source", "127.0.0.1:1234", "application/json", `{"source":"virtual"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source code = %d", rec.Code)
	}
	if rec := do("POST", "/api/source", "localhost:1234", "application/json", `{"source":"monitor"}`); rec.Code != http.StatusOK {
		t.Fatalf("source code = %d", rec.Code)
	}
	if got := session.Status(); got.Source != "monitor" || got.State != StateDiscovering {
		t.Fatalf("after source switch = %+v", got)
	}
	if rec := do("GET", "/", "127.0.0.1:1234", "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Doubletake") {
		t.Fatalf("index code = %d", rec.Code)
	}
}
