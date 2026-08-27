package raw

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildSyntheticDNG writes a minimal little-endian DNG with one IFD0 holding an
// uncompressed 16-bit CFA image (RGGB, white level 1023, black level 64). The
// layout mirrors what rpicam-still emits, minus the thumbnail/SubIFD nesting.
func buildSyntheticDNG(w, h int, fill uint16) []byte {
	bo := binary.LittleEndian

	// Pixel strip first, then IFD; header points at the IFD.
	const headerLen = 8
	stripOff := headerLen
	strip := make([]byte, w*h*2)
	for i := 0; i < w*h; i++ {
		bo.PutUint16(strip[i*2:], fill)
	}

	type ent struct {
		tag, typ uint16
		count    uint32
		val      uint32
	}
	// Out-of-line values (CFAPattern, BlackLevel, CFARepeatPatternDim) get
	// appended after the IFD; compute their offsets once the IFD size is known.
	entries := []ent{
		{tagNewSubfileType, 4, 1, 0},
		{tagImageWidth, 3, 1, uint32(w)},
		{tagImageLength, 3, 1, uint32(h)},
		{tagBitsPerSample, 3, 1, 16},
		{tagCompression, 3, 1, 1},
		{tagPhotometricInterpretation, 3, 1, photometricCFA},
		{tagStripOffsets, 4, 1, uint32(stripOff)},
		{tagSamplesPerPixel, 3, 1, 1},
		{tagStripByteCounts, 4, 1, uint32(len(strip))},
		{tagCFARepeatPatternDim, 3, 2, 0}, // value filled below (inline: 2,2)
		// CFAPattern is 4 BYTEs (RGGB) — fits inline in the value field.
		{tagCFAPattern, 1, 4, uint32(CFARed) | uint32(CFAGreen)<<8 | uint32(CFAGreen)<<16 | uint32(CFABlue)<<24},
		{tagWhiteLevel, 4, 1, 1023},
		{tagBlackLevel, 5, 1, 0}, // 8-byte rational, stored out of line below
	}
	nEntries := len(entries)
	ifdOff := stripOff + len(strip)
	ifdLen := 2 + nEntries*12 + 4

	// BlackLevel rational (64/1) lives after the IFD.
	blackOff := ifdOff + ifdLen

	for i := range entries {
		switch entries[i].tag {
		case tagCFARepeatPatternDim:
			entries[i].val = uint32(2) | uint32(2)<<16 // two SHORTs inline: 2,2
		case tagBlackLevel:
			entries[i].val = uint32(blackOff)
		}
	}

	buf := make([]byte, blackOff+8)
	copy(buf, []byte{'I', 'I'})
	bo.PutUint16(buf[2:], 42)
	bo.PutUint32(buf[4:], uint32(ifdOff))
	copy(buf[stripOff:], strip)

	bo.PutUint16(buf[ifdOff:], uint16(nEntries))
	for i, e := range entries {
		p := ifdOff + 2 + i*12
		bo.PutUint16(buf[p:], e.tag)
		bo.PutUint16(buf[p+2:], e.typ)
		bo.PutUint32(buf[p+4:], e.count)
		bo.PutUint32(buf[p+8:], e.val)
	}
	bo.PutUint32(buf[ifdOff+2+nEntries*12:], 0) // next IFD = none

	bo.PutUint32(buf[blackOff:], 64)  // numerator
	bo.PutUint32(buf[blackOff+4:], 1) // denominator

	return buf
}

func TestDecodeDNG_Synthetic(t *testing.T) {
	im, err := DecodeDNG(buildSyntheticDNG(8, 6, 500))
	if err != nil {
		t.Fatal(err)
	}
	if im.Width != 8 || im.Height != 6 {
		t.Errorf("dimensions = %dx%d, want 8x6", im.Width, im.Height)
	}
	if len(im.Pix) != 48 {
		t.Fatalf("len(Pix) = %d, want 48", len(im.Pix))
	}
	if im.Pix[0] != 500 {
		t.Errorf("Pix[0] = %d, want 500", im.Pix[0])
	}
	if im.WhiteLevel != 1023 {
		t.Errorf("WhiteLevel = %d, want 1023", im.WhiteLevel)
	}
	if len(im.BlackLevel) != 1 || im.BlackLevel[0] != 64 {
		t.Errorf("BlackLevel = %v, want [64]", im.BlackLevel)
	}
	if im.CFACols != 2 || im.CFARows != 2 {
		t.Errorf("CFA dims = %dx%d, want 2x2", im.CFACols, im.CFARows)
	}
	// RGGB: (0,0)=R, (1,0)=G, (0,1)=G, (1,1)=B.
	if im.ColorAt(0, 0) != CFARed || im.ColorAt(1, 1) != CFABlue || im.ColorAt(1, 0) != CFAGreen {
		t.Errorf("CFA pattern mismatch: (0,0)=%d (1,0)=%d (1,1)=%d",
			im.ColorAt(0, 0), im.ColorAt(1, 0), im.ColorAt(1, 1))
	}
}

func TestDecodeDNG_RejectsBadMagic(t *testing.T) {
	if _, err := DecodeDNG([]byte("not a dng at all")); err == nil {
		t.Error("expected error for non-DNG input")
	}
}

// If a real rpicam-still DNG is present at the repo root, decode it and sanity
// check the result against the known sample layout.
func TestDecodeDNG_RealSample(t *testing.T) {
	path := filepath.Join("..", "..", "sample.dng")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no sample.dng: %v", err)
	}
	im, err := DecodeDNG(data)
	if err != nil {
		t.Fatal(err)
	}
	if im.Width != 1920 || im.Height != 1080 {
		t.Errorf("sample dimensions = %dx%d, want 1920x1080", im.Width, im.Height)
	}
	if len(im.Pix) != im.Width*im.Height {
		t.Errorf("len(Pix) = %d, want %d", len(im.Pix), im.Width*im.Height)
	}
	if im.WhiteLevel != 1023 {
		t.Errorf("WhiteLevel = %d, want 1023", im.WhiteLevel)
	}
	if len(im.CFAPattern) != 4 {
		t.Errorf("CFAPattern len = %d, want 4", len(im.CFAPattern))
	}
}

// valueSize must stay exact for counts that overflow a 32-bit int. This is the
// platform-independent half of the 32-bit fix: on arm64/amd64 the old int
// arithmetic happened not to wrap, so only asserting on bytesOf would pass on
// a dev machine while still panicking on the armv7 build target.
func TestValueSize_NoOverflow(t *testing.T) {
	for _, tc := range []struct {
		typ   uint16
		count uint32
		want  int64
	}{
		{typ: 3, count: 2, want: 4},                       // SHORT, fits inline
		{typ: 5, count: 0x2000_0001, want: 0x1_0000_0008}, // RATIONAL, overflows int32
		{typ: 1, count: 0xFFFF_FFFF, want: 0xFFFF_FFFF},   // BYTE, max count
		{typ: 5, count: 0xFFFF_FFFF, want: 0x7_FFFF_FFF8}, // widest type, max count
	} {
		if got := valueSize(tc.typ, tc.count); got != tc.want {
			t.Errorf("valueSize(%d, %#x) = %#x, want %#x", tc.typ, tc.count, got, tc.want)
		}
	}
}

// A tag count read off disk must not be able to overflow the size arithmetic.
func TestEntry_BytesOfRejectsOverflowingCount(t *testing.T) {
	data := make([]byte, 64)
	e := entry{
		typ:    5, // RATIONAL, 8 bytes each
		count:  0x2000_0001,
		raw:    data[:4],
		data:   data,
		bo:     binary.LittleEndian,
		offset: 8,
	}

	// Must not panic, and must not hand back bytes it cannot vouch for.
	if got := e.bytesOf(); got != nil {
		t.Errorf("bytesOf() = %d bytes, want nil for an out-of-range count", len(got))
	}
	if got := e.rationals(); len(got) != 0 {
		t.Errorf("rationals() = %d values, want none", len(got))
	}
	if got := e.uints(); len(got) != 0 {
		t.Errorf("uints() = %d values, want none", len(got))
	}
}

// An entry pointing past the end of the file yields nothing rather than
// reading out of bounds.
func TestEntry_BytesOfRejectsOutOfRangeOffset(t *testing.T) {
	data := make([]byte, 64)
	e := entry{typ: 3, count: 8, raw: data[:4], data: data, bo: binary.LittleEndian, offset: 60}
	if got := e.bytesOf(); got != nil {
		t.Errorf("bytesOf() = %d bytes, want nil when the value runs past EOF", len(got))
	}
}

// DecodeDNG parses attacker-shaped input in the sense that matters here: a
// truncated or corrupted file written by a killed rpicam-still. It must return
// an error, never panic — a panic here kills the capture loop.
func FuzzDecodeDNG(f *testing.F) {
	f.Add(buildSyntheticDNG(8, 6, 1000))
	f.Add([]byte("II\x2a\x00"))
	f.Add([]byte("MM\x00\x2a\x00\x00\x00\x08"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		img, err := DecodeDNG(data)
		if err == nil && img == nil {
			t.Fatal("DecodeDNG returned nil image with nil error")
		}
	})
}

// Truncating a valid DNG at every prefix length must never panic.
func TestDecodeDNG_TruncatedPrefixes(t *testing.T) {
	full := buildSyntheticDNG(8, 6, 1000)
	for n := 0; n < len(full); n++ {
		if _, err := DecodeDNG(full[:n]); err == nil {
			// A short prefix legitimately decoding is fine; the point is
			// that it neither panics nor reads out of bounds.
			continue
		}
	}
}
