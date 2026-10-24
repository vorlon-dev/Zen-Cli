// Package github is a minimal GitHub REST client covering what the Zen CLI
// needs: releases, release asset upload/delete and the Contents API (used to
// read and commit the release manifest). It retries HTTP 429 with the
// Retry-After hint (max 3 attempts) and uses request-scoped timeouts
// (15s for API calls, 60s for uploads).
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout = 15 * time.Second
	uploadTimeout  = 60 * time.Second
	apiBase        = "https://api.github.com"
	uploadsBase    = "https://uploads.github.com"
	mediaType      = "application/vnd.github+json"
	userAgent      = "zen-cli/1.0"
)

// Client talks to the GitHub API with a personal access token.
type Client struct {
	token string
	hc    *http.Client
}

// NewClient creates a token-authed client.
func NewClient(token string) *Client {
	return &Client{token: token, hc: &http.Client{}}
}

// APIError is a non-2xx GitHub response.
type APIError struct {
	StatusCode int
	Message    string
	URL        string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d (%s)", e.StatusCode, e.URL)
	}
	return fmt.Sprintf("HTTP %d: %s (%s)", e.StatusCode, e.Message, e.URL)
}

// IsNotFound reports whether err is a GitHub 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// Release is the subset of the GitHub release object the CLI uses.
type Release struct {
	ID                 int64   `json:"id"`
	TagName            string  `json:"tag_name"`
	Name               string  `json:"name"`
	Draft              bool    `json:"draft"`
	Prerelease         bool    `json:"prerelease"`
	UploadURL          string  `json:"upload_url"`
	BrowserDownloadURL string  `json:"browser_download_url"`
	Assets             []Asset `json:"assets"`
}

// Asset is a release asset (the API "url" field is used for deletion).
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// doJSON performs a request, retrying HTTP 429 up to 3 times (Retry-After),
// decodes the JSON body into out (when non-nil) and returns the response
// headers named in wantHeaders.
func (c *Client) doJSON(method, rawURL string, body any, out any, wantHeaders []string) (map[string]string, error) {
	var payload []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		payload = buf
	}
	for attempt := 0; attempt < 3; attempt++ {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		resp, err := c.send(ctx, method, rawURL, rdr)
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			wait := retryDelay(resp, attempt)
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			time.Sleep(wait)
			continue
		}
		headers, herr := handleResponse(resp, out, wantHeaders, rawURL)
		cancel()
		return headers, herr
	}
	// unreachable, but keep the compiler honest
	return nil, fmt.Errorf("github: retries exhausted for %s", rawURL)
}

func (c *Client) send(ctx context.Context, method, rawURL string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mediaType)
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.hc.Do(req)
}

func handleResponse(resp *http.Response, out any, wantHeaders []string, rawURL string) (map[string]string, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, data, rawURL)
	}
	headers := map[string]string{}
	for _, h := range wantHeaders {
		headers[h] = resp.Header.Get(h)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return headers, fmt.Errorf("decode response from %s: %w", rawURL, err)
		}
	}
	return headers, nil
}

func apiError(status int, body []byte, rawURL string) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	return &APIError{StatusCode: status, Message: e.Message, URL: rawURL}
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs >= 0 {
			if secs > 60 {
				secs = 60
			}
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(attempt+1) * time.Second
}

// CreateRelease creates a release; when the tag already exists (HTTP 422)
// it falls back to returning the existing release for that tag.
func (c *Client) CreateRelease(repo, tag, title, body string) (*Release, error) {
	in := map[string]string{"tag_name": tag, "name": title, "body": body}
	var rel Release
	_, err := c.doJSON(http.MethodPost, apiBase+"/repos/"+repo+"/releases", in, &rel, nil)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.StatusCode == http.StatusUnprocessableEntity {
			if existing, err2 := c.GetReleaseByTag(repo, tag); err2 == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	return &rel, nil
}

// GetReleaseByTag fetches the release for a tag (404 when absent).
func (c *Client) GetReleaseByTag(repo, tag string) (*Release, error) {
	var rel Release
	u := fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBase, repo, url.PathEscape(tag))
	_, err := c.doJSON(http.MethodGet, u, nil, &rel, nil)
	if err != nil {
		return nil, err
	}
	return &rel, nil
}

// DeleteAsset deletes a release asset by its API URL. A 404 counts as
// success (already gone).
func (c *Client) DeleteAsset(assetURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	resp, err := c.send(ctx, http.MethodDelete, assetURL, nil)
	if err != nil {
		return err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return apiError(resp.StatusCode, data, assetURL)
}

// UploadAsset streams filePath to the release as an octet-stream with
// Content-Length. releaseRef is the release's upload_url (which may carry a
// {?name,label} template) or a bare numeric release id. repo and tag are
// used to build the returned browser download URL:
// https://github.com/{repo}/releases/download/{tag}/{name}.
func (c *Client) UploadAsset(repo, releaseRef, tag, name, filePath string) (string, error) {
	endpoint, err := uploadEndpoint(repo, releaseRef, name)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", mediaType)
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", name, err)
	}
	var out Asset
	if _, herr := handleResponse(resp, &out, nil, endpoint); herr != nil {
		return "", fmt.Errorf("upload %s: %w", name, herr)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, name), nil
}

func uploadEndpoint(repo, releaseRef, name string) (string, error) {
	ref := strings.TrimSpace(releaseRef)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", uploadsBase, repo, id, url.QueryEscape(name)), nil
	}
	endpoint := strings.SplitN(ref, "{", 2)[0]
	if endpoint == "" {
		return "", fmt.Errorf("release upload url is empty")
	}
	return endpoint + "?name=" + url.QueryEscape(name), nil
}

// Contents is a file fetched from the Contents API, content already decoded.
type Contents struct {
	SHA     string
	Content []byte
}

// GetContents fetches a file from the repo (base64 content is decoded).
func (c *Client) GetContents(repo, path, branch string) (*Contents, error) {
	u := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", apiBase, repo, escapePath(path), url.QueryEscape(branch))
	var out struct {
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	_, err := c.doJSON(http.MethodGet, u, nil, &out, nil)
	if err != nil {
		return nil, err
	}
	cn := &Contents{SHA: out.SHA}
	if out.Content != "" {
		clean := strings.ReplaceAll(out.Content, "\n", "")
		dec, derr := base64.StdEncoding.DecodeString(clean)
		if derr != nil {
			return nil, fmt.Errorf("decode contents of %s: %w", path, derr)
		}
		cn.Content = dec
	}
	return cn, nil
}

// PutContents creates or updates a file via the Contents API. The current
// blob sha is fetched first so create (no sha) and update (with sha) both
// work from one call.
func (c *Client) PutContents(repo, path, branch, message string, content []byte) error {
	in := map[string]string{
		"message": message,
		"branch":  branch,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if existing, err := c.GetContents(repo, path, branch); err == nil && existing.SHA != "" {
		in["sha"] = existing.SHA
	}
	u := fmt.Sprintf("%s/repos/%s/contents/%s", apiBase, repo, escapePath(path))
	_, err := c.doJSON(http.MethodPut, u, in, nil, nil)
	return err
}

// GetUser verifies the token and returns the GitHub login plus the raw
// X-GitHub-Scopes response header.
func (c *Client) GetUser() (login, scopes string, err error) {
	var out struct {
		Login string `json:"login"`
	}
	headers, err := c.doJSON(http.MethodGet, apiBase+"/user", nil, &out, []string{"X-GitHub-Scopes"})
	if err != nil {
		return "", "", err
	}
	return out.Login, headers["X-GitHub-Scopes"], nil
}

// escapePath URL-escapes each slash-separated path segment.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
