// Package media detects photo and video formats and converts them with
// external tools (ffmpeg, libvips, ImageMagick, libheif, sips) found at runtime.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// Kind is the broad type of a file.
type Kind string

const (
	KindImage Kind = "image"
	KindVideo Kind = "video"
	KindOther Kind = "other"
)

// Info describes what a file really is, judged from its bytes, not its name.
type Info struct {
	Kind       Kind   `json:"kind"`
	Format     string `json:"format"`               // jpeg, png, gif, webp, heic, avif, mov, mp4, other
	VideoCodec string `json:"videoCodec,omitempty"` // hevc, h264, other (videos only)
}

// IsHEIC reports whether the image uses a format that many
// browsers and Android devices cannot open (HEIC/HEIF).
func (i Info) IsHEIC() bool { return i.Kind == KindImage && i.Format == "heic" }

// IsHEVC reports whether the video is encoded with HEVC (H.265).
func (i Info) IsHEVC() bool { return i.Kind == KindVideo && i.VideoCodec == "hevc" }

var heifBrands = map[string]bool{
	"heic": true, "heix": true, "heim": true, "heis": true,
	"hevc": true, "hevx": true, "hevm": true, "hevs": true,
	"mif1": true, "msf1": true, "mif2": true,
}

// ProbeFile opens path and calls Probe.
func ProbeFile(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	return Probe(f, st.Size())
}

// Probe inspects the first bytes (and, for ISO-BMFF files, the box tree) to
// find the real format.
func Probe(r io.ReaderAt, size int64) (Info, error) {
	head := make([]byte, 64)
	n, err := r.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Info{}, err
	}
	head = head[:n]

	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return Info{Kind: KindImage, Format: "jpeg"}, nil
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return Info{Kind: KindImage, Format: "png"}, nil
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return Info{Kind: KindImage, Format: "gif"}, nil
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return Info{Kind: KindImage, Format: "webp"}, nil
	}

	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		return probeISOBMFF(r, size, head)
	}
	return Info{Kind: KindOther, Format: "other"}, nil
}

func probeISOBMFF(r io.ReaderAt, size int64, head []byte) (Info, error) {
	boxSize := int64(binary.BigEndian.Uint32(head[0:4]))
	if boxSize < 16 || boxSize > 4096 {
		boxSize = 16
	}
	ftyp := make([]byte, boxSize)
	if _, err := r.ReadAt(ftyp, 0); err != nil && !errors.Is(err, io.EOF) {
		return Info{}, err
	}
	major := string(ftyp[8:12])
	brands := []string{major}
	for off := 16; off+4 <= len(ftyp); off += 4 {
		brands = append(brands, string(ftyp[off:off+4]))
	}

	isAVIF, isHEIF := false, false
	for _, b := range brands {
		if b == "avif" || b == "avis" {
			isAVIF = true
		}
		if heifBrands[b] {
			isHEIF = true
		}
	}
	// A still image has a top-level "meta" box but no "moov" box.
	hasMoov := false
	_ = walkBoxes(r, 0, size, func(typ string, _, _ int64) bool {
		if typ == "moov" {
			hasMoov = true
			return false
		}
		return true
	})
	if !hasMoov {
		switch {
		case isAVIF:
			return Info{Kind: KindImage, Format: "avif"}, nil
		case isHEIF:
			return Info{Kind: KindImage, Format: "heic"}, nil
		}
	}

	format := "mp4"
	if major == "qt  " {
		format = "mov"
	}
	if !hasMoov && !isHEIF && !isAVIF {
		return Info{Kind: KindOther, Format: "other"}, nil
	}
	codec := videoCodec(r, size)
	return Info{Kind: KindVideo, Format: format, VideoCodec: codec}, nil
}

// walkBoxes calls fn for each box in [start, end). fn returns false to stop.
func walkBoxes(r io.ReaderAt, start, end int64, fn func(typ string, payload, boxEnd int64) bool) error {
	off := start
	hdr := make([]byte, 16)
	for off+8 <= end {
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return err
		}
		sz := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		payload := off + 8
		switch sz {
		case 0:
			sz = end - off
		case 1:
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return err
			}
			sz = int64(binary.BigEndian.Uint64(hdr[8:16]))
			payload = off + 16
		}
		if sz < 8 || off+sz > end {
			return errors.New("bad box size")
		}
		if !fn(typ, payload, off+sz) {
			return nil
		}
		off += sz
	}
	return nil
}

var containerBoxes = map[string]bool{"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true}

// videoCodec looks at every track's sample description (stsd) and returns
// "hevc" if any track is HEVC/Dolby Vision, "h264" for AVC, or "other".
func videoCodec(r io.ReaderAt, size int64) string {
	var entries []string
	var visit func(start, end int64)
	visit = func(start, end int64) {
		_ = walkBoxes(r, start, end, func(typ string, payload, boxEnd int64) bool {
			if containerBoxes[typ] {
				visit(payload, boxEnd)
			} else if typ == "stsd" {
				// full box: version+flags (4), entry_count (4), then sample entries
				buf := make([]byte, 8)
				if _, err := r.ReadAt(buf, payload); err != nil {
					return true
				}
				_ = walkBoxes(r, payload+8, boxEnd, func(t string, _, _ int64) bool {
					entries = append(entries, t)
					return true
				})
			}
			return true
		})
	}
	visit(0, size)

	codec := ""
	for _, e := range entries {
		switch e {
		case "hvc1", "hev1", "dvh1", "dvhe":
			return "hevc"
		case "avc1", "avc3", "dva1", "dvav":
			codec = "h264"
		}
	}
	if codec == "" {
		return "other"
	}
	return codec
}
