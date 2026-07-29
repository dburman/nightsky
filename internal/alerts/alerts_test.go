package alerts

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNotifier_PostsTitleAndBody(t *testing.T) {
	var gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()

	n := NewNotifier(srv.URL, testLogger())
	if err := n.Send(context.Background(), "Aurora", "green surge at 01:23"); err != nil {
		t.Fatal(err)
	}
	if gotTitle != "Aurora" || gotBody != "green surge at 01:23" {
		t.Errorf("got title=%q body=%q", gotTitle, gotBody)
	}
}

func TestNotifier_NilIsNoop(t *testing.T) {
	var n *Notifier
	if err := n.Send(context.Background(), "x", "y"); err != nil {
		t.Errorf("nil notifier returned error: %v", err)
	}
	if NewNotifier("", testLogger()) != nil {
		t.Error("empty URL should produce nil notifier")
	}
}

// Build a detector with a settled ~1.0 baseline.
func settledDetector(cfg AuroraConfig) *AuroraDetector {
	d := NewAuroraDetector(cfg)
	for i := 0; i < 20; i++ {
		d.Observe(1.0, 0.1)
	}
	return d
}

func TestAurora_FiresAfterSustainedGreenExcess(t *testing.T) {
	d := settledDetector(AuroraConfig{RatioThreshold: 1.3, MinFrames: 3})

	fired := 0
	for i := 0; i < 10; i++ {
		if d.Observe(1.6, 0.1) {
			fired++
			if i != 2 {
				t.Errorf("fired on frame %d, want frame 2 (MinFrames=3)", i)
			}
		}
	}
	if fired != 1 {
		t.Errorf("fired %d times during one sustained event, want 1", fired)
	}
}

func TestAurora_CloudSuppresses(t *testing.T) {
	d := settledDetector(AuroraConfig{RatioThreshold: 1.3, MinFrames: 3, MaxCloud: 0.5})
	for i := 0; i < 10; i++ {
		if d.Observe(1.8, 0.9) {
			t.Fatal("fired under heavy cloud")
		}
	}
}

func TestAurora_RearmsAfterQuietPeriod(t *testing.T) {
	d := settledDetector(AuroraConfig{RatioThreshold: 1.3, MinFrames: 2})

	// First event.
	d.Observe(1.6, 0.1)
	if !d.Observe(1.6, 0.1) {
		t.Fatal("first event did not fire")
	}
	// Quiet stretch long enough to re-arm.
	for i := 0; i < rearmFrames; i++ {
		d.Observe(1.0, 0.1)
	}
	// Second surge fires again.
	d.Observe(1.6, 0.1)
	if !d.Observe(1.6, 0.1) {
		t.Error("re-armed detector did not fire on second surge")
	}
}

func TestAurora_NoDetectionBeforeBaseline(t *testing.T) {
	d := NewAuroraDetector(AuroraConfig{RatioThreshold: 1.3, MinFrames: 1})
	// First frames are green but there is no baseline yet.
	for i := 0; i < baselineMinSamples-1; i++ {
		if d.Observe(1.8, 0.1) {
			t.Fatal("fired before baseline settled")
		}
	}
}
