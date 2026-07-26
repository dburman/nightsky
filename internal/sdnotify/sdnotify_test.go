package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// listen creates a unixgram socket and returns received datagrams on a channel.
func listen(t *testing.T) (string, <-chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	ch := make(chan string, 4)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			ch <- string(buf[:n])
		}
	}()
	return path, ch
}

func TestNotify_SendsStates(t *testing.T) {
	sock, ch := listen(t)
	t.Setenv("NOTIFY_SOCKET", sock)

	Ready()
	Heartbeat()
	Stopping()

	want := []string{"READY=1", "WATCHDOG=1", "STOPPING=1"}
	for _, w := range want {
		select {
		case got := <-ch:
			if got != w {
				t.Errorf("got %q, want %q", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", w)
		}
	}
}

func TestNotify_NoopWithoutSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	// Must not panic or block.
	Ready()
	Heartbeat()
	Stopping()
}
