// Package client implements the thin JSON client used by CLI and GUI surfaces.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/settings"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

func (c *Client) List(ctx context.Context) ([]download.Item, error) {
	var items []download.Item
	if err := c.do(ctx, http.MethodGet, "/v1/downloads", nil, http.StatusOK, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (c *Client) Add(ctx context.Context, req download.Request) (download.Item, error) {
	var item download.Item
	if err := c.do(ctx, http.MethodPost, "/v1/downloads", req, http.StatusAccepted, &item); err != nil {
		return download.Item{}, err
	}
	return item, nil
}

func (c *Client) Update(ctx context.Context, id string, patch download.Patch) error {
	return c.do(ctx, http.MethodPatch, "/v1/downloads/"+url.PathEscape(id), patch, http.StatusNoContent, nil)
}

func (c *Client) Settings(ctx context.Context) (settings.Settings, error) {
	var s settings.Settings
	err := c.do(ctx, http.MethodGet, "/v1/settings", nil, http.StatusOK, &s)
	return s, err
}

func (c *Client) PutSettings(ctx context.Context, s settings.Settings) (settings.Settings, error) {
	var saved settings.Settings
	err := c.do(ctx, http.MethodPut, "/v1/settings", s, http.StatusOK, &saved)
	return saved, err
}

func (c *Client) Pause(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/downloads/"+url.PathEscape(id)+":pause", nil, http.StatusNoContent, nil)
}

func (c *Client) Resume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/downloads/"+url.PathEscape(id)+":resume", nil, http.StatusNoContent, nil)
}

func (c *Client) Retry(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/downloads/"+url.PathEscape(id)+":retry", nil, http.StatusNoContent, nil)
}

func (c *Client) Delete(ctx context.Context, id string, removeFiles bool) error {
	path := "/v1/downloads/" + url.PathEscape(id)
	if removeFiles {
		path += "?files=true"
	}
	return c.do(ctx, http.MethodDelete, path, nil, http.StatusNoContent, nil)
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
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method != http.MethodGet {
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
