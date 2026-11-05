package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/thienanblog/ios-pc-android-transfer/internal/media"
)

// Paths in the API are relative to the shared folder and use "/" on every
// system, for example "Trip 2026/IMG_0001.HEIC". "" is the shared folder itself.

type fileInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Modified   int64  `json:"modified"` // milliseconds since 1970
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	VideoCodec string `json:"videoCodec,omitempty"`
}

type folderInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Modified int64  `json:"modified"`
	Items    int    `json:"items"`
}

type probeEntry struct {
	size  int64
	mtime time.Time
	info  media.Info
}

// cleanRel checks a relative path from a request. It refuses anything that
// could leave the shared folder or reach hidden files.
func cleanRel(rel string) (string, bool) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "", true
	}
	if strings.ContainsRune(rel, '\\') || len(rel) > 1024 {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", false
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return "", false
			}
		}
	}
	return rel, true
}

// resolve turns a clean relative path into an absolute one, refusing
// symbolic links on the way so nothing outside the shared folder is reachable.
func (s *Server) resolve(rel string) (string, bool) {
	rel, ok := cleanRel(rel)
	if !ok {
		return "", false
	}
	abs := s.cfg.DataDir
	if rel == "" {
		return abs, true
	}
	for _, part := range strings.Split(rel, "/") {
		abs = filepath.Join(abs, part)
		if st, err := os.Lstat(abs); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
	}
	return abs, true
}

// describe returns details of a file, caching the format probe.
func (s *Server) describe(rel string) (fileInfo, error) {
	abs, ok := s.resolve(rel)
	if !ok {
		return fileInfo{}, errors.New("bad path")
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return fileInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return fileInfo{}, errors.New("not a file")
	}
	s.mu.Lock()
	pe, ok := s.probes[rel]
	s.mu.Unlock()
	if !ok || pe.size != st.Size() || !pe.mtime.Equal(st.ModTime()) {
		info, err := media.ProbeFile(abs)
		if err != nil {
			info = media.Info{Kind: media.KindOther, Format: "other"}
		}
		pe = probeEntry{size: st.Size(), mtime: st.ModTime(), info: info}
		s.mu.Lock()
		if len(s.probes) > 50000 {
			s.probes = map[string]probeEntry{}
		}
		s.probes[rel] = pe
		s.mu.Unlock()
	}
	return fileInfo{
		Name: path.Base(rel), Path: rel, Size: st.Size(), Modified: st.ModTime().UnixMilli(),
		Kind: string(pe.info.Kind), Format: pe.info.Format, VideoCodec: pe.info.VideoCodec,
	}, nil
}

// lookup finds a visible regular file by its relative path.
func (s *Server) lookup(rel string) (string, fileInfo, bool) {
	rel, ok := cleanRel(rel)
	if !ok || rel == "" {
		return "", fileInfo{}, false
	}
	fi, err := s.describe(rel)
	if err != nil {
		return "", fileInfo{}, false
	}
	abs, _ := s.resolve(rel)
	return abs, fi, true
}

func visible(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") {
		return false
	}
	switch strings.ToLower(name) {
	case "desktop.ini", "thumbs.db", "$recycle.bin", "system volume information":
		return false
	}
	return true
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	dir, ok := cleanRel(r.URL.Query().Get("dir"))
	absDir, ok2 := s.resolve(dir)
	if !ok || !ok2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad folder"})
		return
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	files := []fileInfo{}
	folders := []folderInfo{}
	for _, e := range entries {
		n := e.Name()
		if !visible(n) {
			continue
		}
		rel := path.Join(dir, n)
		switch {
		case e.Type().IsDir():
			fi := folderInfo{Name: n, Path: rel}
			if info, err := e.Info(); err == nil {
				fi.Modified = info.ModTime().UnixMilli()
			}
			if sub, err := os.ReadDir(filepath.Join(absDir, n)); err == nil {
				for _, x := range sub {
					if visible(x.Name()) {
						fi.Items++
					}
				}
			}
			folders = append(folders, fi)
		case e.Type().IsRegular():
			if fi, err := s.describe(rel); err == nil {
				files = append(files, fi)
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool {
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified != files[j].Modified {
			return files[i].Modified > files[j].Modified
		}
		return files[i].Name < files[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "folders": folders, "files": files})
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	parent, ok := s.resolve(req.Dir)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad folder"})
		return
	}
	if st, err := os.Stat(parent); err != nil || !st.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	name := SafeName(req.Name)
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty name"})
		return
	}
	if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rel, _ := cleanRel(req.Dir)
	writeJSON(w, http.StatusOK, map[string]string{"path": path.Join(rel, name)})
}

func contentDisposition(kind, name string) string {
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

var mimeByFormat = map[string]string{
	"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp",
	"heic": "image/heic", "avif": "image/avif", "mov": "video/quicktime", "mp4": "video/mp4",
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	abs, fi, ok := s.lookup(r.PathValue("path"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if ct, ok := mimeByFormat[fi.Format]; ok {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if r.URL.Query().Get("inline") == "1" {
		w.Header().Set("Content-Disposition", contentDisposition("inline", fi.Name))
	} else {
		w.Header().Set("Content-Disposition", contentDisposition("attachment", fi.Name))
	}
	http.ServeContent(w, r, fi.Name, time.UnixMilli(fi.Modified), f)
}

// handleDelete removes a file, or a folder only when it is empty, so one
// tap on a phone can never wipe a whole album.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	rel, ok := cleanRel(r.PathValue("path"))
	if !ok || rel == "" {
		http.NotFound(w, r)
		return
	}
	abs, ok := s.resolve(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	st, err := os.Lstat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if st.IsDir() {
		entries, err := os.ReadDir(abs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		for _, e := range entries {
			if visible(e.Name()) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "folder-not-empty"})
				return
			}
		}
		// Only system leftovers such as .DS_Store or Thumbs.db remain.
		for _, e := range entries {
			if !e.IsDir() {
				os.Remove(filepath.Join(abs, e.Name()))
			}
		}
	} else if !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(abs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func fileKey(fi fileInfo, extra string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", fi.Path, fi.Size, fi.Modified, extra)))
	return hex.EncodeToString(sum[:12])
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	abs, fi, ok := s.lookup(r.PathValue("path"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	key := fileKey(fi, "thumb")
	thumb := filepath.Join(s.thumbsDir(), key+".jpg")
	serve := func(p string) {
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, p)
	}
	if _, err := os.Stat(thumb); err == nil {
		serve(thumb)
		return
	}

	s.mu.Lock()
	failedAt, failed := s.thumbFail[key]
	s.mu.Unlock()
	if !failed || time.Since(failedAt) > 5*time.Minute {
		l := s.lockFor(s.thumbMus, key)
		l.Lock()
		err := func() error {
			if _, err := os.Stat(thumb); err == nil {
				return nil
			}
			select {
			case s.thumbSem <- struct{}{}:
			case <-r.Context().Done():
				return r.Context().Err()
			}
			defer func() { <-s.thumbSem }()
			ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
			defer cancel()
			info := media.Info{Kind: media.Kind(fi.Kind), Format: fi.Format, VideoCodec: fi.VideoCodec}
			return s.cfg.Tools.Thumbnail(ctx, abs, thumb, info, 360)
		}()
		l.Unlock()
		if err == nil {
			serve(thumb)
			return
		}
		s.mu.Lock()
		s.thumbFail[key] = time.Now()
		s.mu.Unlock()
	}
	// Browsers show these themselves; fall back to the original.
	switch fi.Format {
	case "jpeg", "png", "gif", "webp":
		serve(abs)
		return
	}
	http.NotFound(w, r)
}
