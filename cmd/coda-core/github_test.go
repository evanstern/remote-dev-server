package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func generateTestKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test-key.pem")
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("writing key: %v", err)
	}

	return key, keyPath
}

func TestSignJWT(t *testing.T) {
	key, _ := generateTestKey(t)
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	jwt, err := signJWT("12345", key, now)
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("parsing header: %v", err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("unexpected header: %v", header)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload["iss"] != "12345" {
		t.Errorf("expected iss=12345, got %v", payload["iss"])
	}

	iat := int64(payload["iat"].(float64))
	exp := int64(payload["exp"].(float64))
	expectedIAT := now.Add(-60 * time.Second).Unix()
	expectedEXP := now.Add(10 * time.Minute).Unix()
	if iat != expectedIAT {
		t.Errorf("iat: expected %d, got %d", expectedIAT, iat)
	}
	if exp != expectedEXP {
		t.Errorf("exp: expected %d, got %d", expectedEXP, exp)
	}
}

func TestLoadPrivateKey_PKCS1(t *testing.T) {
	_, keyPath := generateTestKey(t)

	loaded, err := loadPrivateKey(keyPath)
	if err != nil {
		t.Fatalf("loadPrivateKey: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil key")
	}
}

func TestLoadPrivateKey_MissingFile(t *testing.T) {
	_, err := loadPrivateKey("/nonexistent/key.pem")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadPrivateKey_InvalidPEM(t *testing.T) {
	tmpDir := t.TempDir()
	badPath := filepath.Join(tmpDir, "bad.pem")
	os.WriteFile(badPath, []byte("not a pem file"), 0600)

	_, err := loadPrivateKey(badPath)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestExchangeForInstallationToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing Bearer token")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_test_token_123",
			"expires_at": "2025-01-15T13:00:00Z",
		})
	}))
	defer server.Close()

	req, _ := http.NewRequest("POST", server.URL, nil)
	req.Header.Set("Authorization", "Bearer test-jwt")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Token != "ghs_test_token_123" {
		t.Errorf("expected ghs_test_token_123, got %s", result.Token)
	}
}
