// Package formance implements ledger.Client on the Formance Ledger v2 REST API
// (ghcr.io/formancehq/ledger v2.4). Auth is the deployment's business: pass an http.Client
// that adds the token, or none when the ledger is reachable only inside the network.
package formance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger"
)

// Client talks to one ledger of one Formance server.
type Client struct {
	base   string
	ledger string
	http   *http.Client
}

// Option configures New.
type Option func(*Client)

// WithHTTPClient uses the given client (timeouts, auth transport) instead of a 10 s default.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a client of the ledger named name at baseURL (http://ledger:3068).
func New(baseURL, name string, opts ...Option) *Client {
	c := &Client{base: strings.TrimRight(baseURL, "/"), ledger: name, http: &http.Client{Timeout: 10 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name is the ledger's name.
func (c *Client) Name() string { return c.ledger }

// APIError is an answer the ledger gave with an error code (4xx).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("formance: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// Error codes of the v2 API we act on.
const (
	codeConflict         = "CONFLICT"
	codeInsufficientFund = "INSUFFICIENT_FUND"
	codeLedgerExists     = "LEDGER_ALREADY_EXISTS"
	codeNotFound         = "NOT_FOUND"
)

// Ping checks the server is up (GET /_healthcheck).
func (c *Client) Ping(ctx context.Context) error {
	var out json.RawMessage
	return c.do(ctx, http.MethodGet, "/_healthcheck", nil, nil, &out)
}

// EnsureLedger creates the ledger if it does not exist (POST /v2/{ledger}).
func (c *Client) EnsureLedger(ctx context.Context) error {
	err := c.do(ctx, http.MethodPost, "/v2/"+url.PathEscape(c.ledger), nil, map[string]any{}, nil)
	var api *APIError
	if errors.As(err, &api) && api.Code == codeLedgerExists {
		return nil
	}
	return err
}

type txBody struct {
	Timestamp       string                       `json:"timestamp,omitempty"`
	Script          ledger.Script                `json:"script"`
	Reference       string                       `json:"reference"`
	Metadata        map[string]string            `json:"metadata"`
	AccountMetadata map[string]map[string]string `json:"accountMetadata,omitempty"`
}

type txData struct {
	ID        int64             `json:"id"`
	Reference string            `json:"reference"`
	Timestamp time.Time         `json:"timestamp"`
	Postings  []ledger.Posting  `json:"postings"`
	Metadata  map[string]string `json:"metadata"`
	Reverted  bool              `json:"reverted"`
}

func (d txData) tx() ledger.Tx {
	return ledger.Tx{ID: d.ID, Reference: d.Reference, Timestamp: d.Timestamp, Postings: d.Postings, Metadata: d.Metadata, Reverted: d.Reverted}
}

// Post runs the script (POST /v2/{ledger}/transactions) with the reference as Idempotency-Key.
// The same request again is answered with the stored transaction (Idempotency-Hit); the same
// key with a different body is refused by the ledger (ErrConflict); a reference that was used
// without the key answers 409 CONFLICT and the existing transaction is looked up. An
// insufficient balance is ErrInsufficientFunds, any other 4xx ErrRejected, 5xx or a network
// error ErrUnavailable.
func (c *Client) Post(ctx context.Context, tx ledger.Transaction) (ledger.Tx, bool, error) {
	if tx.Reference == "" {
		return ledger.Tx{}, false, fmt.Errorf("%w: a reference is required", ledger.ErrRejected)
	}
	body := txBody{Script: tx.Script, Reference: tx.Reference, Metadata: tx.Metadata, AccountMetadata: tx.AccountMetadata}
	if body.Metadata == nil {
		body.Metadata = map[string]string{}
	}
	if !tx.Timestamp.IsZero() {
		body.Timestamp = tx.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	var out struct {
		Data txData `json:"data"`
	}
	headers := http.Header{"Idempotency-Key": {tx.Reference}}
	hit, err := c.doHeaders(ctx, http.MethodPost, "/v2/"+url.PathEscape(c.ledger)+"/transactions", headers, body, &out)
	var api *APIError
	switch {
	case err == nil:
		return out.Data.tx(), hit, nil
	case errors.As(err, &api) && api.Code == codeConflict:
		existing, lookupErr := c.ByReference(ctx, tx.Reference)
		if lookupErr != nil {
			return ledger.Tx{}, false, lookupErr
		}
		if existing == nil {
			return ledger.Tx{}, false, fmt.Errorf("%w: reference %s conflicts but is not found", ledger.ErrUnavailable, tx.Reference)
		}
		return *existing, true, nil
	case errors.As(err, &api) && api.Code == codeInsufficientFund:
		return ledger.Tx{}, false, fmt.Errorf("%w: %s", ledger.ErrInsufficientFunds, api.Message)
	case errors.As(err, &api) && strings.Contains(strings.ToLower(api.Message), "idempotency"):
		return ledger.Tx{}, false, fmt.Errorf("%w: %s", ledger.ErrConflict, api.Message)
	case errors.As(err, &api) && api.Status >= 400 && api.Status < 500:
		return ledger.Tx{}, false, fmt.Errorf("%w: %v", ledger.ErrRejected, err)
	}
	return ledger.Tx{}, false, err
}

// ByReference finds the transaction with the reference, or nil.
func (c *Client) ByReference(ctx context.Context, reference string) (*ledger.Tx, error) {
	var found *ledger.Tx
	err := c.Transactions(ctx, ledger.Query{Reference: reference}, func(tx ledger.Tx) error {
		if found == nil {
			t := tx
			found = &t
		}
		return nil
	})
	return found, err
}

type cursorResponse[T any] struct {
	Cursor struct {
		HasMore bool   `json:"hasMore"`
		Next    string `json:"next"`
		Data    []T    `json:"data"`
	} `json:"cursor"`
}

const pageSize = 100

// Transactions lists the transactions matching q, newest first, page by page.
func (c *Client) Transactions(ctx context.Context, q ledger.Query, fn func(ledger.Tx) error) error {
	var clauses []map[string]any
	if q.Reference != "" {
		clauses = append(clauses, match("reference", q.Reference))
	}
	if q.Account != "" {
		clauses = append(clauses, match("account", q.Account))
	}
	for k, v := range q.Metadata {
		clauses = append(clauses, match("metadata["+k+"]", v))
	}
	if q.Start != nil {
		clauses = append(clauses, map[string]any{"$gte": map[string]any{"timestamp": q.Start.UTC().Format(time.RFC3339Nano)}})
	}
	if q.End != nil {
		clauses = append(clauses, map[string]any{"$lt": map[string]any{"timestamp": q.End.UTC().Format(time.RFC3339Nano)}})
	}
	return c.list(ctx, "/v2/"+url.PathEscape(c.ledger)+"/transactions", clauses, nil, func(d txData) error { return fn(d.tx()) })
}

type accountData struct {
	Address  string            `json:"address"`
	Metadata map[string]string `json:"metadata"`
	Volumes  map[string]struct {
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
		Balance int64 `json:"balance"`
	} `json:"volumes"`
}

func (a accountData) account() ledger.Account {
	out := ledger.Account{Address: a.Address, Metadata: a.Metadata, Balances: map[string]int64{}}
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	for asset, v := range a.Volumes {
		out.Balances[asset] = v.Balance
	}
	return out
}

// Account reads one account with its volumes (GET /v2/{ledger}/accounts/{address}), or nil
// when the ledger has never seen it.
func (c *Client) Account(ctx context.Context, address string) (*ledger.Account, error) {
	var out struct {
		Data accountData `json:"data"`
	}
	path := "/v2/" + url.PathEscape(c.ledger) + "/accounts/" + url.PathEscape(address) + "?expand=volumes"
	err := c.do(ctx, http.MethodGet, path, nil, nil, &out)
	var api *APIError
	if errors.As(err, &api) && (api.Status == http.StatusNotFound || api.Code == codeNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	acc := out.Data.account()
	return &acc, nil
}

// Accounts lists the accounts matching q with their volumes.
func (c *Client) Accounts(ctx context.Context, q ledger.AccountQuery, fn func(ledger.Account) error) error {
	var clauses []map[string]any
	if q.Address != "" {
		clauses = append(clauses, match("address", q.Address))
	}
	for k, v := range q.Metadata {
		clauses = append(clauses, match("metadata["+k+"]", v))
	}
	params := url.Values{"expand": {"volumes"}}
	return c.list(ctx, "/v2/"+url.PathEscape(c.ledger)+"/accounts", clauses, params, func(d accountData) error { return fn(d.account()) })
}

// SetAccountMetadata merges metadata into the account's (POST /v2/{ledger}/accounts/{address}/metadata).
func (c *Client) SetAccountMetadata(ctx context.Context, address string, metadata map[string]string) error {
	path := "/v2/" + url.PathEscape(c.ledger) + "/accounts/" + url.PathEscape(address) + "/metadata"
	err := c.do(ctx, http.MethodPost, path, nil, metadata, nil)
	var api *APIError
	if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
		return fmt.Errorf("%w: %v", ledger.ErrRejected, err)
	}
	return err
}

func match(property string, value any) map[string]any {
	return map[string]any{"$match": map[string]any{property: value}}
}

func list[T any](ctx context.Context, c *Client, path string, clauses []map[string]any, params url.Values, fn func(T) error) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("pageSize", fmt.Sprint(pageSize))
	switch len(clauses) {
	case 0:
	case 1:
		b, err := json.Marshal(clauses[0])
		if err != nil {
			return err
		}
		params.Set("query", string(b))
	default:
		b, err := json.Marshal(map[string]any{"$and": clauses})
		if err != nil {
			return err
		}
		params.Set("query", string(b))
	}
	extra := url.Values{}
	if e := params.Get("expand"); e != "" {
		extra.Set("expand", e)
	}
	for {
		var out cursorResponse[T]
		if err := c.do(ctx, http.MethodGet, path+"?"+params.Encode(), nil, nil, &out); err != nil {
			return err
		}
		for _, d := range out.Cursor.Data {
			if err := fn(d); err != nil {
				return err
			}
		}
		if !out.Cursor.HasMore || out.Cursor.Next == "" {
			return nil
		}
		params = url.Values{"cursor": {out.Cursor.Next}}
		for k, v := range extra {
			params[k] = v
		}
	}
}

func (c *Client) list(ctx context.Context, path string, clauses []map[string]any, params url.Values, fn any) error {
	switch f := fn.(type) {
	case func(txData) error:
		return list(ctx, c, path, clauses, params, f)
	case func(accountData) error:
		return list(ctx, c, path, clauses, params, f)
	}
	panic("formance: unsupported list callback")
}

func (c *Client) do(ctx context.Context, method, path string, headers http.Header, body, out any) error {
	_, err := c.doHeaders(ctx, method, path, headers, body, out)
	return err
}

// doHeaders sends one request and decodes the answer into out (nil = ignore the body). It
// reports whether the ledger answered from its idempotency store. 4xx answers are *APIError;
// anything else that is not 2xx is ledger.ErrUnavailable.
func (c *Client) doHeaders(ctx context.Context, method, path string, headers http.Header, body, out any) (bool, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header[k] = v
	}
	res, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %s %s: %v", ledger.ErrUnavailable, method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return false, fmt.Errorf("%w: read %s %s: %v", ledger.ErrUnavailable, method, path, err)
	}
	hit := strings.EqualFold(res.Header.Get("Idempotency-Hit"), "true")
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if out == nil || len(raw) == 0 {
			return hit, nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return hit, fmt.Errorf("formance: decode %s %s: %w", method, path, err)
		}
		return hit, nil
	}
	var apiErr struct {
		Code    string `json:"errorCode"`
		Message string `json:"errorMessage"`
	}
	_ = json.Unmarshal(raw, &apiErr)
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		msg := apiErr.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return false, &APIError{Status: res.StatusCode, Code: apiErr.Code, Message: msg}
	}
	return false, fmt.Errorf("%w: %s %s answered HTTP %d %s", ledger.ErrUnavailable, method, path, res.StatusCode, apiErr.Code)
}

var _ ledger.Client = (*Client)(nil)
