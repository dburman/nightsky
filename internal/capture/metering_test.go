package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"testing"
)

// referenceZoneMean is the original image.At-based implementation, kept as the
// oracle for the allocation-free samplers. If these ever disagree, the fast
// path is wrong, not merely different.
func referenceZoneMean(img image.Image, zone string) float64 {
	b := img.Bounds()
	at := func(x, y int) float64 {
		r, g, bl, _ := img.At(x, y).RGBA()
		return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
	}

	var sum float64
	var count int
	switch zone {
	case "center":
		cx := float64(b.Min.X+b.Max.X) / 2
		cy := float64(b.Min.Y+b.Max.Y) / 2
		r := float64(min(b.Dx(), b.Dy())) * 0.5
		for y := b.Min.Y; y < b.Max.Y; y += 4 {
			dy := float64(y) - cy
			for x := b.Min.X; x < b.Max.X; x += 4 {
				dx := float64(x) - cx
				if math.Sqrt(dx*dx+dy*dy) > r {
					continue
				}
				sum += at(x, y)
				count++
			}
		}
	case "top":
		yMax := b.Min.Y + b.Dy()/3
		for y := b.Min.Y; y < yMax; y += 4 {
			for x := b.Min.X; x < b.Max.X; x += 4 {
				sum += at(x, y)
				count++
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y += 4 {
			for x := b.Min.X; x < b.Max.X; x += 4 {
				sum += at(x, y)
				count++
			}
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func meterSrc(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8((x * 3) % 256), uint8((y * 5) % 256), uint8((x*y)%256)/2 + 10, 255})
		}
	}
	return img
}

// meterVariants returns the image the capture path actually meters, per mode:
// rpicam-still's JPEG decodes to *image.YCbCr, its PNG to *image.RGBA. NRGBA
// and a non-standard type cover the fallback branches.
func meterVariants(t *testing.T, w, h int) map[string]image.Image {
	t.Helper()
	src := meterSrc(w, h)

	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	ycbcr, err := jpeg.Decode(bytes.NewReader(jb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	var pb bytes.Buffer
	if err := png.Encode(&pb, src); err != nil {
		t.Fatal(err)
	}
	rgba, err := png.Decode(bytes.NewReader(pb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	nrgba := image.NewNRGBA(src.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			nrgba.SetNRGBA(x, y, color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255})
		}
	}

	return map[string]image.Image{
		"YCbCr(jpeg)": ycbcr,
		"RGBA(png)":   rgba,
		"NRGBA":       nrgba,
		"fallback":    onlyAt{src},
	}
}

// onlyAt hides the concrete type so the generic At path is exercised.
type onlyAt struct{ img image.Image }

func (o onlyAt) ColorModel() color.Model { return o.img.ColorModel() }
func (o onlyAt) Bounds() image.Rectangle { return o.img.Bounds() }
func (o onlyAt) At(x, y int) color.Color { return o.img.At(x, y) }

// The allocation-free samplers must agree with the At-based oracle for every
// zone and every concrete type the pipeline produces.
func TestZoneMean_MatchesReference(t *testing.T) {
	for _, size := range []struct{ w, h int }{{64, 48}, {321, 241}, {640, 480}} {
		for name, img := range meterVariants(t, size.w, size.h) {
			for _, zone := range []string{"full", "center", "top", "bogus"} {
				want := referenceZoneMean(img, zone)
				got := ZoneMean(img, zone)
				if math.Abs(want-got) > 1e-9 {
					t.Errorf("%dx%d %s zone=%s: got %v, want %v (diff %g)",
						size.w, size.h, name, zone, got, want, want-got)
				}
			}
		}
	}
}

// Metering runs on every frame, so it must not allocate. This guards the
// regression that motivated the rewrite: image.At boxes a color.Color per
// pixel, which cost ~57k allocations per frame.
func TestZoneMean_DoesNotAllocate(t *testing.T) {
	for name, img := range meterVariants(t, 640, 480) {
		if name == "fallback" {
			continue // the generic path legitimately boxes
		}
		for _, zone := range []string{"full", "center", "top"} {
			allocs := testing.AllocsPerRun(3, func() { ZoneMean(img, zone) })
			if allocs > 0 {
				t.Errorf("%s zone=%s allocated %.0f times per call, want 0", name, zone, allocs)
			}
		}
	}
}

func BenchmarkZoneMean(b *testing.B) {
	src := meterSrc(1920, 1080)

	var jb bytes.Buffer
	jpeg.Encode(&jb, src, &jpeg.Options{Quality: 95})
	ycbcr, _ := jpeg.Decode(bytes.NewReader(jb.Bytes()))

	for _, tc := range []struct {
		name string
		img  image.Image
	}{
		{"RGBA(night)", src},
		{"YCbCr(day)", ycbcr},
	} {
		for _, zone := range []string{"full", "center"} {
			b.Run(tc.name+"/"+zone, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = ZoneMean(tc.img, zone)
				}
			})
		}
	}
}

func BenchmarkZoneMean_Reference(b *testing.B) {
	src := meterSrc(1920, 1080)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = referenceZoneMean(src, "center")
	}
}
