// Package ledger is the small view of a Formance Ledger that the wallet layer needs: post a
// Numscript transaction under a reference, read transactions and accounts back, set account
// metadata. The wallet package depends on the Client interface only, so a fake is trivial and
// another engine with the same semantics (references, metadata, Numscript) could sit behind it.
//
// Amounts are integers in the asset's minor unit (KWD/3: 1.000 KWD is 1000), as in the ledger.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Posting is one movement of one asset between two accounts.
type Posting struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Amount      int64  `json:"amount"`
	Asset       string `json:"asset"`
}

// Script is a Numscript program and its variables. A monetary variable is written as
// "ASSET AMOUNT" (Monetary); accounts, assets and strings as themselves.
type Script struct {
	Plain string            `json:"plain"`
	Vars  map[string]string `json:"vars,omitempty"`
}

// Monetary writes a monetary variable: "KWD/3 1000".
func Monetary(asset string, amount int64) string {
	return asset + " " + strconv.FormatInt(amount, 10)
}

// Transaction is what Post sends: one Numscript run under a reference. The reference is the
// business key, unique per ledger; the ledger refuses a second transaction with the same one.
type Transaction struct {
	Reference string
	// Timestamp is when the money moved; zero means now.
	Timestamp time.Time
	Script    Script
	Metadata  map[string]string
	// AccountMetadata is set on accounts in the same transaction (address -> metadata).
	AccountMetadata map[string]map[string]string
}

// Tx is a transaction read back from the ledger.
type Tx struct {
	ID        int64
	Reference string
	Timestamp time.Time
	Postings  []Posting
	Metadata  map[string]string
	Reverted  bool
}

// Query selects transactions: every condition given must hold.
type Query struct {
	Reference string
	// Account matches transactions touching the account, as source or destination. The ledger's
	// address patterns apply: "wallet:abc:" is every account under wallet:abc, a segment left
	// empty matches any one segment, a trailing "..." any depth.
	Account  string
	Metadata map[string]string
	// Start (inclusive) and End (exclusive) bound the transactions' timestamps.
	Start, End *time.Time
}

// Account is an account read back from the ledger, with its balance per asset.
type Account struct {
	Address  string
	Metadata map[string]string
	Balances map[string]int64
}

// AccountQuery selects accounts: an address pattern (see Query.Account) and metadata that must
// match exactly. Every condition given must hold.
type AccountQuery struct {
	Address  string
	Metadata map[string]string
}

// Client is what the wallet layer needs from the engine.
type Client interface {
	// Post records the transaction and returns it. A reference already in the ledger returns
	// the existing transaction with replayed = true (a retry after a lost answer books nothing
	// twice); the same reference with a different script is ErrConflict.
	Post(ctx context.Context, tx Transaction) (result Tx, replayed bool, err error)
	// ByReference returns the transaction with the reference, or nil.
	ByReference(ctx context.Context, reference string) (*Tx, error)
	// Transactions calls fn for every transaction matching q, newest first.
	Transactions(ctx context.Context, q Query, fn func(Tx) error) error
	// Account reads one account with its balances, or nil when the ledger has never seen it.
	Account(ctx context.Context, address string) (*Account, error)
	// Accounts calls fn for every account matching q.
	Accounts(ctx context.Context, q AccountQuery, fn func(Account) error) error
	// SetAccountMetadata merges metadata into the account's (the account is created if new).
	SetAccountMetadata(ctx context.Context, address string, metadata map[string]string) error
	// Ping checks the ledger is up.
	Ping(ctx context.Context) error
}

// Errors every Client maps the engine's answers to.
var (
	// ErrUnavailable: the ledger could not be reached or answered 5xx; retry later.
	ErrUnavailable = errors.New("ledger: unavailable")
	// ErrRejected: the ledger refused the transaction for good (bad script, unbalanced,
	// unknown account in a metadata read); retrying the same request cannot help.
	ErrRejected = errors.New("ledger: transaction rejected")
	// ErrInsufficientFunds: a source without overdraft had less than the amount.
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	// ErrConflict: the reference exists with a different transaction.
	ErrConflict = errors.New("ledger: reference used by a different transaction")
)

// Balance is a convenience on Account: the balance of one asset (0 when absent).
func (a *Account) Balance(asset string) int64 {
	if a == nil {
		return 0
	}
	return a.Balances[asset]
}

// String formats a posting for logs.
func (p Posting) String() string {
	return fmt.Sprintf("%s -> %s %s %d", p.Source, p.Destination, p.Asset, p.Amount)
}
