package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Tools holds the paths of the external programs we found. Empty means missing.
type Tools struct {
	FFmpeg      string `json:"ffmpeg,omitempty"`
	FFprobe     string `json:"ffprobe,omitempty"`
	Vips        string `json:"vips,omitempty"`
	Magick      string `json:"magick,omitempty"`
	HeifConvert string `json:"heifConvert,omitempty"`
	Sips        string `json:"sips,omitempty"`

	FFmpegVersion string `json:"ffmpegVersion,omitempty"`
	// FFmpegHEIC is true when ffmpeg is new enough (7.1+) to read HEIC photos.
	FFmpegHEIC bool `json:"ffmpegHeic"`
	// FFmpegZscale is true when ffmpeg has the zscale filter, used to tone-map
	// iPhone HDR video to normal (SDR) colours.
	FFmpegZscale bool `json:"ffmpegZscale"`
	// magickLegacy is true when Magick points to ImageMagick 6 "convert".
	magickLegacy bool
}

// CanConvertImages reports whether at least one HEIC decoder is available.
func (t Tools) CanConvertImages() bool {
	return t.Sips != "" || t.Vips != "" || t.Magick != "" || t.HeifConvert != "" || (t.FFmpeg != "" && t.FFmpegHEIC)
}

// CanConvertVideos reports whether ffmpeg is available.
func (t Tools) CanConvertVideos() bool { return t.FFmpeg != "" }

// DetectTools looks for the converters on PATH and in extraDirs (for example
// the folder that holds the photobridge executable, so a user can simply drop
// ffmpeg.exe next to it).
func DetectTools(extraDirs ...string) Tools {
	find := func(name string) string {
		exe := name
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		for _, d := range extraDirs {
			for _, p := range []string{filepath.Join(d, exe), filepath.Join(d, "bin", exe), filepath.Join(d, "ffmpeg", "bin", exe)} {
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p
				}
			}
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}

	t := Tools{
		FFmpeg:      find("ffmpeg"),
		FFprobe:     find("ffprobe"),
		Vips:        find("vips"),
		HeifConvert: find("heif-convert"),
	}
	if runtime.GOOS == "darwin" {
		t.Sips = find("sips")
	}
	if p := find("magick"); p != "" {
		t.Magick = p
	} else if runtime.GOOS != "windows" {
		// Never call "convert" on Windows: that name belongs to a system
		// tool that converts disks. On Linux/macOS check it is ImageMagick 6.
		if p := find("convert"); p != "" && strings.Contains(runOut(p, "-version"), "ImageMagick") {
			t.Magick = p
			t.magickLegacy = true
		}
	}
	if t.FFmpeg != "" {
		t.FFmpegVersion = ParseFFmpegVersion(runOut(t.FFmpeg, "-hide_banner", "-version"))
		t.FFmpegHEIC = FFmpegSupportsHEIC(t.FFmpegVersion)
		t.FFmpegZscale = strings.Contains(runOut(t.FFmpeg, "-hide_banner", "-filters"), " zscale ")
	}
	return t
}

func runOut(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

var ffVersionRe = regexp.MustCompile(`(?m)^ffmpeg version (\S+)`)

// ParseFFmpegVersion returns the version token from `ffmpeg -version` output,
// for example "7.1.1", "n9.0.2-17-g2a571b6068-20260930" or "6.1.1-3ubuntu5".
func ParseFFmpegVersion(out string) string {
	m := ffVersionRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

var numVersionRe = regexp.MustCompile(`^n?(\d+)\.(\d+)`)

// FFmpegSupportsHEIC reports whether this ffmpeg version can decode HEIC
// still images (added in 7.0, tiled iPhone photos in 7.1). Development builds
// ("N-12345-g…") are assumed to be recent.
func FFmpegSupportsHEIC(version string) bool {
	if version == "" {
		return false
	}
	if strings.HasPrefix(version, "N-") || strings.HasPrefix(version, "git") {
		return true
	}
	m := numVersionRe.FindStringSubmatch(version)
	if m == nil {
		return true
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 7 || (major == 7 && minor >= 1)
}
