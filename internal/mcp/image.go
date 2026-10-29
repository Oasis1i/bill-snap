package mcp

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP so bill screenshots decode without cgo
)

const (
	maxImageSide   = 1024
	maxImageBytes  = 32 << 20
	maxImageEdge   = 32768
	maxImagePixels = 20_000_000
	jpegQuality    = 85
)

var (
	errNoImage = errors.New("bill has no image")
	errMissing = errors.New("bill image missing")
	errEscape  = errors.New("bill image escapes upload dir")
	errDecode  = errors.New("bill image undecodable")
)

// resolveBillImage returns the real path of a screenshot that is a regular
// file inside uploadDir. Symlinks are followed. A path that resolves outside
// uploadDir, including a symlink that leaves the directory, is errEscape.
// An empty imagePath is errNoImage. A path inside the directory that is
// missing or not a regular file is errMissing. uploadDir itself must exist.
func resolveBillImage(uploadDir, imagePath string) (string, error) {
	imagePath = strings.TrimSpace(imagePath)
	if imagePath == "" {
		return "", errNoImage
	}
	uploadDir = strings.TrimSpace(uploadDir)
	if uploadDir == "" {
		return "", errMissing
	}
	root, err := filepath.Abs(uploadDir)
	if err != nil {
		return "", errMissing
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", errMissing
	}
	candidate, err := filepath.Abs(imagePath)
	if err != nil {
		return "", errMissing
	}
	resolved, err := evalPath(candidate)
	if err != nil {
		return "", errMissing
	}
	switch classifyPath(root, resolved) {
	case pathEscape:
		return "", errEscape
	case pathNotFile:
		return "", errMissing
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return "", errMissing
	}
	return resolved, nil
}

func hasBillImage(uploadDir, imagePath string) bool {
	_, err := resolveBillImage(uploadDir, imagePath)
	return err == nil
}

type pathClass int

const (
	pathInside pathClass = iota
	pathEscape
	pathNotFile
)

func classifyPath(root, path string) pathClass {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return pathEscape
	}
	if rel == "" || rel == "." {
		return pathNotFile
	}
	return pathInside
}

// evalPath resolves symlinks. When the final component is missing it still
// resolves the nearest existing parent, so a missing file under a symlink
// that points outside the upload directory is classified as an escape
// instead of a simple absence.
func evalPath(path string) (string, error) {
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return filepath.EvalSymlinks(path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	rest := []string{filepath.Base(path)}
	parent := filepath.Dir(path)
	for parent != path {
		if _, err := os.Lstat(parent); err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			for i, j := 0, len(rest)-1; i < j; i, j = i+1, j-1 {
				rest[i], rest[j] = rest[j], rest[i]
			}
			parts := make([]string, 0, 1+len(rest))
			parts = append(parts, resolved)
			parts = append(parts, rest...)
			return filepath.Clean(filepath.Join(parts...)), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		rest = append(rest, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		path = parent
		parent = next
	}
	return "", os.ErrNotExist
}

func readBillImage(uploadDir, imagePath string) ([]byte, error) {
	path, err := resolveBillImage(uploadDir, imagePath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMissing
		}
		return nil, errMissing
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errMissing
	}
	if fi.Size() > maxImageBytes {
		return nil, errDecode
	}
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return nil, errDecode
	}
	if int64(len(data)) > maxImageBytes {
		return nil, errDecode
	}
	return data, nil
}

// encodeBillImage shrinks the longest side to maxImageSide and returns PNG
// or JPEG. Images that are already small enough are not enlarged. PNG and
// JPEG at or under the limit are returned unchanged. Other decodable
// formats (WebP) are re-encoded: opaque as JPEG, anything with alpha as PNG.
func encodeBillImage(data []byte) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", errDecode
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", errDecode
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxImageEdge || cfg.Height > maxImageEdge {
		return nil, "", errDecode
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, "", errDecode
	}
	nw, nh, resized := fitSide(cfg.Width, cfg.Height, maxImageSide)
	if !resized && (format == "png" || format == "jpeg") {
		if format == "png" {
			return data, "image/png", nil
		}
		return data, "image/jpeg", nil
	}
	img, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errDecode
	}
	if decoded != "" {
		format = decoded
	}
	out := img
	if resized {
		canvas := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(canvas, canvas.Bounds(), img, img.Bounds(), draw.Src, nil)
		out = canvas
	}
	var buf bytes.Buffer
	if usePNG(format, img) {
		if err := png.Encode(&buf, out); err != nil {
			return nil, "", errDecode
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, "", errDecode
	}
	return buf.Bytes(), "image/jpeg", nil
}

func usePNG(format string, img image.Image) bool {
	switch format {
	case "png":
		return true
	case "jpeg":
		return false
	}
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return true
}

// fitSide scales w,h so the longest side is maxSide. The shorter side is
// rounded half up and never exceeds maxSide. Values already within the
// limit are returned unchanged so callers can skip a resample.
func fitSide(w, h, maxSide int) (nw, nh int, resized bool) {
	if w <= maxSide && h <= maxSide {
		return w, h, false
	}
	if w >= h {
		nw = maxSide
		nh = int((int64(h)*int64(maxSide) + int64(w)/2) / int64(w))
		if nh < 1 {
			nh = 1
		}
		if nh > maxSide {
			nh = maxSide
		}
		return nw, nh, true
	}
	nh = maxSide
	nw = int((int64(w)*int64(maxSide) + int64(h)/2) / int64(h))
	if nw < 1 {
		nw = 1
	}
	if nw > maxSide {
		nw = maxSide
	}
	return nw, nh, true
}
