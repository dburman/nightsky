// Package raw decodes DNG (Adobe Digital Negative) files produced by
// rpicam-still into linear, single-channel Bayer/CFA sensor data, so dark and
// flat calibration can run on the raw signal before debayering rather than on
// the gamma-encoded 8-bit JPEG/PNG.
//
// Only the subset rpicam-still actually emits is supported: an uncompressed
// 16-bit CFA image stored in a SubIFD. Compressed (lossless-JPEG) or tiled
// DNGs return an error rather than producing wrong data silently.
package raw

import (
	"encoding/binary"
	"fmt"
)

// TIFF/DNG tag numbers used during decode.
const (
	tagNewSubfileType            = 254
	tagImageWidth                = 256
	tagImageLength               = 257
	tagBitsPerSample             = 258
	tagCompression               = 259
	tagPhotometricInterpretation = 262
	tagStripOffsets              = 273
	tagSamplesPerPixel           = 277
	tagRowsPerStrip              = 278
	tagStripByteCounts           = 279
	tagTileWidth                 = 322
	tagSubIFDs                   = 330
	tagCFARepeatPatternDim       = 33421
	tagCFAPattern                = 33422
	tagBlackLevel                = 50714
	tagWhiteLevel                = 50717
)

const (
	photometricCFA  = 32803
	compressionNone = 1
)

// CFA colour codes as used in the DNG CFAPattern tag.
const (
	CFARed   = 0
	CFAGreen = 1
	CFABlue  = 2
)

// Image holds decoded linear CFA sensor data and the calibration metadata
// needed to normalize it. Pix is row-major, one uint16 sample per photosite.
type Image struct {
	Width      int
	Height     int
	Pix        []uint16
	CFACols    int     // CFA repeat pattern width (typically 2)
	CFARows    int     // CFA repeat pattern height (typically 2)
	CFAPattern []uint8 // CFACols*CFARows colour codes, row-major
	BlackLevel []float64
	WhiteLevel uint16
}

// ColorAt returns the CFA colour code (CFARed/CFAGreen/CFABlue) of the
// photosite at (x, y), using the repeating CFA pattern.
func (im *Image) ColorAt(x, y int) uint8 {
	if im.CFACols == 0 || im.CFARows == 0 || len(im.CFAPattern) == 0 {
		return CFAGreen
	}
	return im.CFAPattern[(y%im.CFARows)*im.CFACols+(x%im.CFACols)]
}

// ifd is a parsed image file directory: tag -> entry.
type ifd map[uint16]entry

type entry struct {
	typ   uint16
	count uint32
	// raw is the 4-byte value/offset field; resolve via the helpers below.
	raw    []byte
	data   []byte // full backing slice for offset resolution
	bo     binary.ByteOrder
	offset uint32 // absolute offset of the value when it doesn't fit inline
}

func typeSize(t uint16) int {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11, 13:
		return 4
	case 5, 10, 12:
		return 8
	}
	return 1
}

// valueSize returns the byte length of an entry's value as an int64.
//
// The width matters. Both typ and count are read straight off disk, and this
// binary is built for 32-bit ARM (Pi Zero 2 / armv7) where int is 32 bits: a
// corrupt count makes typeSize*count overflow to a negative length in int, and
// slicing with a negative length panics — killing the capture loop over a
// truncated file. Computing in int64 cannot overflow, because typeSize is at
// most 8 and count is at most 2^32-1.
func valueSize(typ uint16, count uint32) int64 {
	return int64(typeSize(typ)) * int64(count)
}

// bytesOf returns the entry's value bytes, following the offset when the value
// doesn't fit in the inline 4-byte field. It returns nil for any entry whose
// declared size or offset does not lie within the file.
func (e entry) bytesOf() []byte {
	n := valueSize(e.typ, e.count)
	if n <= 0 {
		return nil
	}
	if n <= 4 {
		if int64(len(e.raw)) < n {
			return nil
		}
		return e.raw[:n]
	}
	end := int64(e.offset) + n
	if end > int64(len(e.data)) {
		return nil
	}
	return e.data[e.offset:end]
}

// uints reads SHORT/LONG/BYTE values as uint32s.
func (e entry) uints() []uint32 {
	b := e.bytesOf()
	sz := typeSize(e.typ)
	// Size the result from the bytes actually available, not from the
	// file's declared count: a corrupt count would otherwise reserve
	// gigabytes up front.
	out := make([]uint32, 0, len(b)/sz)
	for i := 0; i+sz <= len(b); i += sz {
		switch e.typ {
		case 1:
			out = append(out, uint32(b[i]))
		case 3:
			out = append(out, uint32(e.bo.Uint16(b[i:])))
		case 4, 13:
			out = append(out, e.bo.Uint32(b[i:]))
		}
	}
	return out
}

// rationals reads RATIONAL values (num/den) as float64s.
func (e entry) rationals() []float64 {
	b := e.bytesOf()
	out := make([]float64, 0, len(b)/8)
	for i := 0; i+8 <= len(b); i += 8 {
		num := e.bo.Uint32(b[i:])
		den := e.bo.Uint32(b[i+4:])
		if den == 0 {
			out = append(out, 0)
		} else {
			out = append(out, float64(num)/float64(den))
		}
	}
	return out
}

func (e entry) first() uint32 {
	if v := e.uints(); len(v) > 0 {
		return v[0]
	}
	return 0
}

// DecodeDNG decodes the linear CFA image from DNG file bytes.
func DecodeDNG(data []byte) (*Image, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("dng: too short")
	}
	var bo binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		bo = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("dng: bad byte-order mark")
	}
	if bo.Uint16(data[2:]) != 42 {
		return nil, fmt.Errorf("dng: bad TIFF magic")
	}

	// Collect IFD0, its chained IFDs, and any SubIFDs, then pick the CFA one.
	var ifds []ifd
	seen := map[uint32]bool{}
	var walk func(off uint32)
	walk = func(off uint32) {
		for off != 0 && !seen[off] {
			seen[off] = true
			d, next, subs, err := parseIFD(data, off, bo)
			if err != nil {
				return
			}
			ifds = append(ifds, d)
			for _, s := range subs {
				walk(s)
			}
			off = next
		}
	}
	walk(bo.Uint32(data[4:]))

	var cfa ifd
	for _, d := range ifds {
		e, ok := d[tagPhotometricInterpretation]
		if !ok || e.first() != photometricCFA {
			continue
		}
		// Prefer the full-resolution image (NewSubfileType bit 0 clear).
		if st, ok := d[tagNewSubfileType]; ok && st.first()&1 != 0 {
			continue
		}
		cfa = d
		break
	}
	if cfa == nil {
		return nil, fmt.Errorf("dng: no CFA (raw) image found")
	}

	return decodeCFA(data, cfa, bo)
}

func parseIFD(data []byte, off uint32, bo binary.ByteOrder) (ifd, uint32, []uint32, error) {
	if int(off)+2 > len(data) {
		return nil, 0, nil, fmt.Errorf("dng: IFD offset out of range")
	}
	n := bo.Uint16(data[off:])
	end := int(off) + 2 + int(n)*12 + 4
	if end > len(data) {
		return nil, 0, nil, fmt.Errorf("dng: IFD truncated")
	}

	d := make(ifd, n)
	var subs []uint32
	for i := 0; i < int(n); i++ {
		e := int(off) + 2 + i*12
		tag := bo.Uint16(data[e:])
		ent := entry{
			typ:   bo.Uint16(data[e+2:]),
			count: bo.Uint32(data[e+4:]),
			raw:   data[e+8 : e+12],
			data:  data,
			bo:    bo,
		}
		if typeSize(ent.typ)*int(ent.count) > 4 {
			ent.offset = bo.Uint32(data[e+8:])
		}
		d[tag] = ent
		if tag == tagSubIFDs {
			subs = ent.uints()
		}
	}
	next := bo.Uint32(data[int(off)+2+int(n)*12:])
	return d, next, subs, nil
}

func decodeCFA(data []byte, d ifd, bo binary.ByteOrder) (*Image, error) {
	if _, tiled := d[tagTileWidth]; tiled {
		return nil, fmt.Errorf("dng: tiled raw not supported")
	}
	if c, ok := d[tagCompression]; ok && c.first() != compressionNone {
		return nil, fmt.Errorf("dng: compressed raw (compression=%d) not supported", c.first())
	}
	if s, ok := d[tagSamplesPerPixel]; ok && s.first() != 1 {
		return nil, fmt.Errorf("dng: expected single-channel CFA, got %d samples/pixel", s.first())
	}
	if bps, ok := d[tagBitsPerSample]; ok && bps.first() != 16 {
		return nil, fmt.Errorf("dng: unsupported bits-per-sample %d (only 16)", bps.first())
	}

	we, hok := d[tagImageWidth]
	he, lok := d[tagImageLength]
	if !hok || !lok {
		return nil, fmt.Errorf("dng: missing image dimensions")
	}
	width := int(we.first())
	height := int(he.first())
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("dng: invalid dimensions %dx%d", width, height)
	}

	offs := d[tagStripOffsets].uints()
	counts := d[tagStripByteCounts].uints()
	if len(offs) == 0 || len(offs) != len(counts) {
		return nil, fmt.Errorf("dng: bad strip layout (%d offsets, %d counts)", len(offs), len(counts))
	}

	pix := make([]uint16, 0, width*height)
	for i := range offs {
		start, n := int(offs[i]), int(counts[i])
		if start < 0 || start+n > len(data) || n%2 != 0 {
			return nil, fmt.Errorf("dng: strip %d out of range", i)
		}
		strip := data[start : start+n]
		for j := 0; j+2 <= len(strip); j += 2 {
			pix = append(pix, bo.Uint16(strip[j:]))
		}
	}
	if len(pix) < width*height {
		return nil, fmt.Errorf("dng: short pixel data (%d, want %d)", len(pix), width*height)
	}
	pix = pix[:width*height]

	im := &Image{Width: width, Height: height, Pix: pix, WhiteLevel: 65535}

	if dim, ok := d[tagCFARepeatPatternDim]; ok {
		v := dim.uints()
		if len(v) == 2 {
			im.CFACols, im.CFARows = int(v[0]), int(v[1])
		}
	}
	if pat, ok := d[tagCFAPattern]; ok {
		for _, b := range pat.bytesOf() {
			im.CFAPattern = append(im.CFAPattern, b)
		}
	}
	if bl, ok := d[tagBlackLevel]; ok {
		switch bl.typ {
		case 5, 10:
			im.BlackLevel = bl.rationals()
		default:
			for _, v := range bl.uints() {
				im.BlackLevel = append(im.BlackLevel, float64(v))
			}
		}
	}
	if wl, ok := d[tagWhiteLevel]; ok {
		im.WhiteLevel = uint16(wl.first())
	}

	return im, nil
}
