// Package gps fetches a location fix from a running gpsd daemon.
// It uses gpsd's JSON streaming protocol over TCP (default port 2947) so
// no external Go dependency is required.
package gps

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// Fix holds a GPS position obtained from gpsd.
type Fix struct {
	Latitude  float64
	Longitude float64
}

// FetchFix connects to gpsd at addr (e.g. "localhost:2947"), sends the
// WATCH command, and waits for a TPV report with a valid 2-D or 3-D fix.
// It returns the first such fix or an error if the context expires first.
func FetchFix(ctx context.Context, addr string) (Fix, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Fix{}, fmt.Errorf("connect to gpsd at %s: %w", addr, err)
	}
	defer conn.Close()

	// Request streaming reports.
	if _, err := fmt.Fprintf(conn, `?WATCH={"enable":true,"json":true}`+"\n"); err != nil {
		return Fix{}, fmt.Errorf("send WATCH: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	for {
		// Honour cancellation between reads.
		select {
		case <-ctx.Done():
			return Fix{}, ctx.Err()
		default:
		}

		// Set a short per-line deadline so the scan loop stays cancellable.
		if dl, ok := ctx.Deadline(); ok {
			conn.SetReadDeadline(dl)
		} else {
			conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return Fix{}, fmt.Errorf("read from gpsd: %w", err)
			}
			return Fix{}, fmt.Errorf("gpsd closed the connection")
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var msg struct {
			Class  string  `json:"class"`
			Mode   int     `json:"mode"`
			Lat    float64 `json:"lat"`
			Lon    float64 `json:"lon"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Class != "TPV" {
			continue
		}
		// mode 2 = 2-D fix, mode 3 = 3-D fix — both give valid lat/lon.
		if msg.Mode < 2 {
			continue
		}
		return Fix{Latitude: msg.Lat, Longitude: msg.Lon}, nil
	}
}
