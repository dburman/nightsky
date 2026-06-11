package raw

import "testing"

// cfaScene builds an RGGB image where every red photosite holds rv, green gv,
// blue bv — i.e. a uniform colour as seen through the mosaic.
func cfaScene(w, h int, rv, gv, bv uint16, white uint16) *Image {
	im := &Image{
		Width: w, Height: h, WhiteLevel: white,
		CFACols: 2, CFARows: 2,
		CFAPattern: []uint8{CFARed, CFAGreen, CFAGreen, CFABlue},
		Pix:        make([]uint16, w*h),
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch im.ColorAt(x, y) {
			case CFARed:
				im.Pix[y*w+x] = rv
			case CFAGreen:
				im.Pix[y*w+x] = gv
			case CFABlue:
				im.Pix[y*w+x] = bv
			}
		}
	}
	return im
}

// A uniform colour through the mosaic must debayer to that colour everywhere,
// including edges and corners.
func TestDebayer_UniformScene(t *testing.T) {
	im := cfaScene(8, 8, 1000, 500, 250, 1000)
	rgb := im.Debayer(DebayerOptions{Gamma: 1}) // linear, no WB

	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			i := rgb.PixOffset(x, y)
			r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
			// 1000/1000=255, 500/1000=127.5→127|128, 250/1000=63.75→64.
			if r != 255 {
				t.Fatalf("(%d,%d) R=%d, want 255", x, y, r)
			}
			if g < 127 || g > 128 {
				t.Fatalf("(%d,%d) G=%d, want ~127", x, y, g)
			}
			if b < 63 || b > 64 {
				t.Fatalf("(%d,%d) B=%d, want ~64", x, y, b)
			}
		}
	}
}

// White-balance gains scale R and B in linear space.
func TestDebayer_WhiteBalanceGains(t *testing.T) {
	im := cfaScene(4, 4, 200, 400, 100, 1000)
	rgb := im.Debayer(DebayerOptions{WBRed: 2.0, WBBlue: 4.0, Gamma: 1})

	i := rgb.PixOffset(1, 1)
	r, g, b := rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2]
	// R: 200/1000*2=0.4→102, G: 400/1000→102, B: 100/1000*4=0.4→102.
	for n, v := range map[string]uint8{"R": r, "G": g, "B": b} {
		if v < 101 || v > 103 {
			t.Errorf("%s = %d, want ~102", n, v)
		}
	}
}

// Gamma encoding brightens midtones: 0.25 linear at gamma 2.2 ≈ 0.532.
func TestDebayer_Gamma(t *testing.T) {
	im := cfaScene(4, 4, 250, 250, 250, 1000)
	rgb := im.Debayer(DebayerOptions{Gamma: 2.2})
	i := rgb.PixOffset(1, 1)
	g := rgb.Pix[i+1]
	// 0.25^(1/2.2)*255 ≈ 135.7
	if g < 133 || g > 138 {
		t.Errorf("gamma-encoded G = %d, want ~136", g)
	}
}

// Black level must be subtracted before normalization.
func TestDebayer_BlackLevel(t *testing.T) {
	im := cfaScene(4, 4, 64, 64, 64, 1024)
	im.BlackLevel = []float64{64}
	rgb := im.Debayer(DebayerOptions{Gamma: 1})
	i := rgb.PixOffset(1, 1)
	if rgb.Pix[i] != 0 || rgb.Pix[i+1] != 0 || rgb.Pix[i+2] != 0 {
		t.Errorf("black-level scene should debayer to 0, got (%d,%d,%d)",
			rgb.Pix[i], rgb.Pix[i+1], rgb.Pix[i+2])
	}
}
