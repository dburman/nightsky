package raw

import "testing"

func TestGray16RoundTrip(t *testing.T) {
	im := &Image{Width: 3, Height: 2, Pix: []uint16{0, 1, 255, 256, 1023, 65535}}
	got := FromGray16(im.ToGray16())
	if got.Width != 3 || got.Height != 2 {
		t.Fatalf("dimensions = %dx%d, want 3x2", got.Width, got.Height)
	}
	for i, v := range im.Pix {
		if got.Pix[i] != v {
			t.Errorf("Pix[%d] = %d, want %d", i, got.Pix[i], v)
		}
	}
}

func TestStackMedian_RejectsOutlier(t *testing.T) {
	mk := func(v uint16) *Image {
		return &Image{Width: 2, Height: 1, Pix: []uint16{v, v}}
	}
	contaminated := mk(10)
	contaminated.Pix[1] = 4000 // cosmic-ray hit

	master, err := StackMedian([]*Image{mk(10), mk(10), contaminated})
	if err != nil {
		t.Fatal(err)
	}
	if master.Pix[1] != 10 {
		t.Errorf("outlier survived: Pix[1] = %d, want 10", master.Pix[1])
	}
}

func TestStackMedian_MeanBelowThree(t *testing.T) {
	a := &Image{Width: 1, Height: 1, Pix: []uint16{100}}
	b := &Image{Width: 1, Height: 1, Pix: []uint16{300}}
	master, err := StackMedian([]*Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if master.Pix[0] != 200 {
		t.Errorf("two-frame stack = %d, want mean 200", master.Pix[0])
	}
}

func TestStackMedian_Errors(t *testing.T) {
	if _, err := StackMedian(nil); err == nil {
		t.Error("expected error for empty stack")
	}
	a := &Image{Width: 2, Height: 1, Pix: make([]uint16, 2)}
	b := &Image{Width: 1, Height: 1, Pix: make([]uint16, 1)}
	if _, err := StackMedian([]*Image{a, b}); err == nil {
		t.Error("expected error for mismatched sizes")
	}
}
