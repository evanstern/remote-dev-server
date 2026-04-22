package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// HTTPTransport POSTs messages to an opencode session endpoint.
type HTTPTransport struct {
	Client *http.Client
}

// NewHTTPTransport returns a transport with a 3-second timeout,
// matching the design doc's fire-and-forget semantics.
func NewHTTPTransport() *HTTPTransport {
	return &HTTPTransport{Client: &http.Client{Timeout: 3 * time.Second}}
}

// Deliver POSTs a text message to http://127.0.0.1:<port>/session/<id>/message
// with body {"text":"<text>"}. Returns error on non-2xx, missing
// coordinates, invalid port, or transport failure.
//
// Delivery is loopback-only by design: the v2 message bus is a
// single-host system and only talks to orchestrator sessions on
// 127.0.0.1. Cross-host delivery is explicitly out of scope for v2.
func (h *HTTPTransport) Deliver(ctx context.Context, port int, sessionID, text string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	if sessionID == "" {
		return fmt.Errorf("missing session_id")
	}
	u := &url.URL{
		Scheme:  "http",
		Host:    fmt.Sprintf("127.0.0.1:%d", port),
		Path:    "/session/" + sessionID + "/message",
		RawPath: "/session/" + url.PathEscape(sessionID) + "/message",
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("deliver: http %d", resp.StatusCode)
	}
	return nil
}
