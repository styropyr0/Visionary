// Package d1 is a small client for the Cloudflare D1 REST API.
package d1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAPIBase is Cloudflare's v4 REST API.
const DefaultAPIBase = "https://api.cloudflare.com/client/v4"

// Response caps: writes return tiny bodies, reads can carry thousands of rows.
const (
	execResponseLimit  = 1 << 20
	queryResponseLimit = 16 << 20
)

// Rows is one statement's result in column order, from the raw endpoint.
type Rows struct {
	Columns []string
	Rows    [][]any
	// RowsRead and DurationMs come from D1's query metadata.
	RowsRead   int64
	DurationMs float64
}

type statement struct {
	SQL    string   `json:"sql"`
	Params []string `json:"params,omitempty"`
}

type Client struct {
	queryURL string
	rawURL   string
	token    string
	http     *http.Client
}

// New returns nil when any credential is missing. apiBase may be empty for
// Cloudflare's API.
func New(apiBase, accountID, databaseID, apiToken string) *Client {
	accountID, databaseID, apiToken = strings.TrimSpace(accountID), strings.TrimSpace(databaseID), strings.TrimSpace(apiToken)
	if accountID == "" || databaseID == "" || apiToken == "" {
		return nil
	}
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	base := fmt.Sprintf("%s/accounts/%s/d1/database/%s", apiBase, accountID, databaseID)
	return &Client{
		queryURL: base + "/query",
		rawURL:   base + "/raw",
		token:    apiToken,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

// Exec runs sql, which may hold several statements joined by semicolons.
// The REST API only accepts string params; column affinity converts numbers.
func (c *Client) Exec(ctx context.Context, sql string, params ...string) error {
	_, err := c.ExecChanges(ctx, sql, params...)
	return err
}

// ExecChanges is Exec that also reports how many rows the first statement
// changed.
func (c *Client) ExecChanges(ctx context.Context, sql string, params ...string) (int64, error) {
	raw, err := c.post(ctx, c.queryURL, statement{SQL: sql, Params: params}, execResponseLimit)
	if err != nil {
		return 0, err
	}
	return firstChanges(raw), nil
}

// Statement is one parameterized statement for Batch.
type Statement struct {
	SQL    string
	Params []string
}

// Batch runs statements in one call. D1 caps each at 100 bound params.
func (c *Client) Batch(ctx context.Context, stmts []Statement) error {
	if len(stmts) == 0 {
		return nil
	}
	body := struct {
		Batch []statement `json:"batch"`
	}{Batch: make([]statement, len(stmts))}
	for i, s := range stmts {
		body.Batch[i] = statement{SQL: s.SQL, Params: s.Params}
	}
	_, err := c.post(ctx, c.queryURL, body, execResponseLimit)
	return err
}

func firstChanges(raw json.RawMessage) int64 {
	var res []struct {
		Meta struct {
			Changes int64 `json:"changes"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &res) != nil || len(res) == 0 {
		return 0
	}
	return res[0].Meta.Changes
}

// Query runs one statement and returns its rows as arrays in column order.
func (c *Client) Query(ctx context.Context, sql string, params ...string) (*Rows, error) {
	raw, err := c.post(ctx, c.rawURL, statement{SQL: sql, Params: params}, queryResponseLimit)
	if err != nil {
		return nil, err
	}
	var results []struct {
		Results struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		} `json:"results"`
		Meta struct {
			RowsRead int64   `json:"rows_read"`
			Duration float64 `json:"duration"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("d1: decode rows: %w", err)
	}
	if len(results) == 0 {
		return nil, errors.New("d1: empty result")
	}
	r := results[0]
	rows := r.Results.Rows
	if rows == nil {
		rows = [][]any{}
	}
	return &Rows{Columns: r.Results.Columns, Rows: rows, RowsRead: r.Meta.RowsRead, DurationMs: r.Meta.Duration}, nil
}

type envelope struct {
	Success bool            `json:"success"`
	Errors  []message       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type message struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// StatusError is a request D1 rejected, such as a SQL error.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// post sends body to url and returns the result once D1 reports success.
// Bodies larger than limit are rejected rather than cut mid-JSON.
func (c *Client) post(ctx context.Context, url string, body any, limit int64) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("d1: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("d1: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("d1: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("d1: read response: %w", err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("d1: response larger than %d bytes", limit)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("d1: status %d: unreadable response", resp.StatusCode)
	}
	if !env.Success || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode, Message: joinMessages(env.Errors)}
	}
	return env.Result, nil
}

func joinMessages(msgs []message) string {
	if len(msgs) == 0 {
		return "unknown D1 error"
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Message)
	}
	return strings.Join(parts, "; ")
}
