package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeFixtures(t *testing.T) {
	cases := []struct {
		file string
		want Info
		heic bool
		hevc bool
	}{
		{"small.heic", Info{Kind: KindImage, Format: "heic"}, true, false},
		{"hevc.mov", Info{Kind: KindVideo, Format: "mov", VideoCodec: "hevc"}, false, true},
		{"h264.mp4", Info{Kind: KindVideo, Format: "mp4", VideoCodec: "h264"}, false, false},
	}
	for _, c := range cases {
		got, err := ProbeFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v want %+v", c.file, got, c.want)
		}
		if got.IsHEIC() != c.heic || got.IsHEVC() != c.hevc {
			t.Errorf("%s: IsHEIC=%v IsHEVC=%v", c.file, got.IsHEIC(), got.IsHEVC())
		}
	}
}

func TestProbeMagicBytes(t *testing.T) {
	cases := map[string]string{
		"jpeg":  "\xFF\xD8\xFF\xE0rest",
		"png":   "\x89PNG\r\n\x1a\nrest",
		"gif":   "GIF89a....",
		"webp":  "RIFF\x00\x00\x00\x00WEBPVP8 ",
		"other": "hello world, not media",
	}
	for want, data := range cases {
		got, err := Probe(bytes.NewReader([]byte(data)), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if got.Format != want {
			t.Errorf("%q: got %s want %s", data, got.Format, want)
		}
	}
}

func TestProbeAVIFIsNotHEIC(t *testing.T) {
	// ftyp avif with mif1 compatible brand, followed by an empty meta box.
	data := []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif\x00\x00\x00\x08meta")
	got, err := Probe(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "avif" || got.IsHEIC() {
		t.Fatalf("got %+v", got)
	}
}

func TestProbeTruncatedDoesNotPanic(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "hevc.mov"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 5, 12, 40, 200, len(raw) / 2} {
		_, _ = Probe(bytes.NewReader(raw[:n]), int64(n))
	}
}

func TestFFmpegVersion(t *testing.T) {
	cases := []struct {
		out     string
		version string
		heic    bool
	}{
		{"ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023", "6.1.1-3ubuntu5", false},
		{"ffmpeg version 7.0.2-static https://johnvansickle.com", "7.0.2-static", false},
		{"ffmpeg version 7.1.1-full_build-www.gyan.dev Copyright", "7.1.1-full_build-www.gyan.dev", true},
		{"ffmpeg version n9.0.2-17-g2a571b6068-20260930 Copyright", "n9.0.2-17-g2a571b6068-20260930", true},
		{"ffmpeg version N-118000-gabc Copyright", "N-118000-gabc", true},
		{"ffmpeg version 8.0 Copyright", "8.0", true},
		{"something else", "", false},
	}
	for _, c := range cases {
		v := ParseFFmpegVersion(c.out)
		if v != c.version {
			t.Errorf("%q: version %q want %q", c.out, v, c.version)
		}
		if got := FFmpegSupportsHEIC(v); got != c.heic {
			t.Errorf("%q: heic %v want %v", v, got, c.heic)
		}
	}
}

// The tests below run only when the converters are installed.

func TestConvertImageIntegration(t *testing.T) {
	tools := DetectTools()
	if !tools.CanConvertImages() {
		t.Skip("no HEIC converter installed")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, ext := range []string{".jpg", ".png"} {
		dst := filepath.Join(dir, "out"+ext)
		if err := tools.ConvertImage(ctx, filepath.Join("testdata", "small.heic"), dst); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		info, err := ProbeFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{".jpg": "jpeg", ".png": "png"}[ext]
		if info.Format != want {
			t.Fatalf("%s: got format %s", ext, info.Format)
		}
	}
	thumb := filepath.Join(dir, "thumb.jpg")
	if err := tools.Thumbnail(ctx, filepath.Join("testdata", "small.heic"), thumb, Info{Kind: KindImage, Format: "heic"}, 32); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, dir)
}

func TestConvertVideoIntegration(t *testing.T) {
	tools := DetectTools()
	if !tools.CanConvertVideos() {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dst := filepath.Join(dir, "out.mp4")
	var last float64
	if err := tools.ConvertVideo(ctx, filepath.Join("testdata", "hevc.mov"), dst, func(f float64) { last = f }); err != nil {
		t.Fatal(err)
	}
	info, err := ProbeFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindVideo || info.VideoCodec != "h264" {
		t.Fatalf("got %+v", info)
	}
	if last != 1 {
		t.Fatalf("progress ended at %v", last)
	}
	thumb := filepath.Join(dir, "thumb.jpg")
	if err := tools.Thumbnail(ctx, filepath.Join("testdata", "hevc.mov"), thumb, Info{Kind: KindVideo}, 64); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, dir)
}

func TestConvertImageNoTools(t *testing.T) {
	var tools Tools
	err := tools.ConvertImage(context.Background(), "x.heic", filepath.Join(t.TempDir(), "x.jpg"))
	if err != ErrNoConverter {
		t.Fatalf("got %v", err)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(m) > 0 {
		t.Fatalf("temporary files left: %v", m)
	}
}
