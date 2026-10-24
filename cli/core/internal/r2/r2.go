// Package r2 uploads release artifacts to Cloudflare R2. The AWS Signature
// Version 4 signing is implemented by hand with the standard library only
// (crypto/hmac + sha256); R2 uses region "auto" and service "s3".
package r2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const uploadTimeout = 120 * time.Second

// Put uploads filePath to https://{accountID}.r2.cloudflarestorage.com/
// {bucket}/{key} and returns the public URL {publicBase}/{key} (empty when
// publicBase is not set).
func Put(bucket, publicBase, accountID, accessKey, secret, key, filePath string) (string, error) {
	payloadHash, size, err := hashFile(filePath)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	host := accountID + ".r2.cloudflarestorage.com"
	uri := "/" + escapeSegments(bucket) + "/" + escapeSegments(key)
	endpoint := "https://" + host + uri

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/auto/s3/aws4_request"

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodPut,
		uri,
		"", // empty query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(crHash[:])

	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte("auto"))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	authorization := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature)

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authorization)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("r2 upload: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("r2 upload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	public := strings.TrimRight(strings.TrimSpace(publicBase), "/")
	if public == "" {
		return "", nil
	}
	return public + "/" + key, nil
}

// hashFile streams the file through sha256 and reports its size.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// escapeSegments URL-escapes each slash-separated path segment.
func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
