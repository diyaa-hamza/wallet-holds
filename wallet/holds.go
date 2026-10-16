package wallet

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger"
)

// Hold is one authorization as the ledger knows it: the hold account's metadata and balance.
type Hold struct {
	WalletID  string    `json:"walletId"`
	Processor string    `json:"processor"`
	AuthID    string    `json:"authId"`
	Asset     string    `json:"asset"`
	Amount    int64     `json:"amount"`    // authorized
	Remaining int64     `json:"remaining"` // not yet captured or released
	Status    string    `json:"status"`    // open, captured, released, expired
	ExpiresAt time.Time `json:"expiresAt"`
	ClosedAt  time.Time `json:"closedAt,omitzero"`
	// Destination is where captures go; Metadata the rest of the account's metadata.
	Destination string            `json:"destination"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Expired reports whether an open hold is past its expiry at now.
func (h Hold) Expired(now time.Time) bool {
	return h.Status == StatusOpen && !h.ExpiresAt.IsZero() && !now.Before(h.ExpiresAt)
}

func holdOf(a ledger.Account) (Hold, bool) {
	md := a.Metadata
	if md[MetaKind] != KindHold {
		return Hold{}, false
	}
	h := Hold{
		WalletID: md[MetaWallet], Processor: md[MetaProcessor], AuthID: md[MetaAuthID], Asset: md[MetaAsset],
		Status: md[MetaStatus], Destination: md[metaDestination], Metadata: map[string]string{},
	}
	h.Amount, _ = strconv.ParseInt(md[MetaAmount], 10, 64)
	h.Remaining = a.Balances[h.Asset]
	h.ExpiresAt, _ = time.Parse(time.RFC3339, md[MetaExpiresAt])
	h.ClosedAt, _ = time.Parse(time.RFC3339, md[MetaClosedAt])
	reserved := map[string]bool{
		MetaKind: true, MetaWallet: true, MetaProcessor: true, MetaAuthID: true, MetaAsset: true, MetaAmount: true,
		MetaStatus: true, MetaExpiresAt: true, MetaClosedAt: true, metaDestination: true, metaVoidDestination: true,
	}
	for k, v := range md {
		if !reserved[k] {
			h.Metadata[k] = v
		}
	}
	return h, true
}

// Hold reads one hold of the wallet; ErrHoldNotFound when there is none.
func (s *Service) Hold(ctx context.Context, walletID, authID string) (Hold, error) {
	a, err := s.ledger.Account(ctx, s.chart.Hold(walletID, authID))
	if err != nil {
		return Hold{}, err
	}
	if a == nil {
		return Hold{}, fmt.Errorf("%w: wallet %s authorization %s", ErrHoldNotFound, walletID, authID)
	}
	h, ok := holdOf(*a)
	if !ok {
		return Hold{}, fmt.Errorf("%w: wallet %s authorization %s (account without hold metadata)", ErrHoldNotFound, walletID, authID)
	}
	return h, nil
}

// Holds lists the open holds of the wallet.
func (s *Service) Holds(ctx context.Context, walletID string) ([]Hold, error) {
	if err := ValidateID("wallet", walletID); err != nil {
		return nil, err
	}
	var out []Hold
	err := s.OpenHolds(ctx, walletID, func(h Hold) error {
		out = append(out, h)
		return nil
	})
	return out, err
}

// OpenHolds calls fn for every open hold of the wallet, or of every wallet when walletID is
// empty (the sweeper's view).
func (s *Service) OpenHolds(ctx context.Context, walletID string, fn func(Hold) error) error {
	q := ledger.AccountQuery{Address: s.chart.HoldPattern(walletID), Metadata: map[string]string{MetaKind: KindHold, MetaStatus: StatusOpen}}
	return s.ledger.Accounts(ctx, q, func(a ledger.Account) error {
		if h, ok := holdOf(a); ok {
			return fn(h)
		}
		return nil
	})
}

// Balances is the wallet's spendable balance per asset (holds excluded). A wallet the ledger
// has never seen has no balances and no error.
func (s *Service) Balances(ctx context.Context, walletID string) (map[string]int64, error) {
	if err := ValidateID("wallet", walletID); err != nil {
		return nil, err
	}
	a, err := s.ledger.Account(ctx, s.chart.Main(walletID))
	if err != nil {
		return nil, err
	}
	if a == nil {
		return map[string]int64{}, nil
	}
	return a.Balances, nil
}

// Statement calls fn for every transaction touching the wallet (main and holds) in the
// window, newest first.
func (s *Service) Statement(ctx context.Context, walletID string, start, end *time.Time, fn func(ledger.Tx) error) error {
	if err := ValidateID("wallet", walletID); err != nil {
		return err
	}
	return s.ledger.Transactions(ctx, ledger.Query{Account: s.chart.WalletPattern(walletID), Start: start, End: end}, fn)
}
