//go:build ignore

// Draws the toolbar icon at the four sizes Chrome asks for: a blue rounded
// square with a white tick. Run from the repository root:
//
//	go run extension/icons/gen.go
//
// The PNGs are committed so a build needs no image tooling.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

var (
	blue  = color.NRGBA{0x1f, 0x5f, 0xbf, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// The tick as two segments on a unit square, and the stroke half-width.
var tick = [][4]float64{{0.27, 0.53, 0.43, 0.69}, {0.43, 0.69, 0.74, 0.34}}

const (
	radius    = 0.22
	halfWidth = 0.075
	samples   = 4
)

func inRoundedSquare(x, y float64) bool {
	dx := math.Max(math.Abs(x-0.5)-(0.5-radius), 0)
	dy := math.Max(math.Abs(y-0.5)-(0.5-radius), 0)
	return dx*dx+dy*dy <= radius*radius
}

func nearSegment(x, y float64, s [4]float64) bool {
	vx, vy := s[2]-s[0], s[3]-s[1]
	t := math.Max(0, math.Min(1, ((x-s[0])*vx+(y-s[1])*vy)/(vx*vx+vy*vy)))
	return math.Hypot(x-(s[0]+t*vx), y-(s[1]+t*vy)) <= halfWidth
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/samples) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/samples) / float64(size)
					if !inRoundedSquare(x, y) {
						continue
					}
					c := blue
					if nearSegment(x, y, tick[0]) || nearSegment(x, y, tick[1]) {
						c = white
					}
					r, g, b, a = r+float64(c.R), g+float64(c.G), b+float64(c.B), a+255
				}
			}
			if a == 0 {
				continue
			}
			n := float64(samples * samples)
			img.SetNRGBA(px, py, color.NRGBA{uint8(r * 255 / a), uint8(g * 255 / a), uint8(b * 255 / a), uint8(a / n)})
		}
	}
	return img
}

func main() {
	for _, size := range []int{16, 32, 48, 128} {
		f, err := os.Create(filepath.Join("extension", "icons", fmt.Sprintf("icon-%d.png", size)))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(f, render(size)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
	}
}
