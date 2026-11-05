package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thienanblog/ios-pc-android-transfer/internal/media"
)

const testToken = "test-token-123"

func newTestServer(t *testing.T, tools media.Tools) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{
		DataDir: dir,
		Token:   testToken,
		Tools:   tools,
		Version: "test",
		Web:     fstest.MapFS{"index.html": {Data: []byte("<html>ok</html>")}},
		URLs:    func() []string { return []string{"http://192.0.2.10:8686/?t=" + testToken} },
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, s.Handler(), dir
}

// do sends a request as a phone on the LAN that already has the cookie.
func do(t *testing.T, h http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.RemoteAddr = "192.0.2.50:5555"
	r.Host = "192.0.2.10:8686"
	r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	if method != http.MethodGet {
		r.Header.Set(csrfHeader, "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", w.Body.String(), err)
	}
	return m
}

func TestAccessControl(t *testing.T) {
	_, h, _ := newTestServer(t, media.Tools{})

	// A phone without the key is turned away.
	r := httptest.NewRequest("GET", "/api/files", nil)
	r.RemoteAddr = "192.0.2.50:1"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}

	// Wrong key.
	r = httptest.NewRequest("GET", "/?t=wrong", nil)
	r.RemoteAddr = "192.0.2.50:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}

	// Scanning the QR link sets the cookie and drops the key from the URL.
	r = httptest.NewRequest("GET", "/?t="+testToken, nil)
	r.RemoteAddr = "192.0.2.50:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("token link: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].Value != testToken || !c[0].HttpOnly {
		t.Fatalf("cookie not set: %v", c)
	}

	// This computer itself, via localhost, needs no key.
	r = httptest.NewRequest("GET", "/api/files", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Host = "localhost:8686"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("localhost: %d", w.Code)
	}

	// But not when a page uses another host name to reach localhost (DNS rebinding).
	r = httptest.NewRequest("GET", "/api/files", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Host = "evil.example:8686"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("rebinding: %d", w.Code)
	}

	// Changes need the custom header.
	r = httptest.NewRequest("POST", "/api/uploads", strings.NewReader(`{}`))
	r.RemoteAddr = "127.0.0.1:1"
	r.Host = "localhost:8686"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", w.Code)
	}
}

func TestInfoHidesLinksFromPhones(t *testing.T) {
	_, h, _ := newTestServer(t, media.Tools{})
	m := decode(t, do(t, h, "GET", "/api/info", nil))
	if _, ok := m["urls"]; ok {
		t.Fatal("phones must not get the links")
	}
	if _, ok := m["dataDir"]; ok {
		t.Fatal("phones must not get the folder path")
	}

	r := httptest.NewRequest("GET", "/api/info", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Host = "127.0.0.1:8686"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if m := decode(t, w); m["urls"] == nil {
		t.Fatalf("local info missing urls: %v", m)
	}

	r = httptest.NewRequest("GET", "/api/qr?i=0", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Host = "localhost"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("qr: %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/qr?i=0", nil); w.Code != http.StatusForbidden {
		t.Fatalf("qr from phone: %d", w.Code)
	}
}

func TestResumableUploadKeepsBytes(t *testing.T) {
	_, h, dir := newTestServer(t, media.Tools{})
	content, err := os.ReadFile(filepath.Join("..", "media", "testdata", "small.heic"))
	if err != nil {
		t.Fatal(err)
	}
	lastMod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
	initBody, _ := json.Marshal(map[string]any{"name": "IMG_0001.HEIC", "size": len(content), "lastModified": lastMod})

	m := decode(t, do(t, h, "POST", "/api/uploads", initBody))
	id := m["id"].(string)
	if m["offset"].(float64) != 0 {
		t.Fatalf("fresh upload offset %v", m["offset"])
	}

	// First chunk.
	w := do(t, h, "PUT", "/api/uploads/"+id+"?offset=0", content[:100])
	if w.Code != 200 || decode(t, w)["offset"].(float64) != 100 {
		t.Fatalf("chunk1: %d %s", w.Code, w.Body)
	}
	// The phone reconnects and asks again: the server remembers 100 bytes.
	if m := decode(t, do(t, h, "POST", "/api/uploads", initBody)); m["offset"].(float64) != 100 || m["id"] != id {
		t.Fatalf("resume: %v", m)
	}
	if m := decode(t, do(t, h, "GET", "/api/uploads/"+id, nil)); m["offset"].(float64) != 100 {
		t.Fatalf("status: %v", m)
	}
	// A chunk sent for the wrong offset is refused with the right offset.
	w = do(t, h, "PUT", "/api/uploads/"+id+"?offset=50", content[50:])
	if w.Code != http.StatusConflict || decode(t, w)["offset"].(float64) != 100 {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body)
	}
	// Too many bytes are refused and nothing is kept.
	w = do(t, h, "PUT", "/api/uploads/"+id+"?offset=100", append(append([]byte{}, content[100:]...), 'x'))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", w.Code, w.Body)
	}
	// Last chunk completes the file.
	w = do(t, h, "PUT", "/api/uploads/"+id+"?offset=100", content[100:])
	m = decode(t, w)
	if w.Code != 200 || m["done"] != true {
		t.Fatalf("final: %d %s", w.Code, w.Body)
	}
	file := m["file"].(map[string]any)
	if file["format"] != "heic" || file["name"] != "IMG_0001.HEIC" {
		t.Fatalf("file info: %v", file)
	}

	saved, err := os.ReadFile(filepath.Join(dir, "IMG_0001.HEIC"))
	if err != nil || !bytes.Equal(saved, content) {
		t.Fatalf("saved file differs from original (err %v)", err)
	}
	st, _ := os.Stat(filepath.Join(dir, "IMG_0001.HEIC"))
	if st.ModTime().UnixMilli() != lastMod {
		t.Fatalf("mtime %v", st.ModTime())
	}

	// Same name again gets a new name rather than overwriting.
	initBody2, _ := json.Marshal(map[string]any{"name": "IMG_0001.HEIC", "size": 3, "lastModified": 1})
	id2 := decode(t, do(t, h, "POST", "/api/uploads", initBody2))["id"].(string)
	m = decode(t, do(t, h, "PUT", "/api/uploads/"+id2+"?offset=0", []byte("abc")))
	if m["file"].(map[string]any)["name"] != "IMG_0001 (1).HEIC" {
		t.Fatalf("duplicate name: %v", m)
	}
}

func TestListDownloadDelete(t *testing.T) {
	_, h, dir := newTestServer(t, media.Tools{})
	src, _ := os.ReadFile(filepath.Join("..", "media", "testdata", "hevc.mov"))
	os.WriteFile(filepath.Join(dir, "clip.MOV"), src, 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "Ảnh đẹp.jpg"), []byte("\xFF\xD8\xFFjpeg"), 0o644)

	m := decode(t, do(t, h, "GET", "/api/files", nil))
	files := m["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("files: %v", files)
	}
	var clip map[string]any
	for _, f := range files {
		if f.(map[string]any)["name"] == "clip.MOV" {
			clip = f.(map[string]any)
		}
	}
	if clip == nil || clip["videoCodec"] != "hevc" || clip["kind"] != "video" {
		t.Fatalf("clip: %v", clip)
	}

	w := do(t, h, "GET", "/api/files/clip.MOV", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), src) {
		t.Fatalf("download: %d", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("disposition %q", cd)
	}
	w = do(t, h, "GET", "/api/files/"+"%E1%BA%A2nh%20%C4%91%E1%BA%B9p.jpg", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "filename*=utf-8''") {
		t.Fatalf("unicode download: %d %q", w.Code, w.Header().Get("Content-Disposition"))
	}

	for _, bad := range []string{".hidden", "..%2Fsecret", "nope.jpg", "%2E%2E", "a/../../etc/passwd", "sub%5C..%5Cx"} {
		// The router may answer a ".." path with a redirect to the cleaned
		// path instead; either way nothing is served.
		if w := do(t, h, "GET", "/api/files/"+bad, nil); w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}

	if w := do(t, h, "DELETE", "/api/files/clip.MOV", nil); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "clip.MOV")); !os.IsNotExist(err) {
		t.Fatal("file still there")
	}
}

func TestConvertWithoutTools(t *testing.T) {
	_, h, dir := newTestServer(t, media.Tools{})
	src, _ := os.ReadFile(filepath.Join("..", "media", "testdata", "small.heic"))
	os.WriteFile(filepath.Join(dir, "a.heic"), src, 0o644)
	m := decode(t, do(t, h, "POST", "/api/convert", []byte(`{"path":"a.heic","to":"jpg"}`)))
	if m["state"] != "error" || m["error"] != "image-tool-missing" {
		t.Fatalf("got %v", m)
	}
	w := do(t, h, "POST", "/api/convert", []byte(`{"path":"a.heic","to":"mp4"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("image to mp4: %d", w.Code)
	}
}

func TestConvertJobs(t *testing.T) {
	tools := media.DetectTools()
	if !tools.CanConvertImages() || !tools.CanConvertVideos() {
		t.Skip("converters not installed")
	}
	_, h, dir := newTestServer(t, tools)
	for _, f := range []string{"small.heic", "hevc.mov"} {
		src, _ := os.ReadFile(filepath.Join("..", "media", "testdata", f))
		os.WriteFile(filepath.Join(dir, f), src, 0o644)
	}
	for _, c := range []struct{ name, to, format string }{{"small.heic", "jpg", "jpeg"}, {"small.heic", "png", "png"}, {"hevc.mov", "mp4", "mp4"}} {
		body := []byte(`{"path":"` + c.name + `","to":"` + c.to + `"}`)
		var m map[string]any
		deadline := time.Now().Add(time.Minute)
		for {
			m = decode(t, do(t, h, "POST", "/api/convert", body))
			if m["state"] == "ready" || m["state"] == "error" || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if m["state"] != "ready" {
			t.Fatalf("%s->%s: %v", c.name, c.to, m)
		}
		w := do(t, h, "GET", m["url"].(string), nil)
		if w.Code != 200 {
			t.Fatalf("download converted: %d", w.Code)
		}
		info, _ := media.Probe(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if info.Format != c.format {
			t.Fatalf("%s->%s: got %+v", c.name, c.to, info)
		}
		if c.to == "mp4" && info.VideoCodec != "h264" {
			t.Fatalf("video codec %s", info.VideoCodec)
		}
		want := strings.TrimSuffix(c.name, filepath.Ext(c.name)) + "." + c.to
		if !strings.Contains(w.Header().Get("Content-Disposition"), want) {
			t.Fatalf("disposition %q", w.Header().Get("Content-Disposition"))
		}
	}

	w := do(t, h, "GET", "/api/thumbs/hevc.mov", nil)
	if w.Code != 200 {
		t.Fatalf("thumb: %d", w.Code)
	}
	if b, _ := io.ReadAll(w.Body); !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
		t.Fatal("thumb is not a jpeg")
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"IMG_0001.HEIC":         "IMG_0001.HEIC",
		"../../etc/passwd":      "passwd",
		`..\..\win.ini`:         "win.ini",
		"a:b*c?.jpg":            "a_b_c_.jpg",
		".bashrc":               "bashrc",
		"":                      "file",
		"..":                    "file",
		"CON.txt":               "_CON.txt",
		"Ảnh Đà Lạt.jpg":        "Ảnh Đà Lạt.jpg",
		"name\x00with\nctl.png": "namewithctl.png",
		"trailing. ":            "trailing",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q want %q", in, got, want)
		}
	}
	long := strings.Repeat("ả", 150) + ".jpg"
	if got := SafeName(long); len(got) > 200 || !strings.HasSuffix(got, ".jpg") {
		t.Errorf("long name: %d bytes", len(got))
	}
}

func TestFolders(t *testing.T) {
	_, h, dir := newTestServer(t, media.Tools{})

	// Create a folder, then upload into it.
	m := decode(t, do(t, h, "POST", "/api/folders", []byte(`{"dir":"","name":"Đà Lạt 2026"}`)))
	if m["path"] != "Đà Lạt 2026" {
		t.Fatalf("mkdir: %v", m)
	}
	if w := do(t, h, "POST", "/api/folders", []byte(`{"dir":"","name":"Đà Lạt 2026"}`)); w.Code != http.StatusConflict {
		t.Fatalf("duplicate mkdir: %d", w.Code)
	}
	if w := do(t, h, "POST", "/api/folders", []byte(`{"dir":"../x","name":"a"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("mkdir outside: %d", w.Code)
	}
	m = decode(t, do(t, h, "POST", "/api/folders", []byte(`{"dir":"Đà Lạt 2026","name":"../../evil"}`)))
	if m["path"] != "Đà Lạt 2026/evil" {
		t.Fatalf("nested mkdir name not cleaned: %v", m)
	}

	init, _ := json.Marshal(map[string]any{"dir": "Đà Lạt 2026", "name": "a.jpg", "size": 4, "lastModified": 1})
	id := decode(t, do(t, h, "POST", "/api/uploads", init))["id"].(string)
	m = decode(t, do(t, h, "PUT", "/api/uploads/"+id+"?offset=0", []byte("\xFF\xD8\xFF\x00")))
	if f := m["file"].(map[string]any); f["path"] != "Đà Lạt 2026/a.jpg" || f["format"] != "jpeg" {
		t.Fatalf("upload into folder: %v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "Đà Lạt 2026", "a.jpg")); err != nil {
		t.Fatal(err)
	}
	bad, _ := json.Marshal(map[string]any{"dir": "../outside", "name": "a.jpg", "size": 4})
	if w := do(t, h, "POST", "/api/uploads", bad); w.Code != http.StatusBadRequest {
		t.Fatalf("upload outside: %d", w.Code)
	}
	missing, _ := json.Marshal(map[string]any{"dir": "nope", "name": "a.jpg", "size": 4})
	if w := do(t, h, "POST", "/api/uploads", missing); w.Code != http.StatusNotFound {
		t.Fatalf("upload to missing folder: %d", w.Code)
	}

	// Listing the top shows the folder; listing inside shows the file.
	m = decode(t, do(t, h, "GET", "/api/files", nil))
	folders := m["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["items"].(float64) != 2 {
		t.Fatalf("top listing: %v", m)
	}
	m = decode(t, do(t, h, "GET", "/api/files?dir="+url.QueryEscape("Đà Lạt 2026"), nil))
	if len(m["files"].([]any)) != 1 || len(m["folders"].([]any)) != 1 {
		t.Fatalf("folder listing: %v", m)
	}
	if w := do(t, h, "GET", "/api/files?dir=..", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("list outside: %d", w.Code)
	}

	// Download by path.
	w := do(t, h, "GET", "/api/files/"+url.PathEscape("Đà Lạt 2026")+"/a.jpg", nil)
	if w.Code != 200 || w.Body.Len() != 4 {
		t.Fatalf("download in folder: %d", w.Code)
	}

	// A folder with files cannot be deleted; an empty one can.
	if w := do(t, h, "DELETE", "/api/files/"+url.PathEscape("Đà Lạt 2026"), nil); w.Code != http.StatusConflict {
		t.Fatalf("delete non-empty: %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/files/"+url.PathEscape("Đà Lạt 2026")+"/evil", nil); w.Code != 200 {
		t.Fatalf("delete empty: %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/files/"+url.PathEscape("Đà Lạt 2026")+"/a.jpg", nil); w.Code != 200 {
		t.Fatalf("delete file: %d", w.Code)
	}
	os.WriteFile(filepath.Join(dir, "Đà Lạt 2026", ".DS_Store"), []byte("x"), 0o644)
	if w := do(t, h, "DELETE", "/api/files/"+url.PathEscape("Đà Lạt 2026"), nil); w.Code != 200 {
		t.Fatalf("delete folder with only system files: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "DELETE", "/api/files/", nil); w.Code == 200 {
		t.Fatal("deleting the shared folder itself must fail")
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	_, h, dir := newTestServer(t, media.Tools{})
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.jpg"), []byte("\xFF\xD8\xFFsecret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks not available")
	}
	if w := do(t, h, "GET", "/api/files/link/secret.jpg", nil); w.Code != http.StatusNotFound {
		t.Fatalf("followed symlink: %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/files?dir=link", nil); w.Code == http.StatusOK {
		t.Fatalf("listed through symlink")
	}
}
