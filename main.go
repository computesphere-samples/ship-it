// Notes is a small note-taking app: a page to read and add notes, a JSON API,
// and a health check. Notes are kept in one file, /data/notes.json by
// default, so they survive a restart only if /data is on a volume.
//
// It reads two settings from the environment: NOTES_TITLE, the page heading,
// and NOTES_WRITE_KEY, a secret anyone adding a note must give. Until both are
// set, it still starts, but /healthz fails and nobody can add notes.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

var version = "1.0.0"

const (
	maxText     = 1000 // characters per note
	maxNotes    = 200  // notes kept; the oldest are dropped
	minKeyChars = 16
)

type note struct {
	ID        int64     `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type config struct {
	title    string
	writeKey string
	dataDir  string
}

// missing lists the settings that aren't set, or aren't usable.
func (c config) missing() []string {
	var m []string
	if strings.TrimSpace(c.title) == "" {
		m = append(m, "NOTES_TITLE")
	}
	if utf8.RuneCountInString(c.writeKey) < minKeyChars {
		m = append(m, "NOTES_WRITE_KEY")
	}
	return m
}

type server struct {
	cfg  config
	path string // the notes file
	log  *slog.Logger
	now  func() time.Time

	mu    sync.Mutex
	notes []note // oldest first
}

var errBadKey = errors.New("wrong write key")

func (s *server) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.notes)
}

// persist writes every note to a temporary file, then renames it over the old
// one, so the file is never half written. The caller holds s.mu.
func (s *server) persist() error {
	b, err := json.MarshalIndent(s.notes, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *server) list() []note {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]note, len(s.notes))
	for i, n := range s.notes { // newest first
		out[len(s.notes)-1-i] = n
	}
	return out
}

func (s *server) add(text string) (note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := note{ID: s.now().UnixNano(), Text: text, CreatedAt: s.now().UTC()}
	s.notes = append(s.notes, n)
	if len(s.notes) > maxNotes {
		s.notes = s.notes[len(s.notes)-maxNotes:]
	}
	if err := s.persist(); err != nil {
		s.notes = s.notes[:len(s.notes)-1]
		return note{}, err
	}
	return n, nil
}

func (s *server) keyOK(given string) bool {
	if s.cfg.writeKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.cfg.writeKey)) == 1
}

// checkText trims a note and checks its length.
func checkText(t string) (string, string) {
	t = strings.TrimSpace(t)
	switch {
	case t == "":
		return "", "A note can't be empty."
	case utf8.RuneCountInString(t) > maxText:
		return "", "A note can be at most 1000 characters."
	}
	return t, ""
}

// onVolume reports whether the data folder is its own mount, such as a volume,
// rather than part of the container's filesystem.
func onVolume(dir string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && f[4] == dir {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;color:#1e293b}
h1{font-size:1.6rem;margin:0 0 1rem}
.warn{background:#fef3c7;border:1px solid #f59e0b;padding:.6rem .8rem;border-radius:6px;margin:0 0 1rem}
.err{background:#fee2e2;border:1px solid #ef4444;padding:.6rem .8rem;border-radius:6px;margin:0 0 1rem}
form{display:grid;gap:.5rem;margin:0 0 1.5rem}
textarea,input{font:inherit;padding:.5rem;border:1px solid #cbd5e1;border-radius:6px}
button{font:inherit;padding:.5rem;border:0;border-radius:6px;background:#355dee;color:#fff;cursor:pointer}
ul{list-style:none;padding:0}li{border-top:1px solid #e2e8f0;padding:.7rem 0;white-space:pre-wrap}
small{color:#64748b}footer{margin-top:2rem;color:#64748b;font-size:.85rem}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Missing}}<p class="warn">Not configured yet. Set {{range $i, $m := .Missing}}{{if $i}} and {{end}}<code>{{$m}}</code>{{end}}, then redeploy.</p>{{end}}
{{if not .OnVolume}}<p class="warn">Notes are on this spherelet's own disk and will be lost on the next redeploy. Mount a volume at <code>{{.DataDir}}</code>.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/">
<textarea name="text" rows="3" maxlength="1000" placeholder="Write a note" required></textarea>
<input name="key" type="password" placeholder="Write key" autocomplete="current-password" required>
<button>Add note</button>
</form>
<ul>{{range .Notes}}<li>{{.Text}}<br><small>{{.CreatedAt.Format "2006-01-02 15:04 UTC"}}</small></li>{{else}}<li><small>No notes yet.</small></li>{{end}}</ul>
<footer>Notes {{.Version}}</footer>
</body></html>`))

type pageData struct {
	Title, DataDir, Error, Version string
	Missing                        []string
	OnVolume                       bool
	Notes                          []note
}

func (s *server) render(w http.ResponseWriter, status int, errMsg string) {
	title := strings.TrimSpace(s.cfg.title)
	if title == "" {
		title = "Notes"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = page.Execute(w, pageData{
		Title: title, DataDir: s.cfg.dataDir, Error: errMsg, Version: version,
		Missing: s.cfg.missing(), OnVolume: onVolume(s.cfg.dataDir), Notes: s.list(),
	})
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		s.render(w, http.StatusOK, "")
	})
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			s.render(w, http.StatusBadRequest, "That note is too long.")
			return
		}
		status, msg := s.create(r.PostFormValue("text"), r.PostFormValue("key"))
		if msg != "" {
			s.render(w, status, msg)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("GET /api/notes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"notes": s.list()})
	})
	mux.HandleFunc("POST /api/notes", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send JSON: {"text": "..."}`})
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		status, msg := s.create(in.Text, key)
		if msg != "" {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusCreated, s.list()[0])
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		storage := "spherelet disk"
		if onVolume(s.cfg.dataDir) {
			storage = "volume"
		}
		if m := s.cfg.missing(); len(m) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not configured", "missing": m, "storage": storage})
			return
		}
		if fi, err := os.Stat(s.cfg.dataDir); err != nil || !fi.IsDir() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "no data folder", "storage": storage})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "storage": storage})
	})

	return s.secure(s.logRequests(mux))
}

// create checks the key and the text, then saves the note. It returns an HTTP
// status and a message for the user when it can't.
func (s *server) create(text, key string) (int, string) {
	if m := s.cfg.missing(); len(m) > 0 {
		return http.StatusServiceUnavailable, "Notes isn't configured yet: set " + strings.Join(m, " and ") + "."
	}
	if !s.keyOK(key) {
		s.log.Warn("note refused", "reason", errBadKey.Error())
		return http.StatusForbidden, "That write key isn't right."
	}
	t, msg := checkText(text)
	if msg != "" {
		return http.StatusBadRequest, msg
	}
	n, err := s.add(t)
	if err != nil {
		s.log.Error("can't save notes", "path", s.path, "error", err.Error())
		return http.StatusInternalServerError, "Couldn't save the note. Check the service's log."
	}
	s.log.Info("note added", "id", n.ID, "chars", utf8.RuneCountInString(n.Text))
	return 0, ""
}

func (s *server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" && rec.status == http.StatusOK {
			return // passing health checks would drown out everything else
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	cfg := config{
		title:    os.Getenv("NOTES_TITLE"),
		writeKey: os.Getenv("NOTES_WRITE_KEY"),
		dataDir:  "/data",
	}
	if d := os.Getenv("DATA_DIR"); d != "" {
		cfg.dataDir = d
	}
	s := &server{cfg: cfg, path: filepath.Join(cfg.dataDir, "notes.json"), log: log, now: time.Now}
	if err := s.load(); err != nil {
		log.Error("can't read saved notes", "path", s.path, "error", err.Error())
		os.Exit(1)
	}
	if m := cfg.missing(); len(m) > 0 {
		log.Warn("not configured: notes can't be added and /healthz fails until these are set",
			"missing", m, "key_min_chars", minKeyChars)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Info("notes listening", "version", version, "port", port, "data", s.path,
		"notes", len(s.notes), "storage_is_volume", onVolume(cfg.dataDir))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("can't listen", "error", err.Error())
		os.Exit(1)
	}
}
