package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Conversions can take minutes for long videos, so they run in the
// background: the phone starts one, polls its progress, then downloads it.

type job struct {
	mu       sync.Mutex
	state    string // queued, working, ready, error
	progress float64
	err      string
}

type jobStatus struct {
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	URL      string  `json:"url,omitempty"`
	Error    string  `json:"error,omitempty"`
}

var targetExt = map[string]string{"jpg": ".jpg", "png": ".png", "mp4": ".mp4"}

func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	ext, ok := targetExt[req.To]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to must be jpg, png or mp4"})
		return
	}
	src, fi, ok := s.lookup(req.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}
	if (req.To == "mp4") != (fi.Kind == "video") || fi.Kind == "other" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot convert " + fi.Kind + " to " + req.To})
		return
	}
	if req.To == "mp4" && !s.cfg.Tools.CanConvertVideos() {
		writeJSON(w, http.StatusOK, jobStatus{State: "error", Error: "ffmpeg-missing"})
		return
	}
	if req.To != "mp4" && !s.cfg.Tools.CanConvertImages() {
		writeJSON(w, http.StatusOK, jobStatus{State: "error", Error: "image-tool-missing"})
		return
	}

	key := fileKey(fi, req.To)
	out := filepath.Join(s.cacheDir(), key+ext)
	downloadName := strings.TrimSuffix(fi.Name, filepath.Ext(fi.Name)) + ext
	link := "/api/converted/" + key + "?name=" + url.QueryEscape(downloadName)

	if _, err := os.Stat(out); err == nil {
		writeJSON(w, http.StatusOK, jobStatus{State: "ready", Progress: 1, URL: link})
		return
	}

	s.mu.Lock()
	j, exists := s.jobs[key]
	if exists {
		j.mu.Lock()
		retry := j.state == "error"
		j.mu.Unlock()
		if retry {
			exists = false
		}
	}
	if !exists {
		j = &job{state: "queued"}
		s.jobs[key] = j
		go s.runJob(j, src, out, req.To)
	}
	s.mu.Unlock()

	j.mu.Lock()
	st := jobStatus{State: j.state, Progress: j.progress, Error: j.err}
	j.mu.Unlock()
	if st.State == "ready" {
		st.URL = link
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) runJob(j *job, src, out, to string) {
	set := func(state string, p float64, err string) {
		j.mu.Lock()
		j.state, j.progress, j.err = state, p, err
		j.mu.Unlock()
	}
	select {
	case s.convertSem <- struct{}{}:
	case <-s.ctx.Done():
		set("error", 0, "stopped")
		return
	}
	defer func() { <-s.convertSem }()
	set("working", 0, "")

	timeout := 2 * time.Minute
	if to == "mp4" {
		timeout = 3 * time.Hour
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	var err error
	if to == "mp4" {
		err = s.cfg.Tools.ConvertVideo(ctx, src, out, func(f float64) { set("working", f, "") })
	} else {
		err = s.cfg.Tools.ConvertImage(ctx, src, out)
	}
	if err != nil {
		s.cfg.Logf("convert %s to %s: %v", filepath.Base(src), to, err)
		set("error", 0, err.Error())
		return
	}
	set("ready", 1, "")
}

var keyRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

func (s *Server) handleConverted(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !keyRe.MatchString(key) {
		http.NotFound(w, r)
		return
	}
	for _, ext := range []string{".jpg", ".png", ".mp4"} {
		p := filepath.Join(s.cacheDir(), key+ext)
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			break
		}
		name := SafeName(r.URL.Query().Get("name"))
		if filepath.Ext(name) != ext {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
		}
		disp := "attachment"
		if r.URL.Query().Get("inline") == "1" {
			disp = "inline"
		}
		w.Header().Set("Content-Disposition", contentDisposition(disp, name))
		w.Header().Set("Content-Type", map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".mp4": "video/mp4"}[ext])
		http.ServeContent(w, r, name, st.ModTime(), f)
		return
	}
	http.NotFound(w, r)
}
