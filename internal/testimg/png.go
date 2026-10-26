// Package testimg builds small PNGs for tests and the smoke script.
package testimg

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// NoisyPNG is a gradient plus a dark block. Large enough copies are treated
// as bills by the mock extractor. seed changes the bytes so two calls differ.
func NoisyPNG(w, h, seed int) ([]byte, error) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	denX := w - 1
	if denX < 1 {
		denX = 1
	}
	denY := h - 1
	if denY < 1 {
		denY = 1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x * 255) / denX),
				G: uint8((y * 255) / denY),
				B: uint8((seed * 40) % 256),
				A: 255,
			})
		}
	}
	x0 := (seed * 17) % max(w-10, 1)
	y0 := (seed * 13) % max(h-10, 1)
	for y := y0; y < y0+8 && y < h; y++ {
		for x := x0; x < x0+20 && x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SolidPNG is an opaque white rectangle. The mock extractor rejects it as a
// non-bill when both sides are at least 64 pixels.
func SolidPNG(w, h int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
