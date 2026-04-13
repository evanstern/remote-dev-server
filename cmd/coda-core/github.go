package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func runGitHub(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: coda-core github <token|comment>")
	}
	switch args[0] {
	case "token":
		return runGitHubToken(args[1:])
	case "comment":
		return runGitHubComment(args[1:])
	default:
		return fmt.Errorf("unknown github subcommand: %s", args[0])
	}
}

func runGitHubToken(args []string) error {
	fs := flag.NewFlagSet("github-token", flag.ExitOnError)
	clientID := fs.String("client-id", "", "GitHub App Client ID (used as JWT issuer)")
	installationID := fs.String("installation-id", "", "GitHub App Installation ID")
	keyPath := fs.String("key", "", "Path to private key PEM file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *clientID == "" || *installationID == "" || *keyPath == "" {
		return fmt.Errorf("--client-id, --installation-id, and --key are required")
	}

	privateKey, err := loadPrivateKey(*keyPath)
	if err != nil {
		return fmt.Errorf("loading private key: %w", err)
	}

	jwt, err := signJWT(*clientID, privateKey, time.Now())
	if err != nil {
		return fmt.Errorf("signing JWT: %w", err)
	}

	token, err := exchangeForInstallationToken(jwt, *installationID)
	if err != nil {
		return fmt.Errorf("exchanging for installation token: %w", err)
	}

	fmt.Print(token)
	return nil
}

func runGitHubComment(args []string) error {
	fs := flag.NewFlagSet("github-comment", flag.ExitOnError)
	clientID := fs.String("client-id", "", "GitHub App Client ID (used as JWT issuer)")
	installationID := fs.String("installation-id", "", "GitHub App Installation ID")
	keyPath := fs.String("key", "", "Path to private key PEM file")
	repo := fs.String("repo", "", "Repository (owner/repo)")
	issueNum := fs.Int("issue", 0, "Issue or PR number")
	body := fs.String("body", "", "Comment body (reads stdin if empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *clientID == "" || *installationID == "" || *keyPath == "" {
		return fmt.Errorf("--client-id, --installation-id, and --key are required")
	}
	if *repo == "" || *issueNum == 0 {
		return fmt.Errorf("--repo and --issue are required")
	}

	commentBody := *body
	if commentBody == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		commentBody = string(data)
	}
	if strings.TrimSpace(commentBody) == "" {
		return fmt.Errorf("comment body is empty")
	}

	privateKey, err := loadPrivateKey(*keyPath)
	if err != nil {
		return fmt.Errorf("loading private key: %w", err)
	}

	jwt, err := signJWT(*clientID, privateKey, time.Now())
	if err != nil {
		return fmt.Errorf("signing JWT: %w", err)
	}

	token, err := exchangeForInstallationToken(jwt, *installationID)
	if err != nil {
		return fmt.Errorf("getting installation token: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", *repo, *issueNum)
	payload, _ := json.Marshal(map[string]string{"body": commentBody})

	req, err := http.NewRequest("POST", url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("posting comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.HTMLURL != "" {
		fmt.Println(result.HTMLURL)
	} else {
		fmt.Println("Comment posted.")
	}
	return nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return key, nil
}

func signJWT(clientID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	payload := map[string]interface{}{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": clientID,
	}

	headerJSON, _ := json.Marshal(header)
	payloadJSON, _ := json.Marshal(payload)

	headerB64 := base64URLEncode(headerJSON)
	payloadB64 := base64URLEncode(payloadJSON)

	signingInput := headerB64 + "." + payloadB64

	h := crypto.SHA256.New()
	h.Write([]byte(signingInput))
	hashed := h.Sum(nil)

	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed)
	if err != nil {
		return "", err
	}

	return signingInput + "." + base64URLEncode(signature), nil
}

func exchangeForInstallationToken(jwt, installationID string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installationID)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("no token in response")
	}

	return result.Token, nil
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}
