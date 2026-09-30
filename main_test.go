package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "demo-not-a-real-key-0000"

func newTestServer(t *testing.T, cfg config) (*server, *httptest.Server) {
	t.Helper()
	if cfg.dataDir == "" {
		cfg.dataDir = t.TempDir()
	}
	s := &server{cfg: cfg, path: filepath.Join(cfg.dataDir, "notes.json"),
		log: slog.New(slog.NewJSONHandler(io.Discard, nil)), now: time.Now}
	if err := s.load(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts
}

func postJSON(t *testing.T, base, key, text string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	req, _ := http.NewRequest("POST", base+"/api/notes", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestUnconfigured(t *testing.T) {
	_, ts := newTestServer(t, config{})
	res, _ := http.Get(ts.URL + "/healthz")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "NOTES_WRITE_KEY") {
		t.Fatalf("healthz unconfigured: %d %s", res.StatusCode, b)
	}
	if r := postJSON(t, ts.URL, testKey, "hi"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post unconfigured: %d", r.StatusCode)
	}
	res, _ = http.Get(ts.URL + "/")
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "Not configured yet") {
		t.Fatalf("page unconfigured: %d", res.StatusCode)
	}
}

func TestShortKeyIsNotEnough(t *testing.T) {
	cfg := config{title: "Team notes", writeKey: "short"}
	if m := cfg.missing(); len(m) != 1 || m[0] != "NOTES_WRITE_KEY" {
		t.Fatalf("missing: %v", m)
	}
}

func TestNotesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config{title: "Team notes", writeKey: testKey, dataDir: dir}
	_, ts := newTestServer(t, cfg)

	res, _ := http.Get(ts.URL + "/healthz")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("healthz: %d", res.StatusCode)
	}
	if r := postJSON(t, ts.URL, "wrong-key-wrong-key", "hi"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong key: %d", r.StatusCode)
	}
	if r := postJSON(t, ts.URL, "", "hi"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r := postJSON(t, ts.URL, testKey, "   "); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty: %d", r.StatusCode)
	}
	if r := postJSON(t, ts.URL, testKey, strings.Repeat("x", maxText+1)); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("too long: %d", r.StatusCode)
	}
	if r := postJSON(t, ts.URL, testKey, "first"); r.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d", r.StatusCode)
	}

	// The form on the page, as a browser sends it.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noRedirect.PostForm(ts.URL+"/", url.Values{"text": {"second <b>"}, "key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("form: %d", res.StatusCode)
	}

	// A new server on the same folder, as after a redeploy with a volume.
	_, ts2 := newTestServer(t, cfg)
	res, _ = http.Get(ts2.URL + "/api/notes")
	var out struct{ Notes []note }
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if len(out.Notes) != 2 || out.Notes[0].Text != "second <b>" || out.Notes[1].Text != "first" {
		t.Fatalf("after restart: %+v", out.Notes)
	}
	res, _ = http.Get(ts2.URL + "/")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "second &lt;b&gt;") || !strings.Contains(string(b), "<h1>Team notes</h1>") {
		t.Fatal("page doesn't show the escaped note and title")
	}
	if strings.Contains(string(b), testKey) {
		t.Fatal("page leaks the write key")
	}
}
