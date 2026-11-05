package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Uploads are resumable: the phone asks for an id, then sends the file in
// chunks with the byte offset of each chunk. If Wi-Fi drops, it asks how
// many bytes arrived and carries on from there. Bytes are written exactly as
// received, so the saved file is the phone's original.

type uploadMeta struct {
	Dir          string `json:"dir"` // folder inside the shared folder, "" for the top
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	LastModified int64  `json:"lastModified"` // milliseconds since 1970, from the browser
}

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	var m uploadMeta
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	m.Name = SafeName(m.Name)
	dir, ok := cleanRel(m.Dir)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad folder"})
		return
	}
	m.Dir = dir
	if abs, ok := s.resolve(dir); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad folder"})
		return
	} else if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if m.Size < 0 || m.Size > 1<<40 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad size"})
		return
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", m.Dir, m.Name, m.Size, m.LastModified)))
	id := hex.EncodeToString(sum[:16])

	l := s.lockFor(s.uploadMus, id)
	l.Lock()
	defer l.Unlock()

	metaPath, partPath := s.uploadPaths(id)
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	st, _ := f.Stat()
	f.Close()

	if m.Size == 0 {
		info, err := s.finishUpload(id, m)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": 0, "done": true, "file": info})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": st.Size()})
}

func (s *Server) uploadPaths(id string) (meta, part string) {
	return filepath.Join(s.partsDir(), id+".json"), filepath.Join(s.partsDir(), id+".part")
}

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	_, partPath := s.uploadPaths(id)
	st, err := os.Stat(partPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": st.Size()})
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad offset"})
		return
	}

	l := s.lockFor(s.uploadMus, id)
	l.Lock()
	defer l.Unlock()

	metaPath, partPath := s.uploadPaths(id)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload"})
		return
	}
	var m uploadMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bad upload record"})
		return
	}
	f, err := os.OpenFile(partPath, os.O_WRONLY, 0o644)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload"})
		return
	}
	st, _ := f.Stat()
	have := st.Size()
	if offset != have {
		f.Close()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "offset mismatch", "offset": have})
		return
	}
	limit := m.Size - have
	if limit > maxChunk {
		limit = maxChunk
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		f.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Read one byte more than allowed so an oversized chunk is caught.
	n, copyErr := io.Copy(f, io.LimitReader(r.Body, limit+1))
	if n > limit {
		_ = f.Truncate(have)
		f.Close()
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "chunk too large", "offset": have})
		return
	}
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	now := have + n
	if copyErr != nil {
		// Keep what arrived; the phone will ask for the offset and resume.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "upload interrupted", "offset": now})
		return
	}
	if now < m.Size {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": now})
		return
	}
	info, err := s.finishUpload(id, m)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": now, "done": true, "file": info})
}

func (s *Server) finishUpload(id string, m uploadMeta) (fileInfo, error) {
	metaPath, partPath := s.uploadPaths(id)
	dir, ok := s.resolve(m.Dir)
	if !ok {
		return fileInfo{}, errors.New("bad folder")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fileInfo{}, errors.New("the folder was deleted while uploading")
	}
	s.finishMu.Lock()
	dst, err := uniquePath(dir, m.Name)
	if err == nil {
		err = os.Rename(partPath, dst)
	}
	s.finishMu.Unlock()
	if err != nil {
		return fileInfo{}, err
	}
	os.Remove(metaPath)
	if m.LastModified > 0 {
		t := time.UnixMilli(m.LastModified)
		_ = os.Chtimes(dst, t, t)
	}
	return s.describe(path.Join(m.Dir, filepath.Base(dst)))
}

// SafeName turns a name sent by a phone into a plain file name that cannot
// escape the shared folder and is valid on Windows, macOS and Linux.
func SafeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base("/" + name)
	if name == "/" || name == "\\" || name == "." {
		name = ""
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return -1
		case strings.ContainsRune(`<>:"/\|?*`, r):
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimRight(name, ". "))
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "file"
	}
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = truncateUTF8(name[:len(name)-len(ext)], 200-len(ext)) + ext
	}
	return name
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// uniquePath returns dir/name, or dir/"name (1).ext" and so on if taken.
func uniquePath(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		p := filepath.Join(dir, n)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
	}
	return "", errors.New("too many files with this name")
}
