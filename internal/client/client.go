// Package client implements the thin JSON client used by CLI and GUI surfaces.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zichuanxu/godl/internal/download"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

func (c *Client) List(ctx context.Context) ([]download.Item, error) {
	var items []download.Item
	if err := c.do(ctx, http.MethodGet, "/v1/downloads", nil, http.StatusOK, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (c *Client) Add(ctx context.Context, rawURL, destination string) (download.Item, error) {
	var item download.Item
	input := struct {
		URL         string `json:"url"`
		Destination string `json:"destination"`
	}{URL: rawURL, Destination: destination}
	if err := c.do(ctx, http.MethodPost, "/v1/downloads", input, http.StatusAccepted, &item); err != nil {
		return download.Item{}, err
	}
	return item, nil
}

func (c *Client) Pause(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/downloads/"+id+":pause", nil, http.StatusNoContent, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, wantStatus int, output any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("service returned HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return fmt.Errorf("decode service response: %w", err)
		}
	}
	return nil
}
