// Package wallet turns a Formance Ledger into wallets with holds: a spendable balance per
// wallet in any number of assets, authorization holds that are captured, released or expired,
// transfers between wallets, conversions between assets, and the settlement of captured
// amounts with the processor that cleared them.
//
// The ledger is the only store. Every operation is one transaction under one reference, so a
// retry replays and never books twice; the open holds are the hold accounts with a balance and
// "open" metadata; nothing lives outside the ledger. The wallet id is opaque: the caller maps
// its customers, phone numbers or account numbers to it.
package wallet

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Chart is where the accounts live and how long a hold lasts. The zero value is not usable;
// start from DefaultChart.
type Chart struct {
	// WalletPrefix is the first segment of every wallet account: wallet:{id}:main.
	WalletPrefix string
	// MainSegment and HoldSegment name the balance and the hold accounts under a wallet.
	MainSegment string
	HoldSegment string
	// IssuerPrefix is the first segment of the processor accounts: issuer:{processor}:settlement
	// (captured amounts owed to it) and issuer:{processor}:external (settled, money gone).
	IssuerPrefix string
	// FXPrefix is the first segment of the conversion positions: fx:{sold}-{bought}.
	FXPrefix string
	// FXMarginAccount receives the margin of conversions.
	FXMarginAccount string
	// HoldExpiry is how long a hold lives when the authorization does not say.
	HoldExpiry time.Duration
}

// DefaultChart is the default chart: wallet:{id}:main,
// wallet:{id}:hold:{auth}, issuer:{processor}:settlement, fx:{pair}, revenue:fx_margin, holds
// of 7 days.
func DefaultChart() Chart {
	return Chart{
		WalletPrefix: "wallet", MainSegment: "main", HoldSegment: "hold",
		IssuerPrefix: "issuer", FXPrefix: "fx", FXMarginAccount: "revenue:fx_margin",
		HoldExpiry: 7 * 24 * time.Hour,
	}
}

var segment = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Validate checks every part of the chart is a valid ledger address segment.
func (c Chart) Validate() error {
	for name, s := range map[string]string{
		"WalletPrefix": c.WalletPrefix, "MainSegment": c.MainSegment, "HoldSegment": c.HoldSegment,
		"IssuerPrefix": c.IssuerPrefix, "FXPrefix": c.FXPrefix,
	} {
		if !segment.MatchString(s) {
			return fmt.Errorf("wallet: chart %s %q is not an address segment", name, s)
		}
	}
	if err := ValidateAddress(c.FXMarginAccount); err != nil {
		return fmt.Errorf("wallet: chart FXMarginAccount: %w", err)
	}
	if c.HoldExpiry <= 0 {
		return errors.New("wallet: chart HoldExpiry must be positive")
	}
	return nil
}

// Main is the balance account of the wallet.
func (c Chart) Main(walletID string) string {
	return c.WalletPrefix + ":" + walletID + ":" + c.MainSegment
}

// Hold is the account of one authorization of the wallet.
func (c Chart) Hold(walletID, authID string) string {
	return c.WalletPrefix + ":" + walletID + ":" + c.HoldSegment + ":" + authID
}

// HoldPattern matches every hold account of the wallet (or of every wallet when walletID is
// empty), in the ledger's address pattern syntax.
func (c Chart) HoldPattern(walletID string) string {
	return c.WalletPrefix + ":" + walletID + ":" + c.HoldSegment + ":"
}

// WalletPattern matches every account of the wallet.
func (c Chart) WalletPattern(walletID string) string {
	return c.WalletPrefix + ":" + walletID + ":..."
}

// Settlement is where captured amounts wait for the processor's settlement.
func (c Chart) Settlement(processor string) string {
	return c.IssuerPrefix + ":" + processor + ":settlement"
}

// External is the processor's side of a settlement (money that left).
func (c Chart) External(processor string) string {
	return c.IssuerPrefix + ":" + processor + ":external"
}

// FXPosition is the account holding the position of one conversion pair, e.g. fx:KWD-USD.
func (c Chart) FXPosition(sold, bought string) string {
	return c.FXPrefix + ":" + assetCode(sold) + "-" + assetCode(bought)
}

// assetCode is the asset without its scale: KWD/3 -> KWD.
func assetCode(asset string) string {
	if i := strings.IndexByte(asset, '/'); i > 0 {
		return asset[:i]
	}
	return asset
}

var (
	address = regexp.MustCompile(`^[a-zA-Z0-9_-]+(:[a-zA-Z0-9_-]+)*$`)
	asset   = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,16}(/\d{1,2})?$`)
	ident   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// ValidateAddress checks a is a ledger account address (segments of [a-zA-Z0-9_-] joined by ':').
func ValidateAddress(a string) error {
	if !address.MatchString(a) {
		return fmt.Errorf("%w: address %q", ErrInvalid, a)
	}
	return nil
}

// ValidateAsset checks a is a ledger asset (KWD/3, USD/2, GEM).
func ValidateAsset(a string) error {
	if !asset.MatchString(a) {
		return fmt.Errorf("%w: asset %q", ErrInvalid, a)
	}
	return nil
}

// ValidateID checks an id (wallet, authorization, processor, clearing) fits in one address segment.
func ValidateID(kind, id string) error {
	if !ident.MatchString(id) {
		return fmt.Errorf("%w: %s id %q must be one address segment ([a-zA-Z0-9_-])", ErrInvalid, kind, id)
	}
	return nil
}

// Money is an amount of one asset in its minor unit.
type Money struct {
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
}

// Validate checks the asset and that the amount is positive.
func (m Money) Validate() error {
	if err := ValidateAsset(m.Asset); err != nil {
		return err
	}
	if m.Amount <= 0 {
		return fmt.Errorf("%w: amount %d of %s must be positive", ErrInvalid, m.Amount, m.Asset)
	}
	return nil
}

func (m Money) String() string { return fmt.Sprintf("%s %d", m.Asset, m.Amount) }

// Errors of the operations, besides those of package ledger (which pass through).
var (
	// ErrInvalid: a request field is missing or malformed.
	ErrInvalid = errors.New("wallet: invalid request")
	// ErrHoldNotFound: no hold with the id on the wallet.
	ErrHoldNotFound = errors.New("wallet: hold not found")
	// ErrHoldClosed: the hold was already captured in full, released or expired.
	ErrHoldClosed = errors.New("wallet: hold closed")
	// ErrHoldExceeded: the capture is above what the hold has left.
	ErrHoldExceeded = errors.New("wallet: capture exceeds the hold")
)
