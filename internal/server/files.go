package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thienanblog/ios-pc-android-transfer/internal/media"
)

type fileInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Modified   int64  `json:"modified"` // milliseconds since 1970
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	VideoCodec string `json:"videoCodec,omitempty"`
}

type probeEntry struct {
	size  int64
	mtime time.Time
	info  media.Info
}

// describe returns details of a file in the shared folder, caching the probe.
func (s *Server) describe(name string) (fileInfo, error) {
	st, err := os.Stat(filepath.Join(s.cfg.DataDir, name))
	if err != nil {
		return fileInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return fileInfo{}, errors.New("not a file")
	}
	s.mu.Lock()
	pe, ok := s.probes[name]
	s.mu.Unlock()
	if !ok || pe.size != st.Size() || !pe.mtime.Equal(st.ModTime()) {
		info, err := media.ProbeFile(filepath.Join(s.cfg.DataDir, name))
		if err != nil {
			info = media.Info{Kind: media.KindOther, Format: "other"}
		}
		pe = probeEntry{size: st.Size(), mtime: st.ModTime(), info: info}
		s.mu.Lock()
		s.probes[name] = pe
		s.mu.Unlock()
	}
	return fileInfo{
		Name: name, Size: st.Size(), Modified: st.ModTime().UnixMilli(),
		Kind: string(pe.info.Kind), Format: pe.info.Format, VideoCodec: pe.info.VideoCodec,
	}, nil
}

// lookup checks that name is a plain, visible file directly in the shared folder.
func (s *Server) lookup(name string) (string, fileInfo, bool) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return "", fileInfo{}, false
	}
	fi, err := s.describe(name)
	if err != nil {
		return "", fileInfo{}, false
	}
	return filepath.Join(s.cfg.DataDir, name), fi, true
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	files := []fileInfo{}
	seen := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || !e.Type().IsRegular() || isJunk(n) {
			continue
		}
		fi, err := s.describe(n)
		if err != nil {
			continue
		}
		seen[n] = true
		files = append(files, fi)
	}
	s.mu.Lock()
	for n := range s.probes {
		if !seen[n] {
			delete(s.probes, n)
		}
	}
	s.mu.Unlock()
	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified != files[j].Modified {
			return files[i].Modified > files[j].Modified
		}
		return files[i].Name < files[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func isJunk(name string) bool {
	switch strings.ToLower(name) {
	case "desktop.ini", "thumbs.db", ".ds_store":
		return true
	}
	return false
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
	path, fi, ok := s.lookup(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
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

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	path, _, ok := s.lookup(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func fileKey(fi fileInfo, extra string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", fi.Name, fi.Size, fi.Modified, extra)))
	return hex.EncodeToString(sum[:12])
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	path, fi, ok := s.lookup(r.PathValue("name"))
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
			return s.cfg.Tools.Thumbnail(ctx, path, thumb, info, 360)
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
		serve(path)
		return
	}
	http.NotFound(w, r)
}
