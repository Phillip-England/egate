// Package sdk provides a client for the egate HTTP API.
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseSize = 1 << 20

// Client sends requests to an egate server.
type Client struct {
	baseURL *url.URL
	apiKey  string
	http    *http.Client
}

// Email is an email to send through egate.
type Email struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Subject  string `json:"subject"`
	TextBody string `json:"text_body,omitempty"`
	HTMLBody string `json:"html_body,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`
}

// Response contains the status and body returned by egate (which passes
// Resend's response through unchanged).
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// APIError is returned when egate responds with a non-2xx status.
type APIError struct {
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(string(e.Body))
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("egate: status %d: %s", e.StatusCode, message)
}

// NewClient creates a client. host must include its scheme, for example
// "https://egate.example.com". Egate does not assume a default host.
func NewClient(host, apiKey string) (*Client, error) {
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("egate: host is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("egate: API key is required")
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("egate: host must be an absolute URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("egate: host scheme must be http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &Client{baseURL: u, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// SetHTTPClient replaces the HTTP client used for requests. It is useful for
// custom transports, tracing, or timeouts. A nil client restores the default.
func (c *Client) SetHTTPClient(client *http.Client) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	c.http = client
}

// SendEmail sends an email through egate.
func (c *Client) SendEmail(ctx context.Context, email Email) (*Response, error) {
	payload, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("egate: encode email: %w", err)
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/email"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("egate: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("egate: send request: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("egate: read response: %w", err)
	}
	if len(body) > maxResponseSize {
		return nil, errors.New("egate: response exceeds 1 MiB")
	}
	response := &Response{StatusCode: res.StatusCode, Header: res.Header.Clone(), Body: body}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return response, &APIError{StatusCode: res.StatusCode, Body: body}
	}
	return response, nil
}
