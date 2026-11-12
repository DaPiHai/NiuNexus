package uds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to the privileged core over a Unix socket.
type Client struct {
	HTTP  *http.Client
	Token string
}

// New creates a client with a bounded timeout. The network/address arguments
// are ignored because every request is dialed over the Unix socket.
func New(socket, token string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
		MaxIdleConns:       4,
		IdleConnTimeout:    30 * time.Second,
	}
	return &Client{
		HTTP:  &http.Client{Transport: transport, Timeout: 15 * time.Second},
		Token: token,
	}
}

// Raw performs a request and returns the raw status and body. Non-2xx core
// responses are returned to the caller so the web layer can forward them
// without losing validation details.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("无法连接 NiuNexus core: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("读取 core 响应失败: %w", err)
	}
	return resp.StatusCode, payload, nil
}

// GetJSON performs a GET and decodes a successful JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, out)
}

// DoJSON performs an HTTP request against the core and decodes JSON.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 NiuNexus core: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("读取 core 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(payload))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("core 返回 %s: %s", resp.Status, message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("解析 core 响应失败: %w", err)
	}
	return nil
}
