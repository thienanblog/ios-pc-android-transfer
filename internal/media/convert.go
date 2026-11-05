package media

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrNoConverter means no installed tool could do the job.
var ErrNoConverter = errors.New("no converter installed")

// ConvertImage turns src (usually HEIC) into dst. The output format follows
// dst's extension: .jpg or .png. It tries each installed tool in turn.
func (t Tools) ConvertImage(ctx context.Context, src, dst string) error {
	ext := strings.ToLower(filepath.Ext(dst))
	if ext != ".jpg" && ext != ".png" {
		return fmt.Errorf("unsupported output %q", ext)
	}
	type attempt struct {
		name string
		run  func(tmp string) error
	}
	var attempts []attempt
	if t.Sips != "" {
		attempts = append(attempts, attempt{"sips", func(tmp string) error {
			format := "jpeg"
			if ext == ".png" {
				format = "png"
			}
			return run(ctx, t.Sips, "-s", "format", format, "-s", "formatOptions", "90", src, "--out", tmp)
		}})
	}
	if t.Vips != "" {
		attempts = append(attempts, attempt{"vips", func(tmp string) error {
			out := tmp
			if ext == ".jpg" {
				out += "[Q=90]"
			}
			return run(ctx, t.Vips, "copy", src, out)
		}})
	}
	if t.Magick != "" {
		attempts = append(attempts, attempt{"imagemagick", func(tmp string) error {
			return run(ctx, t.Magick, src+"[0]", "-quality", "90", tmp)
		}})
	}
	if t.HeifConvert != "" {
		attempts = append(attempts, attempt{"heif-convert", func(tmp string) error {
			if err := run(ctx, t.HeifConvert, "-q", "90", src, tmp); err != nil {
				return err
			}
			// Files holding several top-level images are written as
			// name-1.jpg, name-2.jpg … ; keep the first one.
			if _, err := os.Stat(tmp); err == nil {
				return nil
			}
			base := strings.TrimSuffix(tmp, ext)
			matches, _ := filepath.Glob(base + "-*" + ext)
			if len(matches) == 0 {
				return errors.New("heif-convert wrote no file")
			}
			sort.Strings(matches)
			err := os.Rename(matches[0], tmp)
			for _, m := range matches[1:] {
				os.Remove(m)
			}
			return err
		}})
	}
	if t.FFmpeg != "" && t.FFmpegHEIC {
		attempts = append(attempts, attempt{"ffmpeg", func(tmp string) error {
			args := []string{"-hide_banner", "-v", "error", "-y", "-i", src, "-frames:v", "1"}
			if ext == ".jpg" {
				args = append(args, "-q:v", "2")
			}
			return run(ctx, t.FFmpeg, append(args, tmp)...)
		}})
	}
	if len(attempts) == 0 {
		return ErrNoConverter
	}

	var errs []string
	for _, a := range attempts {
		tmp, cleanup, err := tempBeside(dst, ext)
		if err != nil {
			return err
		}
		err = a.run(tmp)
		if err == nil {
			err = checkNonEmpty(tmp)
		}
		if err == nil {
			err = os.Rename(tmp, dst)
		}
		cleanup()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		errs = append(errs, a.name+": "+err.Error())
	}
	return fmt.Errorf("image conversion failed (%s)", strings.Join(errs, "; "))
}

// Thumbnail writes a small JPEG preview (about size pixels wide) of an image
// or video.
func (t Tools) Thumbnail(ctx context.Context, src, dst string, info Info, size int) error {
	tmp, cleanup, err := tempBeside(dst, ".jpg")
	if err != nil {
		return err
	}
	defer cleanup()
	s := strconv.Itoa(size)

	try := func(f func() error) bool {
		if err := f(); err == nil && checkNonEmpty(tmp) == nil {
			return os.Rename(tmp, dst) == nil
		}
		return false
	}
	if info.Kind == KindVideo {
		if t.FFmpeg == "" {
			return ErrNoConverter
		}
		for _, seek := range []string{"0.5", "0"} {
			if try(func() error {
				return run(ctx, t.FFmpeg, "-hide_banner", "-v", "error", "-y", "-ss", seek, "-i", src,
					"-frames:v", "1", "-vf", "scale="+s+":-2", "-q:v", "4", tmp)
			}) {
				return nil
			}
		}
		return errors.New("video thumbnail failed")
	}

	if t.Vips != "" && try(func() error { return run(ctx, t.Vips, "thumbnail", src, tmp+"[Q=80]", s) }) {
		return nil
	}
	if t.Sips != "" && try(func() error {
		return run(ctx, t.Sips, "-s", "format", "jpeg", "-Z", s, src, "--out", tmp)
	}) {
		return nil
	}
	if t.Magick != "" && try(func() error {
		return run(ctx, t.Magick, src+"[0]", "-thumbnail", s+"x"+s, "-quality", "80", tmp)
	}) {
		return nil
	}
	if t.FFmpeg != "" && (t.FFmpegHEIC || !info.IsHEIC()) && try(func() error {
		return run(ctx, t.FFmpeg, "-hide_banner", "-v", "error", "-y", "-i", src, "-frames:v", "1",
			"-vf", "scale="+s+":-2", "-q:v", "4", tmp)
	}) {
		return nil
	}
	// Last resort: a full-size JPEG is still a usable preview.
	if info.IsHEIC() && t.HeifConvert != "" {
		return t.ConvertImage(ctx, src, dst)
	}
	return ErrNoConverter
}

// VideoProgress is called with a value between 0 and 1 while converting.
type VideoProgress func(fraction float64)

// ConvertVideo re-encodes src to an H.264/AAC MP4 at dst, which every phone
// can play. iPhone HDR (HLG / Dolby Vision) is tone-mapped to SDR when the
// ffmpeg build has the zscale filter, so colours do not look washed out.
func (t Tools) ConvertVideo(ctx context.Context, src, dst string, progress VideoProgress) error {
	if t.FFmpeg == "" {
		return ErrNoConverter
	}
	duration, hdr := t.probeVideo(ctx, src)

	vf := "scale=trunc(iw/2)*2:trunc(ih/2)*2"
	if hdr && t.FFmpegZscale {
		vf = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0," +
			"zscale=t=bt709:m=bt709:r=tv,format=yuv420p," + vf
	}
	tmp, cleanup, err := tempBeside(dst, ".mp4")
	if err != nil {
		return err
	}
	defer cleanup()

	args := []string{
		"-hide_banner", "-v", "error", "-y", "-i", src,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-vf", vf,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "160k",
		"-map_metadata", "0", "-movflags", "+faststart",
		"-progress", "pipe:1", "-nostats",
		tmp,
	}
	cmd := exec.CommandContext(ctx, t.FFmpeg, args...)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "out_time_us="); ok && duration > 0 && progress != nil {
			if us, err := strconv.ParseFloat(v, 64); err == nil {
				f := us / 1e6 / duration
				if f > 0.99 {
					f = 0.99
				}
				if f >= 0 {
					progress(f)
				}
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, lastLine(stderr.String()))
	}
	if err := checkNonEmpty(tmp); err != nil {
		return err
	}
	if progress != nil {
		progress(1)
	}
	return os.Rename(tmp, dst)
}

// probeVideo returns the duration in seconds and whether the video is HDR.
func (t Tools) probeVideo(ctx context.Context, src string) (float64, bool) {
	if t.FFprobe == "" {
		return 0, false
	}
	cmd := exec.CommandContext(ctx, t.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "format=duration:stream=color_transfer", "-of", "default=nw=1", src)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	var dur float64
	hdr := false
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "duration":
			dur, _ = strconv.ParseFloat(v, 64)
		case "color_transfer":
			hdr = v == "arib-std-b67" || v == "smpte2084"
		}
	}
	return dur, hdr
}

func run(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func checkNonEmpty(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return errors.New("empty output")
	}
	return nil
}

// tempBeside returns a fresh temporary path in dst's folder with extension ext.
func tempBeside(dst, ext string) (string, func(), error) {
	f, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*"+ext)
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	f.Close()
	os.Remove(name) // tools refuse or behave oddly with an existing empty file
	return name, func() {
		os.Remove(name)
		base := strings.TrimSuffix(name, ext)
		if m, _ := filepath.Glob(base + "-*" + ext); len(m) > 0 {
			for _, p := range m {
				os.Remove(p)
			}
		}
	}, nil
}
