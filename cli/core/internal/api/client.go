// Package api is the thin client for the Zen serverless admin API
// (contract §2.5/§2.6 and §5): push the manifest for instant propagation and
// fetch the resolved manifest for list/rollback/promote.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zen-ota/zen-cli/core/internal/manifest"
)

const timeout = 5 * time.Second

// BaseURL normalizes the configured API base: strips trailing slashes and a
// trailing "/api" so both https://x.vercel.app and https://x.vercel.app/api
// work.
func BaseURL(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(b, "/api")
}

// PostManifest POSTs the full manifest to {base}/api/admin/manifest with the
// admin bearer token. Any non-200 response is an error. Returns the raw
// response body.
func PostManifest(baseURL, adminToken string, m *manifest.Manifest) ([]byte, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, BaseURL(baseURL)+"/api/admin/manifest", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	return roundTrip(req)
}

// GetManifest fetches the resolved manifest for appID from
// {base}/api/admin/manifest?appId=… and returns the raw manifest JSON (the
// {"source":…,"manifest":…} wrapper is unwrapped when present).
func GetManifest(baseURL, adminToken, appID string) ([]byte, error) {
	u := BaseURL(baseURL) + "/api/admin/manifest?appId=" + url.QueryEscape(appID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	data, err := roundTrip(req)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Source   string          `json:"source"`
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Manifest) > 0 {
		return []byte(wrapper.Manifest), nil
	}
	// server returned a bare manifest document
	return data, nil
}

func roundTrip(req *http.Request) ([]byte, error) {
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if e := decodeError(data); e != "" {
			msg = e
		}
		return nil, fmt.Errorf("zen api: HTTP %d: %s", resp.StatusCode, msg)
	}
	return data, nil
}

func decodeError(data []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	if e.Error != "" {
		return e.Error
	}
	return e.Message
}
