package tbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const baseURL = "https://pan.sjtu.edu.cn"

// Client wraps an HTTP client for communicating with the Tbox (SMH) API.
type Client struct {
	http *http.Client
}

// NewClient creates a Client with redirect-following enabled.
func NewClient() *Client {
	return &Client{
		http: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 20 {
					return fmt.Errorf("too many redirects")
				}
				// Preserve Cookie header across redirects.
				if len(via) > 0 {
					for key, vals := range via[0].Header {
						if strings.EqualFold(key, "Cookie") {
							for _, v := range vals {
								req.Header.Set(key, v)
							}
						}
					}
				}
				return nil
			},
		},
	}
}

// urlEncodeByParts encodes each segment of a slash-delimited path individually,
// leaving the slash separators untouched (mirrors C# UrlEncodeByParts).
func urlEncodeByParts(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func isOK(code int) bool { return code >= 200 && code < 300 }

// readBody reads all bytes from r, always closing it.
func readBody(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	return io.ReadAll(r)
}

// apiError tries to decode an ErrorMessageDto from body and returns a formatted error.
func apiError(statusCode int, body []byte) error {
	var e ErrorMessageDto
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return fmt.Errorf("[%s] %s", e.Code, e.Message)
	}
	return fmt.Errorf("server returned %d", statusCode)
}

// LoginUseJaccount logs in using the JAAuthCookie and returns a LoginResDto.
// It follows the SSO redirect, extracts the OAuth code, then posts to the verify endpoint.
func (c *Client) LoginUseJaccount(cookie string) (*LoginResDto, error) {
	req, err := http.NewRequest("GET",
		"https://pan.sjtu.edu.cn/user/v1/sign-in/sso-login-redirect/xpw8ou8y?auto_redirect=true&from=web&custom_state=4ycSqbzfqM9mPuzOKmvTUQ%253D%253D",
		nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "JAAuthCookie="+cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	if resp.Request != nil && resp.Request.URL != nil &&
		strings.Contains(resp.Request.URL.Host, "jaccount") {
		return nil, fmt.Errorf("JAAccount authentication failed: cookie may be expired")
	}

	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	re := regexp.MustCompile(`code=([^&]+)`)
	match := re.FindStringSubmatch(finalURL)
	if len(match) < 2 {
		return nil, fmt.Errorf("SSO code not found in callback URL: %s", finalURL)
	}
	code := match[1]

	req2, err := http.NewRequest("POST",
		fmt.Sprintf("https://pan.sjtu.edu.cn/user/v1/sign-in/verify-account-login/xpw8ou8y?device_id=Chrome+116.0.0.0&type=sso&credential=%s", code),
		nil)
	if err != nil {
		return nil, err
	}

	resp2, err := c.http.Do(req2)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp2.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp2.StatusCode) {
		return nil, apiError(resp2.StatusCode, body)
	}

	var result LoginResDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("login failed: %s", result.Message)
	}
	return &result, nil
}

// GetSpace retrieves the personal space credentials for a userToken.
func (c *Client) GetSpace(userToken string) (*SpaceCred, error) {
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/user/v1/space/1/personal?user_token=%s", baseURL, url.QueryEscape(userToken)),
		nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result SpaceCred
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("GetSpace failed: %s", result.Message)
	}
	return &result, nil
}

// GetSpaceQuotaInfo retrieves quota information for the personal space.
func (c *Client) GetSpaceQuotaInfo(userToken string) (*SpaceQuotaInfo, error) {
	req, err := http.NewRequest("GET",
		fmt.Sprintf("%s/user/v1/space/1?user_token=%s", baseURL, url.QueryEscape(userToken)),
		nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result SpaceQuotaInfo
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetItemInfo retrieves metadata for either a file or directory at path.
func (c *Client) GetItemInfo(cred *SpaceCred, path string) (*MergedItemDto, error) {
	u := fmt.Sprintf("%s/api/v1/directory/%s/%s/%s?info&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result MergedItemDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListItems retrieves a single page of directory contents (1-indexed pages).
func (c *Client) ListItems(cred *SpaceCred, path string, page, pageSize int) (*ItemListDto, error) {
	u := fmt.Sprintf("%s/api/v1/directory/%s/%s/%s?page=%d&page_size=%d&order_by=name&order_by_type=asc&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		page,
		pageSize,
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result ItemListDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetFileStream opens an HTTP stream for downloading a file, optionally with a Range header.
// Returns the response body, the Content-Length (-1 if unknown), and any error.
func (c *Client) GetFileStream(cred *SpaceCred, path string, start, end int64) (io.ReadCloser, int64, error) {
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}

	if start >= 0 {
		if end >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if !isOK(resp.StatusCode) {
		body, _ := readBody(resp.Body)
		return nil, 0, apiError(resp.StatusCode, body)
	}
	return resp.Body, resp.ContentLength, nil
}

// StartChunkUpload initiates a multipart upload session and returns upload credentials.
func (c *Client) StartChunkUpload(cred *SpaceCred, path string, chunkCount int) (*StartChunkUploadResDto, error) {
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?multipart&conflict_resolution_strategy=rename&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)

	// Build partNumberRange array [1,2,...,min(chunkCount,50)]
	var sb strings.Builder
	sb.WriteString("[1")
	limit := chunkCount
	if limit > 50 {
		limit = 50
	}
	for i := 2; i <= limit; i++ {
		sb.WriteString(fmt.Sprintf(",%d", i))
	}
	sb.WriteString("]")

	bodyStr := fmt.Sprintf(`{"partNumberRange":%s}`, sb.String())
	req, err := http.NewRequest("POST", u, strings.NewReader(bodyStr))
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result StartChunkUploadResDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("StartChunkUpload failed: %s", result.Message)
	}
	return &result, nil
}

// RenewChunkUpload refreshes upload credentials for the given parts list.
func (c *Client) RenewChunkUpload(cred *SpaceCred, confirmKey string, parts []int) (*StartChunkUploadResDto, error) {
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?renew&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		confirmKey,
		url.QueryEscape(cred.AccessToken),
	)

	limit := len(parts)
	if limit > 50 {
		limit = 50
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%d", parts[0]))
	for i := 1; i < limit; i++ {
		sb.WriteString(fmt.Sprintf(",%d", parts[i]))
	}
	sb.WriteString("]")

	bodyStr := fmt.Sprintf(`{"partNumberRange":%s}`, sb.String())
	req, err := http.NewRequest("POST", u, strings.NewReader(bodyStr))
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result StartChunkUploadResDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("RenewChunkUpload failed: %s", result.Message)
	}
	return &result, nil
}

// UploadChunk uploads a single chunk to S3 using credentials from info.
func (c *Client) UploadChunk(info *StartChunkUploadResDto, data io.Reader, partNumber int) error {
	partKey := fmt.Sprintf("%d", partNumber)
	part, ok := info.Parts[partKey]
	if !ok {
		return fmt.Errorf("part %d not found in upload info", partNumber)
	}

	u := fmt.Sprintf("https://%s%s?uploadId=%s&partNumber=%d",
		info.Domain,
		info.Path,
		url.QueryEscape(info.UploadId),
		partNumber,
	)
	req, err := http.NewRequest("PUT", u, data)
	if err != nil {
		return err
	}
	for k, v := range part.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if !isOK(resp.StatusCode) {
		return apiError(resp.StatusCode, body)
	}
	return nil
}

// ConfirmUpload finalises the multipart upload identified by confirmKey.
func (c *Client) ConfirmUpload(cred *SpaceCred, confirmKey string) (*ConfirmUploadResDto, error) {
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?confirm&conflict_resolution_strategy=overwrite&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		confirmKey,
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("POST", u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isOK(resp.StatusCode) {
		return nil, apiError(resp.StatusCode, body)
	}

	var result ConfirmUploadResDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("ConfirmUpload failed: %s", result.Message)
	}
	return &result, nil
}

// SimpleUpload uploads small files (≤ 4 MB) using the single-part presigned URL flow.
func (c *Client) SimpleUpload(cred *SpaceCred, path string, data io.Reader, size int64) error {
	// Step 1: obtain presigned upload info.
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?conflict_resolution_strategy=overwrite&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("PUT", u, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if !isOK(resp.StatusCode) {
		return apiError(resp.StatusCode, body)
	}

	var info SimpleUploadInfoDto
	if err := json.Unmarshal(body, &info); err != nil {
		return err
	}

	// Step 2: upload the actual data to the presigned URL.
	uploadURL := fmt.Sprintf("https://%s%s", info.Domain, info.Path)
	req2, err := http.NewRequest("PUT", uploadURL, data)
	if err != nil {
		return err
	}
	req2.ContentLength = size
	for k, v := range info.Headers {
		req2.Header.Set(k, v)
	}

	resp2, err := c.http.Do(req2)
	if err != nil {
		return err
	}
	body2, err := readBody(resp2.Body)
	if err != nil {
		return err
	}
	if !isOK(resp2.StatusCode) {
		return apiError(resp2.StatusCode, body2)
	}

	// Step 3: confirm.
	_, err = c.ConfirmUpload(cred, info.ConfirmKey)
	return err
}

// CreateDirectory creates a directory at dirPath.
func (c *Client) CreateDirectory(cred *SpaceCred, dirPath string) error {
	u := fmt.Sprintf("%s/api/v1/directory/%s/%s/%s?conflict_resolution_strategy=ask&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(dirPath, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("PUT", u, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if !isOK(resp.StatusCode) {
		return apiError(resp.StatusCode, body)
	}

	var result ErrorMessageDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil // Non-JSON success response is fine.
	}
	if result.Status != 0 {
		return fmt.Errorf("CreateDirectory failed: %s", result.Message)
	}
	return nil
}

// DeleteItem deletes a file or directory. It tries the file endpoint first,
// then falls back to the directory endpoint on failure.
func (c *Client) DeleteItem(cred *SpaceCred, path string) error {
	// Try file delete first.
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?permanent=0&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, _ := readBody(resp.Body)
	if isOK(resp.StatusCode) || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	// Fall back to directory delete.
	u2 := fmt.Sprintf("%s/api/v1/directory/%s/%s/%s?permanent=0&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(path, "/")),
		url.QueryEscape(cred.AccessToken),
	)
	req2, err := http.NewRequest("DELETE", u2, nil)
	if err != nil {
		return err
	}

	resp2, err := c.http.Do(req2)
	if err != nil {
		return err
	}
	// Drain the body; error is intentionally ignored since we already have
	// a fallback error from the first DELETE attempt to report.
	readBody(resp2.Body) //nolint: errcheck
	if isOK(resp2.StatusCode) || resp2.StatusCode == http.StatusNoContent {
		return nil
	}

	// Report the first error if both fail.
	return apiError(resp.StatusCode, body)
}

// CopyOrMoveItem copies (isMove=false) or moves (isMove=true) an item.
func (c *Client) CopyOrMoveItem(cred *SpaceCred, src, dst string, isMove bool) error {
	u := fmt.Sprintf("%s/api/v1/file/%s/%s/%s?conflict_resolution_strategy=ask&access_token=%s",
		baseURL,
		url.PathEscape(cred.LibraryId),
		url.PathEscape(cred.SpaceId),
		urlEncodeByParts(strings.TrimPrefix(dst, "/")),
		url.QueryEscape(cred.AccessToken),
	)

	key := "copyFrom"
	if isMove {
		key = "from"
	}
	bodyStr := fmt.Sprintf(`{"%s":"%s"}`, key, src)

	req, err := http.NewRequest("PUT", u, strings.NewReader(bodyStr))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if !isOK(resp.StatusCode) {
		return apiError(resp.StatusCode, body)
	}
	return nil
}
