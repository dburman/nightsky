// Package sdnotify implements the systemd sd_notify protocol with no
// dependencies: a datagram written to the unix socket systemd passes in
// NOTIFY_SOCKET. Every function is a silent no-op when not running under
// systemd (or under a unit without Type=notify), so callers never need to
// guard invocations.
package sdnotify

import (
	"net"
	"os"
)

// notify sends one state datagram to the NOTIFY_SOCKET, if present.
func notify(state string) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}
	// Abstract-namespace sockets are passed with a leading '@'.
	if sock[0] == '@' {
		sock = "\x00" + sock[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Write([]byte(state))
}

// Ready tells systemd the service has finished starting. Required once at
// startup for Type=notify units — until it is sent, the unit stays in
// "activating" and systemctl start blocks.
func Ready() {
	notify("READY=1")
}

// Heartbeat pets the systemd watchdog. With WatchdogSec set on the unit,
// systemd kills and restarts the service if heartbeats stop — turning a
// silently wedged process into an automatic recovery.
func Heartbeat() {
	notify("WATCHDOG=1")
}

// Stopping tells systemd a clean shutdown has begun, so the kill timeout
// starts from a truthful state.
func Stopping() {
	notify("STOPPING=1")
}
