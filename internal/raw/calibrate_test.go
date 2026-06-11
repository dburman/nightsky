package raw

import "testing"

// rggb builds a small RGGB raw image from a row-major sample slice.
func rggb(w, h int, pix []uint16, white uint16) *Image {
	return &Image{
		Width: w, Height: h, Pix: pix, WhiteLevel: white,
		CFACols: 2, CFARows: 2,
		CFAPattern: []uint8{CFARed, CFAGreen, CFAGreen, CFABlue},
	}
}

func TestSubtractDark_Linear(t *testing.T) {
	frame := rggb(2, 2, []uint16{100, 200, 50, 300}, 1023)
	dark := rggb(2, 2, []uint16{10, 250, 50, 100}, 1023)

	if err := frame.SubtractDark(dark); err != nil {
		t.Fatal(err)
	}
	want := []uint16{90, 0, 0, 200} // clamp where dark >= frame
	for i := range want {
		if frame.Pix[i] != want[i] {
			t.Errorf("Pix[%d] = %d, want %d", i, frame.Pix[i], want[i])
		}
	}
}

func TestSubtractDark_SizeMismatch(t *testing.T) {
	frame := rggb(2, 2, []uint16{1, 2, 3, 4}, 1023)
	dark := rggb(1, 1, []uint16{1}, 1023)
	if err := frame.SubtractDark(dark); err == nil {
		t.Error("expected size-mismatch error")
	}
}

// Flat correction normalizes per CFA colour. In a 2x2 RGGB block the two green
// photosites share one colour mean; R and B each have a single sample (mean ==
// their own flat value, so they are unchanged).
func TestDivideFlat_PerColorNormalization(t *testing.T) {
	frame := rggb(2, 2, []uint16{400, 400, 400, 400}, 4095)
	// Layout: idx0=R(flat200), idx1=G(flat100), idx2=G(flat400), idx3=B(flat800).
	flat := rggb(2, 2, []uint16{200, 100, 400, 800}, 4095)

	if err := frame.DivideFlat(flat); err != nil {
		t.Fatal(err)
	}
	// R: mean200 → 400*200/200=400. B: mean800 → 400. G mean=(100+400)/2=250:
	// idx1=400*250/100=1000, idx2=400*250/400=250.
	want := []uint16{400, 1000, 250, 400}
	for i := range want {
		if frame.Pix[i] != want[i] {
			t.Errorf("Pix[%d] = %d, want %d", i, frame.Pix[i], want[i])
		}
	}
}

// With multiple samples per colour, vignette correction equalizes them.
func TestDivideFlat_EqualizesWithinColor(t *testing.T) {
	// 4x1 image, CFA row R G R G → colours R,G,R,G.
	im := &Image{
		Width: 4, Height: 1, WhiteLevel: 4095,
		CFACols: 2, CFARows: 1,
		CFAPattern: []uint8{CFARed, CFAGreen},
		Pix:        []uint16{100, 100, 100, 100},
	}
	flat := &Image{
		Width: 4, Height: 1, WhiteLevel: 4095,
		CFACols: 2, CFARows: 1,
		CFAPattern: []uint8{CFARed, CFAGreen},
		Pix:        []uint16{50, 200, 150, 100}, // R: 50,150 (mean 100); G: 200,100 (mean 150)
	}

	if err := im.DivideFlat(flat); err != nil {
		t.Fatal(err)
	}
	// R pixels: 100*100/50=200, 100*100/150=66; G: 100*150/200=75, 100*150/100=150.
	want := []uint16{200, 75, 66, 150}
	for i := range want {
		if im.Pix[i] != want[i] {
			t.Errorf("Pix[%d] = %d, want %d", i, im.Pix[i], want[i])
		}
	}
}

func TestDivideFlat_ClampsToWhite(t *testing.T) {
	// 4x1, CFA R G R G. Two R photosites with very uneven flat values so the
	// correction of the bright one overshoots and must clamp.
	im := &Image{
		Width: 4, Height: 1, WhiteLevel: 1023,
		CFACols: 2, CFARows: 1, CFAPattern: []uint8{CFARed, CFAGreen},
		Pix: []uint16{1000, 0, 0, 0},
	}
	flat := &Image{
		Width: 4, Height: 1, WhiteLevel: 1023,
		CFACols: 2, CFARows: 1, CFAPattern: []uint8{CFARed, CFAGreen},
		Pix: []uint16{10, 100, 1000, 100}, // R: 10,1000 → mean 505
	}
	if err := im.DivideFlat(flat); err != nil {
		t.Fatal(err)
	}
	// idx0 R: 1000*505/10 = 50500 → clamp to white 1023.
	if im.Pix[0] != 1023 {
		t.Errorf("expected clamp to white 1023, got %d", im.Pix[0])
	}
}
