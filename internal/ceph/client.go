// Package ceph is a small client for the Ceph manager dashboard REST API,
// scoped to the RGW (S3) user and bucket endpoints the operator needs.
//
// The dashboard authenticates with a username/password to POST /api/auth, which
// returns a bearer (JWT) token. The client caches that token and transparently
// re-authenticates when the server returns 401 (expired/invalid token).
package ceph

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultAccept is the versioned media type the Ceph dashboard requires on its
// RGW endpoints. The dashboard rejects requests without a matching version.
const defaultAccept = "application/vnd.ceph.api.v1.0+json"

// Config configures a dashboard Client.
type Config struct {
	// BaseURL is the dashboard root, e.g. https://dashboard.storage.example.net.
	BaseURL string
	// Username / Password authenticate to POST /api/auth. The account needs the
	// rgw-manager role (or admin) on the dashboard.
	Username string
	Password string
	// CACert is an optional PEM bundle used to verify the dashboard TLS cert.
	CACert []byte
	// Insecure disables TLS verification (not recommended).
	Insecure bool
	// Timeout bounds each HTTP request. Defaults to 30s.
	Timeout time.Duration
}

// Client talks to the Ceph dashboard API. It is safe for concurrent use.
type Client struct {
	base string
	user string
	pass string
	http *http.Client

	mu    sync.Mutex
	token string
}

// APIError is returned for any non-2xx dashboard response.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ceph dashboard %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound reports whether err is a 404 from the dashboard.
func IsNotFound(err error) bool {
	var a *APIError
	return errors.As(err, &a) && a.Status == http.StatusNotFound
}

// IsConflict reports whether err is a 409 from the dashboard.
func IsConflict(err error) bool {
	var a *APIError
	return errors.As(err, &a) && a.Status == http.StatusConflict
}

// NewClient validates cfg and builds a Client.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("ceph: dashboard base URL is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("ceph: dashboard username and password are required")
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec // opt-in via config
	if len(cfg.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACert) {
			return nil, fmt.Errorf("ceph: failed to parse CA certificate PEM")
		}
		tlsCfg.RootCAs = pool
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		base: base,
		user: cfg.Username,
		pass: cfg.Password,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// do performs an authenticated request, decoding a 2xx JSON body into out (when
// non-nil). On a 401 it drops the cached token, re-authenticates, and retries
// once. accept overrides the Accept header version when non-empty.
func (c *Client) do(ctx context.Context, method, path string, body, out any, accept string) error {
	if accept == "" {
		accept = defaultAccept
	}
	tok, err := c.ensureToken(ctx)
	if err != nil {
		return err
	}
	status, err := c.execute(ctx, method, path, body, accept, tok, out)
	if status == http.StatusUnauthorized {
		c.clearToken()
		tok, err = c.ensureToken(ctx)
		if err != nil {
			return err
		}
		_, err = c.execute(ctx, method, path, body, accept, tok, out)
	}
	return err
}

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	tok, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.token = tok
	return tok, nil
}

func (c *Client) clearToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func (c *Client) login(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	payload := map[string]string{"username": c.user, "password": c.pass}
	if _, err := c.execute(ctx, http.MethodPost, "/api/auth", payload, defaultAccept, "", &out); err != nil {
		return "", fmt.Errorf("dashboard login failed: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("dashboard login returned no token")
	}
	return out.Token, nil
}

// execute runs a single request and returns the HTTP status. A non-2xx status
// yields an *APIError. token is sent as a bearer when non-empty.
func (c *Client) execute(ctx context.Context, method, path string, body any, accept, token string, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &APIError{
			Status: resp.StatusCode,
			Method: method,
			Path:   path,
			Body:   strings.TrimSpace(string(data)),
		}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// Ping verifies connectivity and credentials by authenticating.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.ensureToken(ctx)
	return err
}
