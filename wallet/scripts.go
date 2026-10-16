package wallet

import (
	"fmt"
	"strings"

	"github.com/diyaa-hamza/wallet-holds/ledger"
)

// Overdraft says how far a source account may go below zero in one posting.
type Overdraft struct {
	Kind  OverdraftKind
	Limit Money // for OverdraftUpTo: the lowest balance allowed, as a positive amount
}

// OverdraftKind is the policy of an Overdraft.
type OverdraftKind int

const (
	// OverdraftRefuse: the posting fails when the balance is short (the default).
	OverdraftRefuse OverdraftKind = iota
	// OverdraftUpTo: the balance may go down to -Limit.
	OverdraftUpTo
	// OverdraftUnbounded: no limit (external accounts).
	OverdraftUnbounded
)

// NoOverdraft, Unbounded and UpTo build the three policies.
var (
	NoOverdraft = Overdraft{Kind: OverdraftRefuse}
	Unbounded   = Overdraft{Kind: OverdraftUnbounded}
)

// UpTo allows the source to go down to -limit.
func UpTo(limit Money) Overdraft { return Overdraft{Kind: OverdraftUpTo, Limit: limit} }

func (o Overdraft) validate(asset string) error {
	switch o.Kind {
	case OverdraftRefuse, OverdraftUnbounded:
		return nil
	case OverdraftUpTo:
		if err := o.Limit.Validate(); err != nil {
			return fmt.Errorf("overdraft limit: %w", err)
		}
		if o.Limit.Asset != asset {
			return fmt.Errorf("%w: overdraft limit in %s, amount in %s", ErrInvalid, o.Limit.Asset, asset)
		}
		return nil
	}
	return fmt.Errorf("%w: overdraft kind %d", ErrInvalid, o.Kind)
}

// sourceClause renders "source = $src" with the overdraft policy; the limit variable is $limit.
func (o Overdraft) sourceClause(v string) string {
	switch o.Kind {
	case OverdraftUnbounded:
		return "source = " + v + " allowing unbounded overdraft"
	case OverdraftUpTo:
		return "source = " + v + " allowing overdraft up to $limit"
	}
	return "source = " + v
}

// script builds a Numscript program and its variables. Every operation renders exactly one of
// these; the text is in the repository so a reviewer can read what the ledger will run.
type script struct {
	vars  []string
	sends []string
	v     map[string]string
}

func newScript() *script { return &script{v: map[string]string{}} }

func (s *script) account(name, address string) *script {
	s.vars = append(s.vars, "account $"+name)
	s.v[name] = address
	return s
}

func (s *script) monetary(name string, m Money) *script {
	s.vars = append(s.vars, "monetary $"+name)
	s.v[name] = ledger.Monetary(m.Asset, m.Amount)
	return s
}

func (s *script) asset(name, a string) *script {
	s.vars = append(s.vars, "asset $"+name)
	s.v[name] = a
	return s
}

// accountMeta declares an account variable read from another account's metadata.
func (s *script) accountMeta(name, from, key string) *script {
	s.vars = append(s.vars, fmt.Sprintf("account $%s = meta($%s, %q)", name, from, key))
	return s
}

func (s *script) send(amount, sourceClause, dest string) *script {
	s.sends = append(s.sends, fmt.Sprintf("send %s (\n  %s\n  destination = %s\n)", amount, sourceClause, dest))
	return s
}

func (s *script) render() ledger.Script {
	var b strings.Builder
	b.WriteString("vars {\n")
	for _, v := range s.vars {
		b.WriteString("  " + v + "\n")
	}
	b.WriteString("}\n")
	for _, snd := range s.sends {
		b.WriteString(snd + "\n")
	}
	return ledger.Script{Plain: b.String(), Vars: s.v}
}

// The programs, one per operation.

func creditScript(source, dest string, m Money, od Overdraft) ledger.Script {
	s := newScript().account("source", source).account("dest", dest).monetary("amount", m)
	if od.Kind == OverdraftUpTo {
		s.monetary("limit", od.Limit)
	}
	return s.send("$amount", od.sourceClause("$source"), "$dest").render()
}

func moveScript(source, dest string, m Money) ledger.Script {
	return newScript().account("source", source).account("dest", dest).monetary("amount", m).
		send("$amount", "source = $source", "$dest").render()
}

func authorizeScript(main, hold string, m Money) ledger.Script {
	return newScript().account("main", main).account("hold", hold).monetary("amount", m).
		send("$amount", "source = $main", "$hold").render()
}

// captureScript moves the cleared amount from the hold to the destination named in the hold's
// metadata; with final, what is left goes back to the hold's void destination.
func captureScript(hold string, m Money, final bool) ledger.Script {
	s := newScript().account("hold", hold).monetary("amount", m).accountMeta("dest", "hold", metaDestination)
	if final {
		s.asset("asset", m.Asset).accountMeta("void", "hold", metaVoidDestination)
	}
	s.send("$amount", "source = $hold", "$dest")
	if final {
		s.send("[$asset *]", "source = $hold", "$void")
	}
	return s.render()
}

// releaseScript gives the hold's whole balance back to its void destination.
func releaseScript(hold, asset string) ledger.Script {
	return newScript().account("hold", hold).asset("asset", asset).accountMeta("void", "hold", metaVoidDestination).
		send("[$asset *]", "source = $hold", "$void").render()
}

func forceDebitScript(main, dest string, m Money, od Overdraft) ledger.Script {
	s := newScript().account("main", main).account("dest", dest).monetary("amount", m)
	if od.Kind == OverdraftUpTo {
		s.monetary("limit", od.Limit)
	}
	return s.send("$amount", od.sourceClause("$main"), "$dest").render()
}

func convertScript(main, fx string, sell, buy Money, margin int64, marginAccount string) ledger.Script {
	s := newScript().account("main", main).account("fx", fx).monetary("sell", sell).monetary("buy", buy)
	s.send("$sell", "source = $main", "$fx")
	if margin > 0 {
		s.account("margin_account", marginAccount).monetary("margin", Money{Asset: sell.Asset, Amount: margin})
		s.send("$margin", "source = $fx allowing unbounded overdraft", "$margin_account")
	}
	s.send("$buy", "source = $fx allowing unbounded overdraft", "$main")
	return s.render()
}
