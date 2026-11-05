// Package server is the HTTP side of PhotoBridge: the web page phones open,
// resumable uploads, the shared file list, downloads and conversions.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/thienanblog/ios-pc-android-transfer/internal/media"
)

// Config is what the server needs from main.
type Config struct {
	DataDir string      // shared folder: received files land here, files put here can be downloaded
	Token   string      // access key carried in the QR code link
	Tools   media.Tools // converters found on this computer
	Version string
	Web     fs.FS // static web page files
	// URLs returns the links (with token) phones should open, best first.
	URLs func() []string
	// Logf is used for request errors; defaults to log.Printf.
	Logf func(format string, args ...any)
}

// Server serves the web page and the API.
type Server struct {
	cfg      Config
	stateDir string // DataDir/.photobridge
	ctx      context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	finishMu  sync.Mutex // serialises picking a free file name
	uploadMus map[string]*sync.Mutex
	probes    map[string]probeEntry
	jobs      map[string]*job
	thumbFail map[string]time.Time
	thumbMus  map[string]*sync.Mutex

	convertSem chan struct{}
	thumbSem   chan struct{}
}

const (
	cookieName  = "pb_token"
	csrfHeader  = "X-PhotoBridge"
	maxChunk    = 64 << 20
	stateFolder = ".photobridge"
)

// New prepares the folders and returns a server.
func New(cfg Config) (*Server, error) {
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.URLs == nil {
		cfg.URLs = func() []string { return nil }
	}
	s := &Server{
		cfg:        cfg,
		stateDir:   filepath.Join(cfg.DataDir, stateFolder),
		uploadMus:  map[string]*sync.Mutex{},
		probes:     map[string]probeEntry{},
		jobs:       map[string]*job{},
		thumbFail:  map[string]time.Time{},
		thumbMus:   map[string]*sync.Mutex{},
		convertSem: make(chan struct{}, 2),
		thumbSem:   make(chan struct{}, 3),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	for _, d := range []string{s.partsDir(), s.cacheDir(), s.thumbsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s.cleanOld(s.partsDir(), 7*24*time.Hour)
	s.cleanOld(s.cacheDir(), 3*24*time.Hour)
	s.cleanOld(s.thumbsDir(), 30*24*time.Hour)
	return s, nil
}

// Close stops running conversions.
func (s *Server) Close() { s.cancel() }

func (s *Server) partsDir() string  { return filepath.Join(s.stateDir, "parts") }
func (s *Server) cacheDir() string  { return filepath.Join(s.stateDir, "converted") }
func (s *Server) thumbsDir() string { return filepath.Join(s.stateDir, "thumbs") }

func (s *Server) cleanOld(dir string, age time.Duration) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Handler returns the full HTTP handler, with access control.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(s.cfg.Web)))
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/qr", s.handleQR)
	mux.HandleFunc("POST /api/uploads", s.handleUploadInit)
	mux.HandleFunc("GET /api/uploads/{id}", s.handleUploadStatus)
	mux.HandleFunc("PUT /api/uploads/{id}", s.handleUploadChunk)
	mux.HandleFunc("GET /api/files", s.handleList)
	mux.HandleFunc("GET /api/files/{name}", s.handleDownload)
	mux.HandleFunc("DELETE /api/files/{name}", s.handleDelete)
	mux.HandleFunc("GET /api/thumbs/{name}", s.handleThumb)
	mux.HandleFunc("POST /api/convert", s.handleConvert)
	mux.HandleFunc("GET /api/converted/{key}", s.handleConverted)
	return s.guard(mux)
}

// guard lets a request through when it carries the access key (in the QR
// link or the cookie set from it), or when it comes from this computer itself.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if t := r.URL.Query().Get("t"); t != "" && s.tokenOK(t) {
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: s.cfg.Token, Path: "/",
				MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
				// Drop the key from the address bar so it is not shared by accident.
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		} else if !s.authorized(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(unauthorizedPage))
			return
		}

		// Changes must come from our own page (a custom header cannot be
		// sent cross-site without a CORS preflight, which we never allow).
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(csrfHeader) != "1" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing " + csrfHeader + " header"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(t string) bool {
	return s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && s.tokenOK(c.Value) {
		return true
	}
	return isLocal(r)
}

// isLocal is true for requests from this computer to localhost. The Host
// check stops web pages from reaching us through DNS rebinding.
func isLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

type toolsStatus struct {
	Images     bool   `json:"images"`
	Videos     bool   `json:"videos"`
	HDR        bool   `json:"hdr"`
	ImageTools string `json:"imageTools"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	t := s.cfg.Tools
	var names []string
	for _, n := range []struct {
		ok   bool
		name string
	}{{t.Sips != "", "sips"}, {t.Vips != "", "libvips"}, {t.Magick != "", "ImageMagick"}, {t.HeifConvert != "", "libheif"}, {t.FFmpeg != "" && t.FFmpegHEIC, "ffmpeg"}} {
		if n.ok {
			names = append(names, n.name)
		}
	}
	resp := map[string]any{
		"version": s.cfg.Version,
		"isLocal": isLocal(r),
		"tools": toolsStatus{
			Images:     t.CanConvertImages(),
			Videos:     t.CanConvertVideos(),
			HDR:        t.FFmpegZscale && t.FFprobe != "",
			ImageTools: strings.Join(names, ", "),
		},
	}
	if isLocal(r) {
		resp["urls"] = s.cfg.URLs()
		resp["dataDir"] = s.cfg.DataDir
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	if !isLocal(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	urls := s.cfg.URLs()
	i := 0
	if v := r.URL.Query().Get("i"); v != "" {
		for _, c := range v {
			if c < '0' || c > '9' {
				i = -1
				break
			}
			i = i*10 + int(c-'0')
		}
	}
	if i < 0 || i >= len(urls) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(urls[i], qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) lockFor(m map[string]*sync.Mutex, key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := m[key]
	if !ok {
		l = &sync.Mutex{}
		m[key] = l
	}
	return l
}

const unauthorizedPage = `<!doctype html><html lang="vi"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>PhotoBridge</title>
<style>body{font-family:system-ui,sans-serif;max-width:28rem;margin:4rem auto;padding:0 1rem;line-height:1.5}</style></head>
<body><h1>Cần mã QR</h1><p>Hãy mở PhotoBridge trên máy tính rồi quét mã QR hiện trên màn hình để kết nối.</p>
<p lang="en">Scan the QR code shown on the computer running PhotoBridge to connect.</p></body></html>`
