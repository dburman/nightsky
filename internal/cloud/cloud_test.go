package cloud

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tinted(r, g, b uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	return img
}

func TestEstimate_GreenRatio(t *testing.T) {
	// Neutral gray sky → ratio ~1.
	m := Estimate(tinted(60, 60, 60), time.Now())
	if m.GreenRatio < 0.95 || m.GreenRatio > 1.05 {
		t.Errorf("neutral GreenRatio = %.3f, want ~1.0", m.GreenRatio)
	}

	// Aurora-green sky → ratio well above 1.
	m = Estimate(tinted(30, 90, 30), time.Now())
	if m.GreenRatio < 2.5 || m.GreenRatio > 3.5 {
		t.Errorf("green GreenRatio = %.3f, want ~3.0", m.GreenRatio)
	}

	// Black frame → neutral fallback, not division blowup.
	m = Estimate(tinted(0, 0, 0), time.Now())
	if m.GreenRatio != 1 {
		t.Errorf("black-frame GreenRatio = %.3f, want 1.0 fallback", m.GreenRatio)
	}
}

func TestWriteReadReport_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 8, 4, 22, 34, 17, 0, time.Local)
	in := []Metric{
		{Timestamp: ts, Mean: 12.5, StdDev: 30.25, Coverage: 0.413,
			GreenRatio: 1.02, StarCount: 805, StarFWHM: 2.75, File: "sky-20260804223417.png"},
		{Timestamp: ts.Add(30 * time.Second), Mean: 13, StdDev: 31, Coverage: 0.414,
			GreenRatio: 1.03, StarCount: 800, StarFWHM: 2.8, File: "sky-20260804223447.png"},
	}
	if err := WriteReport(dir, in); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	// The summary trailer must not break parsing.
	if err := AppendSummary(dir, in); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}

	got, err := ReadReport(dir)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("read %d metrics, want %d", len(got), len(in))
	}
	for i := range in {
		if !got[i].Timestamp.Equal(in[i].Timestamp) {
			t.Errorf("row %d timestamp = %v, want %v", i, got[i].Timestamp, in[i].Timestamp)
		}
		if got[i].File != in[i].File {
			t.Errorf("row %d file = %q, want %q", i, got[i].File, in[i].File)
		}
		if got[i].StarCount != in[i].StarCount {
			t.Errorf("row %d stars = %d, want %d", i, got[i].StarCount, in[i].StarCount)
		}
		if got[i].Coverage != in[i].Coverage {
			t.Errorf("row %d coverage = %v, want %v", i, got[i].Coverage, in[i].Coverage)
		}
	}
}

// Nights recorded before the file column must still parse, leaving File empty
// rather than failing the row — WriteHighlights recovers the name by timestamp.
func TestReadReport_LegacyCSVWithoutFileColumn(t *testing.T) {
	dir := t.TempDir()
	legacy := "timestamp,mean,stddev,coverage,green_ratio,stars,fwhm\n" +
		"2026-08-04T22:34:17,12.50,30.25,0.413,1.020,805,2.75\n" +
		"# avg coverage 41%, 60% of frames clear\n"
	if err := os.WriteFile(filepath.Join(dir, "cloud-2026-08-04.csv"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReport(dir)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d metrics, want 1", len(got))
	}
	if got[0].StarCount != 805 {
		t.Errorf("stars = %d, want 805", got[0].StarCount)
	}
	if got[0].File != "" {
		t.Errorf("file = %q, want empty", got[0].File)
	}
}

// The CSV is named from the first metric's date, which differs from the
// directory when the night's first retained frame lands after midnight.
func TestReadReport_FindsMisnamedCSV(t *testing.T) {
	dir := t.TempDir()
	body := "timestamp,mean,stddev,coverage,green_ratio,stars,fwhm,file\n" +
		"2026-07-30T00:14:02,12.50,30.25,0.413,1.020,40,2.75,sky-20260730001402.png\n"
	// Directory is the 29th; report is named for the 30th.
	if err := os.WriteFile(filepath.Join(dir, "cloud-2026-07-30.csv"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReport(dir)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if len(got) != 1 || got[0].File != "sky-20260730001402.png" {
		t.Fatalf("got %+v, want the single row parsed", got)
	}
}
