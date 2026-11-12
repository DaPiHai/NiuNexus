// Command genicons renders the NiuNexus package and desktop icons.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const scale = 4

func main() {
	out := flag.String("out", "niunexus", "application package directory")
	flag.Parse()

	targets := []struct {
		path string
		size int
	}{
		{filepath.Join(*out, "ICON.PNG"), 64},
		{filepath.Join(*out, "ICON_256.PNG"), 256},
		{filepath.Join(*out, "app", "ui", "images", "icon_64.png"), 64},
		{filepath.Join(*out, "app", "ui", "images", "icon_256.png"), 256},
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target.path), 0o755); err != nil {
			fatal(err)
		}
		if err := writePNG(target.path, target.size); err != nil {
			fatal(fmt.Errorf("%s: %w", target.path, err))
		}
		fmt.Println("icon:", target.path)
	}
}

func writePNG(path string, size int) error {
	img := render(size)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}

func render(size int) image.Image {
	big := image.NewRGBA(image.Rect(0, 0, size*scale, size*scale))
	drawBackground(big, size*scale)
	drawNetwork(big, size*scale)
	return downsample(big, size)
}

func drawBackground(img *image.RGBA, size int) {
	radius := float64(size) * 0.22
	inset := float64(size) * 0.045
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if !insideRoundedRect(fx, fy, inset, inset, float64(size)-inset, float64(size)-inset, radius) {
				continue
			}
			t := (fx + fy) / (2 * float64(size))
			base := lerpColor(color.RGBA{45, 212, 191, 255}, color.RGBA{56, 189, 248, 255}, t)
			// A soft vertical highlight makes the icon feel less flat.
			highlight := 1 - 0.16*(fy/float64(size))
			base.R = uint8(float64(base.R) * highlight)
			base.G = uint8(float64(base.G) * highlight)
			base.B = uint8(float64(base.B) * highlight)
			img.SetRGBA(x, y, base)
		}
	}
}

func drawNetwork(img *image.RGBA, size int) {
	points := []point{
		{0.30 * float64(size), 0.34 * float64(size)},
		{0.70 * float64(size), 0.34 * float64(size)},
		{0.50 * float64(size), 0.70 * float64(size)},
	}
	lineColor := color.RGBA{4, 22, 30, 200}
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			drawLine(img, points[i], points[j], float64(size)*0.032, lineColor)
		}
	}
	for _, p := range points {
		drawCircle(img, p, float64(size)*0.088, color.RGBA{248, 250, 252, 255})
		drawCircle(img, p, float64(size)*0.040, color.RGBA{8, 30, 40, 255})
	}
}

type point struct {
	x, y float64
}

func drawLine(img *image.RGBA, a, b point, width float64, c color.RGBA) {
	minX := int(math.Floor(math.Min(a.x, b.x) - width))
	maxX := int(math.Ceil(math.Max(a.x, b.x) + width))
	minY := int(math.Floor(math.Min(a.y, b.y) - width))
	maxY := int(math.Ceil(math.Max(a.y, b.y) + width))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
				continue
			}
			if distanceToSegment(float64(x)+0.5, float64(y)+0.5, a, b) <= width/2 {
				blend(img, x, y, c)
			}
		}
	}
}

func drawCircle(img *image.RGBA, center point, radius float64, c color.RGBA) {
	minX := int(math.Floor(center.x - radius))
	maxX := int(math.Ceil(center.x + radius))
	minY := int(math.Floor(center.y - radius))
	maxY := int(math.Ceil(center.y + radius))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
				continue
			}
			dx, dy := float64(x)+0.5-center.x, float64(y)+0.5-center.y
			if math.Hypot(dx, dy) <= radius {
				blend(img, x, y, c)
			}
		}
	}
}

func distanceToSegment(px, py float64, a, b point) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	if dx == 0 && dy == 0 {
		return math.Hypot(px-a.x, py-a.y)
	}
	t := ((px-a.x)*dx + (py-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(a.x+t*dx), py-(a.y+t*dy))
}

func insideRoundedRect(x, y, left, top, right, bottom, radius float64) bool {
	if x < left || x > right || y < top || y > bottom {
		return false
	}
	cx := math.Max(left+radius, math.Min(x, right-radius))
	cy := math.Max(top+radius, math.Min(y, bottom-radius))
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func blend(img *image.RGBA, x, y int, src color.RGBA) {
	dst := img.RGBAAt(x, y)
	a := float64(src.A) / 255
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(src.R)*a + float64(dst.R)*(1-a)),
		G: uint8(float64(src.G)*a + float64(dst.G)*(1-a)),
		B: uint8(float64(src.B)*a + float64(dst.B)*(1-a)),
		A: uint8(math.Max(float64(dst.A), float64(src.A))),
	})
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	return color.RGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: 255,
	}
}

func downsample(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a uint64
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					c := src.RGBAAt(x*scale+sx, y*scale+sy)
					r += uint64(c.R)
					g += uint64(c.G)
					b += uint64(c.B)
					a += uint64(c.A)
				}
			}
			n := uint64(scale * scale)
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / n)})
		}
	}
	return dst
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genicons:", err)
	os.Exit(1)
}
