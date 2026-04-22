package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
// coordinates, or transport failure.
func (h *HTTPTransport) Deliver(ctx context.Context, port int, sessionID, text string) error {
	if port == 0 || sessionID == "" {
		return fmt.Errorf("missing port or session_id")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/session/%s/message", port, sessionID)
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
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
