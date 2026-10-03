package telegraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RetryAfterError indicates flood control rate limiting from Telegraph.
type RetryAfterError struct {
	Seconds int
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("flood control, retry in %ds", e.Seconds)
}

// TelegraphError represents an API-level error from Telegraph.
type TelegraphError struct {
	Message string
}

func (e *TelegraphError) Error() string {
	return e.Message
}

// Page represents a published Telegraph page.
type Page struct {
	Path        string `json:"path"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Views       int    `json:"views,omitempty"`
}

// Client interacts with the Telegraph API for a specific domain.
type Client struct {
	domain      string
	accessToken string
	client      *http.Client
}

// NewClient creates a new Telegraph API client for domain (default "graph.org").
func NewClient(accessToken, domain string, client *http.Client) *Client {
	if domain == "" {
		domain = "graph.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		domain:      domain,
		accessToken: accessToken,
		client:      client,
	}
}

// Domain returns the domain this client targets.
func (c *Client) Domain() string {
	return c.domain
}

// AccessToken returns the current access token.
func (c *Client) AccessToken() string {
	return c.accessToken
}

func (c *Client) call(ctx context.Context, method, pathSuffix string, values url.Values) (json.RawMessage, error) {
	if values == nil {
		values = url.Values{}
	}
	if c.accessToken != "" && values.Get("access_token") == "" {
		values.Set("access_token", c.accessToken)
	}

	endpoint := fmt.Sprintf("https://api.%s/%s", c.domain, method)
	if pathSuffix != "" {
		endpoint = fmt.Sprintf("%s/%s", endpoint, strings.TrimPrefix(pathSuffix, "/"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if retryAfter <= 0 {
			retryAfter = 5
		}
		return nil, &RetryAfterError{Seconds: retryAfter}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}

	if envelope.OK {
		return envelope.Result, nil
	}

	errStr := envelope.Error
	if strings.Contains(errStr, "FLOOD_WAIT") {
		clean := strings.ReplaceAll(errStr, "_", " ")
		fields := strings.Fields(clean)
		secs := 5
		if len(fields) > 0 {
			if s, convErr := strconv.Atoi(fields[len(fields)-1]); convErr == nil && s > 0 {
				secs = s
			}
		}
		return nil, &RetryAfterError{Seconds: secs}
	}

	return nil, &TelegraphError{Message: errStr}
}

// CreateAccount registers a new Telegraph account.
func (c *Client) CreateAccount(ctx context.Context, shortName, authorName, authorURL string) (string, error) {
	v := url.Values{"short_name": {shortName}}
	if authorName != "" {
		v.Set("author_name", authorName)
	}
	if authorURL != "" {
		v.Set("author_url", authorURL)
	}

	raw, err := c.call(ctx, "createAccount", "", v)
	if err != nil {
		return "", err
	}

	var res struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	c.accessToken = res.AccessToken
	return res.AccessToken, nil
}

// CreatePage publishes a new page.
func (c *Client) CreatePage(ctx context.Context, title string, contentNodes []any, authorName, authorURL string, returnContent bool) (*Page, error) {
	contentJSON, err := NodesToJSON(contentNodes)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize content nodes: %w", err)
	}

	v := url.Values{
		"title":   {title},
		"content": {contentJSON},
	}
	if authorName != "" {
		v.Set("author_name", authorName)
	}
	if authorURL != "" {
		v.Set("author_url", authorURL)
	}
	if returnContent {
		v.Set("return_content", "true")
	}

	raw, err := c.call(ctx, "createPage", "", v)
	if err != nil {
		return nil, err
	}

	var p Page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.URL == "" && p.Path != "" {
		p.URL = fmt.Sprintf("https://%s/%s", c.domain, p.Path)
	}
	return &p, nil
}

// EditPage updates an existing page.
func (c *Client) EditPage(ctx context.Context, path, title string, contentNodes []any, authorName, authorURL string, returnContent bool) (*Page, error) {
	if path == "" {
		return nil, errors.New("page path is required for editing")
	}
	contentJSON, err := NodesToJSON(contentNodes)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize content nodes: %w", err)
	}

	v := url.Values{
		"title":   {title},
		"content": {contentJSON},
	}
	if authorName != "" {
		v.Set("author_name", authorName)
	}
	if authorURL != "" {
		v.Set("author_url", authorURL)
	}
	if returnContent {
		v.Set("return_content", "true")
	}

	raw, err := c.call(ctx, "editPage", path, v)
	if err != nil {
		return nil, err
	}

	var p Page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.URL == "" && p.Path != "" {
		p.URL = fmt.Sprintf("https://%s/%s", c.domain, p.Path)
	}
	return &p, nil
}

// GetPage retrieves page metadata and optional content.
func (c *Client) GetPage(ctx context.Context, path string, returnContent bool) (*Page, error) {
	v := url.Values{}
	if returnContent {
		v.Set("return_content", "true")
	}

	raw, err := c.call(ctx, "getPage", path, v)
	if err != nil {
		return nil, err
	}

	var p Page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.URL == "" && p.Path != "" {
		p.URL = fmt.Sprintf("https://%s/%s", c.domain, p.Path)
	}
	return &p, nil
}
