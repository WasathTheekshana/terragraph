// Package client submits scan reports to the TerraGraph server's ingest API
// (docs/design.md §7: POST /api/v1/scans).
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"terragraph/scanner/internal/report"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    baseURL,
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SubmitScan POSTs the report to {BaseURL}/api/v1/scans and errors on any
// non-2xx response.
func (c *Client) SubmitScan(r report.ScanReport) error {
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshaling scan report: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/scans", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("submitting scan to %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server rejected scan (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}
