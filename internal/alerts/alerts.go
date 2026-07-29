// Package alerts delivers push notifications (aurora events, night summaries)
// to a user-configured webhook. The wire format is ntfy-compatible — the
// message is the POST body and the title travels in a header — which works
// with ntfy.sh out of the box and with anything else that accepts a plain
// POST.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// retryDelays between delivery attempts; alerts are time-sensitive, so give
// up quickly rather than delivering an aurora alert an hour late.
var retryDelays = []time.Duration{10 * time.Second, 60 * time.Second}

// Notifier posts messages to a webhook. A nil *Notifier is a valid no-op, so
// callers never need to guard on configuration.
type Notifier struct {
	url    string
	client *http.Client
	logger *slog.Logger
}

// NewNotifier returns a Notifier for the webhook URL, or nil when the URL is
// empty (alerts unconfigured).
func NewNotifier(url string, logger *slog.Logger) *Notifier {
	if url == "" {
		return nil
	}
	return &Notifier{
		url:    url,
		client: &http.Client{Timeout: 30 * time.Second},
		logger: logger,
	}
}

// Send delivers one notification, retrying briefly on failure. Safe on a nil
// receiver (no-op).
func (n *Notifier) Send(ctx context.Context, title, message string) error {
	if n == nil {
		return nil
	}

	var err error
	for attempt := 0; ; attempt++ {
		err = n.post(ctx, title, message)
		if err == nil {
			return nil
		}
		if attempt >= len(retryDelays) {
			n.logger.Error("notification delivery failed", "title", title, "error", err)
			return err
		}
		n.logger.Warn("notification delivery failed, retrying",
			"title", title, "retry_in", retryDelays[attempt], "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelays[attempt]):
		}
	}
}

func (n *Notifier) post(ctx context.Context, title, message string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
