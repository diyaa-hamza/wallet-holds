package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger"
)

// Service runs the operations on one ledger with one chart.
type Service struct {
	ledger ledger.Client
	chart  Chart
	now    func() time.Time
}

// Option configures New.
type Option func(*Service)

// WithClock replaces time.Now (hold expiry, closed_at).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds a service; the chart is validated.
func New(client ledger.Client, chart Chart, opts ...Option) (*Service, error) {
	if client == nil {
		return nil, errors.New("wallet: a ledger client is required")
	}
	if err := chart.Validate(); err != nil {
		return nil, err
	}
	s := &Service{ledger: client, chart: chart, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Chart is the service's chart.
func (s *Service) Chart() Chart { return s.chart }

// Ledger is the client the service posts to.
func (s *Service) Ledger() ledger.Client { return s.ledger }

// Result is what an operation booked.
type Result struct {
	TxID      int64            `json:"txId"`
	Reference string           `json:"reference"`
	Replayed  bool             `json:"replayed"`
	Postings  []ledger.Posting `json:"postings"`
}

// Metadata keys written by the service.
const (
	MetaKind            = "kind"
	MetaWallet          = "wallet"
	MetaProcessor       = "processor"
	MetaAuthID          = "auth_id"
	MetaClearingID      = "clearing_id"
	MetaFileID          = "file_id"
	MetaStatus          = "status"
	MetaAsset           = "asset"
	MetaAmount          = "amount"
	MetaExpiresAt       = "expires_at"
	MetaClosedAt        = "closed_at"
	metaDestination     = "destination"
	metaVoidDestination = "void_destination"
)

// Kinds of transactions (metadata "kind") and of hold statuses.
const (
	KindCredit     = "credit"
	KindDebit      = "debit"
	KindTransfer   = "transfer"
	KindAuthorize  = "authorize"
	KindCapture    = "capture"
	KindRelease    = "release"
	KindExpire     = "expire"
	KindForceDebit = "force_debit"
	KindSettle     = "settle"
	KindConvert    = "convert"
	KindHold       = "hold"

	StatusOpen     = "open"
	StatusCaptured = "captured"
	StatusReleased = "released"
	StatusExpired  = "expired"
)

// Reference formats of the processor operations.
func authReference(processor, authID string) string { return "auth:" + processor + ":" + authID }
func captureReference(processor, clearingID string) string {
	return "capture:" + processor + ":" + clearingID
}
func releaseReference(processor, authID string) string { return "release:" + processor + ":" + authID }
func expireReference(processor, authID string) string  { return "expire:" + processor + ":" + authID }
func settleReference(processor, fileID string) string {
	return "issuer_settlement:" + processor + ":" + fileID
}

// post runs one transaction. The ledger's idempotency covers the whole request, and a retry
// may differ in what does not matter (a hold's expiry computed again, a label): on a conflict
// the existing transaction is read back and, when it moved the same money (every posting of
// expect is in it), the retry is answered as a replay; otherwise it is ledger.ErrConflict.
func (s *Service) post(ctx context.Context, ref string, at time.Time, sc ledger.Script, meta map[string]string, accountMeta map[string]map[string]string, expect []ledger.Posting) (Result, error) {
	tx, replayed, err := s.ledger.Post(ctx, ledger.Transaction{
		Reference: ref, Timestamp: at, Script: sc, Metadata: meta, AccountMetadata: accountMeta,
	})
	if errors.Is(err, ledger.ErrConflict) {
		existing, lookupErr := s.ledger.ByReference(ctx, ref)
		if lookupErr != nil {
			return Result{}, lookupErr
		}
		if existing != nil && movesAll(existing.Postings, expect) {
			return Result{TxID: existing.ID, Reference: existing.Reference, Replayed: true, Postings: existing.Postings}, nil
		}
		return Result{}, err
	}
	if err != nil {
		return Result{}, err
	}
	return Result{TxID: tx.ID, Reference: tx.Reference, Replayed: replayed, Postings: tx.Postings}, nil
}

// movesAll reports whether every posting of expect is among got.
func movesAll(got, expect []ledger.Posting) bool {
	if len(expect) == 0 {
		return false
	}
	for _, e := range expect {
		found := false
		for _, g := range got {
			if g == e {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func posting(source, dest string, m Money) []ledger.Posting {
	return []ledger.Posting{{Source: source, Destination: dest, Amount: m.Amount, Asset: m.Asset}}
}

func meta(kind string, extra map[string]string, pairs ...string) map[string]string {
	m := map[string]string{}
	for k, v := range extra {
		m[k] = v
	}
	m[MetaKind] = kind
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			m[pairs[i]] = pairs[i+1]
		}
	}
	return m
}

// CreditRequest funds a wallet from a source account: a top-up from the PSP's collection
// account, a refund from the processor's settlement, a transfer in from any account.
type CreditRequest struct {
	WalletID string
	Source   string
	Money    Money
	// Reference is the business id (topup:{payment_id}); required.
	Reference string
	// SourceOverdraft says whether the source may go negative (an external account does).
	SourceOverdraft Overdraft
	Metadata        map[string]string
	At              time.Time
}

// Credit books the request.
func (s *Service) Credit(ctx context.Context, r CreditRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateAddress(r.Source), r.Money.Validate(),
		requireRef(r.Reference), r.SourceOverdraft.validate(r.Money.Asset)); err != nil {
		return Result{}, err
	}
	return s.post(ctx, r.Reference, r.At, creditScript(r.Source, s.chart.Main(r.WalletID), r.Money, r.SourceOverdraft),
		meta(KindCredit, r.Metadata, MetaWallet, r.WalletID), nil, posting(r.Source, s.chart.Main(r.WalletID), r.Money))
}

// DebitRequest spends from a wallet to a destination account; the balance must cover it.
type DebitRequest struct {
	WalletID    string
	Destination string
	Money       Money
	Reference   string
	Metadata    map[string]string
	At          time.Time
}

// Debit books the request; ledger.ErrInsufficientFunds when the wallet is short.
func (s *Service) Debit(ctx context.Context, r DebitRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateAddress(r.Destination), r.Money.Validate(), requireRef(r.Reference)); err != nil {
		return Result{}, err
	}
	return s.post(ctx, r.Reference, r.At, moveScript(s.chart.Main(r.WalletID), r.Destination, r.Money),
		meta(KindDebit, r.Metadata, MetaWallet, r.WalletID), nil, posting(s.chart.Main(r.WalletID), r.Destination, r.Money))
}

// TransferRequest moves money between two wallets in one transaction.
type TransferRequest struct {
	From, To  string
	Money     Money
	Reference string
	Metadata  map[string]string
	At        time.Time
}

// Transfer books the request; ledger.ErrInsufficientFunds when the source wallet is short.
func (s *Service) Transfer(ctx context.Context, r TransferRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.From), ValidateID("wallet", r.To), r.Money.Validate(), requireRef(r.Reference)); err != nil {
		return Result{}, err
	}
	if r.From == r.To {
		return Result{}, fmt.Errorf("%w: transfer to the same wallet", ErrInvalid)
	}
	m := meta(KindTransfer, r.Metadata, MetaWallet, r.From)
	m["to"] = r.To
	return s.post(ctx, r.Reference, r.At, moveScript(s.chart.Main(r.From), s.chart.Main(r.To), r.Money), m, nil,
		posting(s.chart.Main(r.From), s.chart.Main(r.To), r.Money))
}

// AuthorizeRequest holds an amount for a processor's authorization. The decision is the
// ledger's balance check: a short wallet is ledger.ErrInsufficientFunds, to be answered as a
// decline.
type AuthorizeRequest struct {
	WalletID  string
	Processor string
	AuthID    string
	Money     Money
	// Destination is where captures go; the processor's settlement account by default.
	Destination string
	// ExpiresAt, or ExpiresIn from now, bounds the hold; the chart's expiry by default.
	ExpiresAt time.Time
	ExpiresIn time.Duration
	Metadata  map[string]string
	At        time.Time
}

// Authorize books the hold (reference auth:{processor}:{auth_id}).
func (s *Service) Authorize(ctx context.Context, r AuthorizeRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateID("processor", r.Processor), ValidateID("authorization", r.AuthID), r.Money.Validate()); err != nil {
		return Result{}, err
	}
	dest := r.Destination
	if dest == "" {
		dest = s.chart.Settlement(r.Processor)
	} else if err := ValidateAddress(dest); err != nil {
		return Result{}, err
	}
	now := s.now()
	expires := r.ExpiresAt
	switch {
	case !expires.IsZero():
	case r.ExpiresIn > 0:
		expires = now.Add(r.ExpiresIn)
	default:
		expires = now.Add(s.chart.HoldExpiry)
	}
	main, hold := s.chart.Main(r.WalletID), s.chart.Hold(r.WalletID, r.AuthID)
	holdMeta := map[string]string{
		MetaKind: KindHold, MetaStatus: StatusOpen, MetaWallet: r.WalletID, MetaProcessor: r.Processor, MetaAuthID: r.AuthID,
		MetaAsset: r.Money.Asset, MetaAmount: fmt.Sprint(r.Money.Amount), metaDestination: dest, metaVoidDestination: main,
		MetaExpiresAt: expires.UTC().Format(time.RFC3339),
	}
	for k, v := range r.Metadata {
		if _, reserved := holdMeta[k]; !reserved {
			holdMeta[k] = v
		}
	}
	return s.post(ctx, authReference(r.Processor, r.AuthID), r.At, authorizeScript(main, hold, r.Money),
		meta(KindAuthorize, r.Metadata, MetaWallet, r.WalletID, MetaProcessor, r.Processor, MetaAuthID, r.AuthID),
		map[string]map[string]string{hold: holdMeta}, posting(main, hold, r.Money))
}

// CaptureRequest clears part or all of a hold to its destination.
type CaptureRequest struct {
	WalletID   string
	Processor  string
	AuthID     string
	ClearingID string
	Money      Money
	// Final: nothing more will be captured; what is left of the hold goes back to the wallet.
	Final    bool
	Metadata map[string]string
	At       time.Time
}

// Capture books the clearing (reference capture:{processor}:{clearing_id}). A capture above
// what the hold has left is ErrHoldExceeded: the caller decides whether to ForceDebit the
// difference. A capture of exactly what is left closes the hold.
func (s *Service) Capture(ctx context.Context, r CaptureRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateID("processor", r.Processor), ValidateID("authorization", r.AuthID),
		ValidateID("clearing", r.ClearingID), r.Money.Validate()); err != nil {
		return Result{}, err
	}
	ref := captureReference(r.Processor, r.ClearingID)
	h, err := s.Hold(ctx, r.WalletID, r.AuthID)
	if err != nil {
		return Result{}, err
	}
	if h.Status != StatusOpen {
		return s.replayOr(ctx, ref, ErrHoldClosed)
	}
	if r.Money.Asset != h.Asset {
		return Result{}, fmt.Errorf("%w: capture in %s, hold in %s", ErrInvalid, r.Money.Asset, h.Asset)
	}
	if r.Money.Amount > h.Remaining {
		return s.replayOr(ctx, ref, fmt.Errorf("%w: %d above the %d left", ErrHoldExceeded, r.Money.Amount, h.Remaining))
	}
	final := r.Final && r.Money.Amount < h.Remaining
	closes := r.Final || r.Money.Amount == h.Remaining
	var accountMeta map[string]map[string]string
	if closes {
		accountMeta = map[string]map[string]string{s.chart.Hold(r.WalletID, r.AuthID): {
			MetaStatus: StatusCaptured, MetaClosedAt: s.now().UTC().Format(time.RFC3339),
		}}
	}
	return s.post(ctx, ref, r.At, captureScript(s.chart.Hold(r.WalletID, r.AuthID), r.Money, final),
		meta(KindCapture, r.Metadata, MetaWallet, r.WalletID, MetaProcessor, r.Processor, MetaAuthID, r.AuthID, MetaClearingID, r.ClearingID),
		accountMeta, posting(s.chart.Hold(r.WalletID, r.AuthID), h.Destination, r.Money))
}

// ReleaseRequest gives a hold back to the wallet (the processor voided it).
type ReleaseRequest struct {
	WalletID  string
	Processor string
	AuthID    string
	Metadata  map[string]string
	At        time.Time
}

// Release books the void (reference release:{processor}:{auth_id}); a hold already closed is
// ErrHoldClosed unless this release is the one that closed it (replayed).
func (s *Service) Release(ctx context.Context, r ReleaseRequest) (Result, error) {
	return s.release(ctx, r, KindRelease, StatusReleased, releaseReference(r.Processor, r.AuthID))
}

// Expire is Release by the sweeper (reference expire:{processor}:{auth_id}, status expired).
func (s *Service) Expire(ctx context.Context, r ReleaseRequest) (Result, error) {
	return s.release(ctx, r, KindExpire, StatusExpired, expireReference(r.Processor, r.AuthID))
}

func (s *Service) release(ctx context.Context, r ReleaseRequest, kind, status, ref string) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateID("processor", r.Processor), ValidateID("authorization", r.AuthID)); err != nil {
		return Result{}, err
	}
	h, err := s.Hold(ctx, r.WalletID, r.AuthID)
	if err != nil {
		return Result{}, err
	}
	if h.Status != StatusOpen {
		return s.replayOr(ctx, ref, ErrHoldClosed)
	}
	hold := s.chart.Hold(r.WalletID, r.AuthID)
	closed := map[string]string{MetaStatus: status, MetaClosedAt: s.now().UTC().Format(time.RFC3339)}
	if h.Remaining == 0 {
		// Captured in full without a final flag: nothing to move, just close it.
		if err := s.ledger.SetAccountMetadata(ctx, hold, closed); err != nil {
			return Result{}, err
		}
		return Result{Reference: ref}, nil
	}
	return s.post(ctx, ref, r.At, releaseScript(hold, h.Asset),
		meta(kind, r.Metadata, MetaWallet, r.WalletID, MetaProcessor, r.Processor, MetaAuthID, r.AuthID),
		map[string]map[string]string{hold: closed}, posting(hold, s.chart.Main(r.WalletID), Money{Asset: h.Asset, Amount: h.Remaining}))
}

// replayOr answers a retry of an operation that already happened (the reference exists) and
// returns fallback otherwise.
func (s *Service) replayOr(ctx context.Context, ref string, fallback error) (Result, error) {
	existing, err := s.ledger.ByReference(ctx, ref)
	if err != nil {
		return Result{}, err
	}
	if existing != nil {
		return Result{TxID: existing.ID, Reference: existing.Reference, Replayed: true, Postings: existing.Postings}, nil
	}
	return Result{}, fallback
}

// ForceDebitRequest clears an amount that no hold covers (a clearing without authorization,
// or the part above the hold). Overdraft is the policy of the caller: refuse, up to a limit,
// or unbounded (the wallet then owes).
type ForceDebitRequest struct {
	WalletID   string
	Processor  string
	ClearingID string
	Money      Money
	Overdraft  Overdraft
	// Destination is the processor's settlement account by default.
	Destination string
	Metadata    map[string]string
	At          time.Time
}

// ForceDebit books the clearing (reference capture:{processor}:{clearing_id}, the same space as
// Capture: a clearing is one or the other).
func (s *Service) ForceDebit(ctx context.Context, r ForceDebitRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), ValidateID("processor", r.Processor), ValidateID("clearing", r.ClearingID),
		r.Money.Validate(), r.Overdraft.validate(r.Money.Asset)); err != nil {
		return Result{}, err
	}
	dest := r.Destination
	if dest == "" {
		dest = s.chart.Settlement(r.Processor)
	} else if err := ValidateAddress(dest); err != nil {
		return Result{}, err
	}
	return s.post(ctx, captureReference(r.Processor, r.ClearingID), r.At, forceDebitScript(s.chart.Main(r.WalletID), dest, r.Money, r.Overdraft),
		meta(KindForceDebit, r.Metadata, MetaWallet, r.WalletID, MetaProcessor, r.Processor, MetaClearingID, r.ClearingID), nil,
		posting(s.chart.Main(r.WalletID), dest, r.Money))
}

// SettleRequest closes captured amounts against the processor's settlement file.
type SettleRequest struct {
	Processor string
	FileID    string
	Money     Money
	Metadata  map[string]string
	At        time.Time
}

// Settle books the file (reference issuer_settlement:{processor}:{file_id}); a file above what
// was captured is ledger.ErrInsufficientFunds, a difference to explain, not to book.
func (s *Service) Settle(ctx context.Context, r SettleRequest) (Result, error) {
	if err := errors.Join(ValidateID("processor", r.Processor), ValidateID("file", r.FileID), r.Money.Validate()); err != nil {
		return Result{}, err
	}
	return s.post(ctx, settleReference(r.Processor, r.FileID), r.At, moveScript(s.chart.Settlement(r.Processor), s.chart.External(r.Processor), r.Money),
		meta(KindSettle, r.Metadata, MetaProcessor, r.Processor, MetaFileID, r.FileID), nil,
		posting(s.chart.Settlement(r.Processor), s.chart.External(r.Processor), r.Money))
}

// ConvertRequest exchanges one asset for another inside the wallet at the caller's rate: Sell
// leaves the wallet, Buy enters it, Margin (in the sold asset) goes to the chart's margin
// account; the position account of the pair carries the rest.
type ConvertRequest struct {
	WalletID  string
	Sell      Money
	Buy       Money
	Margin    int64
	Reference string
	Metadata  map[string]string
	At        time.Time
}

// Convert books the exchange; ledger.ErrInsufficientFunds when the wallet is short of Sell.
func (s *Service) Convert(ctx context.Context, r ConvertRequest) (Result, error) {
	if err := errors.Join(ValidateID("wallet", r.WalletID), r.Sell.Validate(), r.Buy.Validate(), requireRef(r.Reference)); err != nil {
		return Result{}, err
	}
	if r.Sell.Asset == r.Buy.Asset {
		return Result{}, fmt.Errorf("%w: convert %s to itself", ErrInvalid, r.Sell.Asset)
	}
	if r.Margin < 0 || r.Margin >= r.Sell.Amount {
		return Result{}, fmt.Errorf("%w: margin %d must be within the sold amount %d", ErrInvalid, r.Margin, r.Sell.Amount)
	}
	m := meta(KindConvert, r.Metadata, MetaWallet, r.WalletID)
	m["sell"] = r.Sell.String()
	m["buy"] = r.Buy.String()
	fxAcc := s.chart.FXPosition(r.Sell.Asset, r.Buy.Asset)
	return s.post(ctx, r.Reference, r.At, convertScript(s.chart.Main(r.WalletID), fxAcc, r.Sell, r.Buy, r.Margin, s.chart.FXMarginAccount), m, nil,
		append(posting(s.chart.Main(r.WalletID), fxAcc, r.Sell), posting(fxAcc, s.chart.Main(r.WalletID), r.Buy)...))
}

func requireRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("%w: a reference is required", ErrInvalid)
	}
	return nil
}
