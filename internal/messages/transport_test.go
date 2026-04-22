package messages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestHTTPTransport(timeout time.Duration) *HTTPTransport {
	return &HTTPTransport{Client: &http.Client{Timeout: timeout}}
}

func hostPort(t *testing.T, rawurl string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(u.Host, ":")
	if len(parts) != 2 {
		t.Fatalf("unexpected host %q", u.Host)
	}
	p, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return parts[0], p
}

func TestHTTPTransport_2xxIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h, port := hostPort(t, srv.URL)
	_ = h
	tr := newTestHTTPTransport(2 * time.Second)
	tr.Client.Transport = rewriteLoopback(t, srv.URL)

	if err := tr.Deliver(context.Background(), port, "ses", "hi"); err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
}

func TestHTTPTransport_500IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	_, port := hostPort(t, srv.URL)
	tr := newTestHTTPTransport(2 * time.Second)
	tr.Client.Transport = rewriteLoopback(t, srv.URL)

	if err := tr.Deliver(context.Background(), port, "ses", "hi"); err == nil {
		t.Fatal("want error on 500, got nil")
	}
}

func TestHTTPTransport_TimeoutIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, port := hostPort(t, srv.URL)
	tr := newTestHTTPTransport(20 * time.Millisecond)
	tr.Client.Transport = rewriteLoopback(t, srv.URL)

	if err := tr.Deliver(context.Background(), port, "ses", "hi"); err == nil {
		t.Fatal("want timeout error, got nil")
	}
}

func TestHTTPTransport_RejectsEmptyCoords(t *testing.T) {
	tr := NewHTTPTransport()
	if err := tr.Deliver(context.Background(), 0, "ses", "hi"); err == nil {
		t.Fatal("want error for port=0")
	}
	if err := tr.Deliver(context.Background(), 4096, "", "hi"); err == nil {
		t.Fatal("want error for empty sessionID")
	}
}

func TestHTTPTransport_SendsCorrectBody(t *testing.T) {
	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, port := hostPort(t, srv.URL)
	tr := newTestHTTPTransport(2 * time.Second)
	tr.Client.Transport = rewriteLoopback(t, srv.URL)

	if err := tr.Deliver(context.Background(), port, "ses1", "hello"); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if parsed["text"] != "hello" {
		t.Fatalf("body text = %q, want %q", parsed["text"], "hello")
	}
	if gotPath != "/session/ses1/message" {
		t.Fatalf("path = %q, want /session/ses1/message", gotPath)
	}
}

// rewriteLoopback lets tests hit httptest.Server even though the
// transport hardcodes 127.0.0.1. It rewrites the URL target host
// to the httptest server host at the round-trip layer.
func rewriteLoopback(t *testing.T, srvURL string) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(srvURL)
	if err != nil {
		t.Fatal(err)
	}
	return &loopbackRewriter{host: target.Host}
}

type loopbackRewriter struct {
	host string
}

func (l *loopbackRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Host = l.host
	req.Host = l.host
	return http.DefaultTransport.RoundTrip(req)
}
