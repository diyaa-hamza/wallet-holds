package wallet

import (
	"strings"
	"testing"
)

func TestChartAddresses(t *testing.T) {
	c := DefaultChart()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		c.Main("c1"):                   "wallet:c1:main",
		c.Hold("c1", "a9"):             "wallet:c1:hold:a9",
		c.HoldPattern("c1"):            "wallet:c1:hold:",
		c.HoldPattern(""):              "wallet::hold:",
		c.WalletPattern("c1"):          "wallet:c1:...",
		c.Settlement("sim"):            "issuer:sim:settlement",
		c.External("sim"):              "issuer:sim:external",
		c.FXPosition("KWD/3", "USD/2"): "fx:KWD-USD",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	for _, bad := range []Chart{
		{WalletPrefix: "wal let", MainSegment: "main", HoldSegment: "hold", IssuerPrefix: "i", FXPrefix: "fx", FXMarginAccount: "r:m", HoldExpiry: 1},
		{WalletPrefix: "w", MainSegment: "main", HoldSegment: "hold", IssuerPrefix: "i", FXPrefix: "fx", FXMarginAccount: "bad address", HoldExpiry: 1},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("chart %+v validated", bad)
		}
	}
}

func TestValidators(t *testing.T) {
	for _, ok := range []string{"KWD/3", "USD/2", "GEM", "BTC/8"} {
		if err := ValidateAsset(ok); err != nil {
			t.Errorf("asset %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"kwd/3", "KWD/", "", "KWD/300", "K WD"} {
		if err := ValidateAsset(bad); err == nil {
			t.Errorf("asset %q accepted", bad)
		}
	}
	for _, bad := range []string{"", "a:b", "a b", "a/b"} {
		if err := ValidateID("x", bad); err == nil {
			t.Errorf("id %q accepted", bad)
		}
	}
	if err := ValidateAddress("wallet:01926d6e-0000-7000-8000-0000000000aa:main"); err != nil {
		t.Error(err)
	}
}

func TestScripts(t *testing.T) {
	m := Money{Asset: "KWD/3", Amount: 1500}
	s := captureScript("wallet:c1:hold:a1", m, true)
	for _, want := range []string{
		"account $hold", "monetary $amount", "asset $asset",
		`account $dest = meta($hold, "destination")`, `account $void = meta($hold, "void_destination")`,
		"send $amount (\n  source = $hold\n  destination = $dest\n)", "send [$asset *] (\n  source = $hold\n  destination = $void\n)",
	} {
		if !strings.Contains(s.Plain, want) {
			t.Errorf("final capture script lacks %q:\n%s", want, s.Plain)
		}
	}
	if s.Vars["amount"] != "KWD/3 1500" || s.Vars["asset"] != "KWD/3" || s.Vars["hold"] != "wallet:c1:hold:a1" {
		t.Errorf("vars = %v", s.Vars)
	}
	if p := captureScript("h", m, false).Plain; strings.Contains(p, "void") {
		t.Errorf("partial capture script touches the void destination:\n%s", p)
	}
	up := forceDebitScript("wallet:c1:main", "issuer:sim:settlement", m, UpTo(Money{Asset: "KWD/3", Amount: 500}))
	if !strings.Contains(up.Plain, "allowing overdraft up to $limit") || up.Vars["limit"] != "KWD/3 500" {
		t.Errorf("up-to script = %+v", up)
	}
	if un := forceDebitScript("a", "b", m, Unbounded).Plain; !strings.Contains(un, "allowing unbounded overdraft") {
		t.Errorf("unbounded script = %s", un)
	}
	if no := forceDebitScript("a", "b", m, NoOverdraft).Plain; strings.Contains(no, "overdraft") {
		t.Errorf("refuse script allows overdraft: %s", no)
	}
	cv := convertScript("wallet:c1:main", "fx:KWD-USD", m, Money{Asset: "USD/2", Amount: 480}, 30, "revenue:fx_margin")
	if strings.Count(cv.Plain, "send ") != 3 || cv.Vars["margin"] != "KWD/3 30" || cv.Vars["buy"] != "USD/2 480" {
		t.Errorf("convert script = %+v", cv)
	}
	if cv0 := convertScript("a", "b", m, Money{Asset: "USD/2", Amount: 480}, 0, "r"); strings.Count(cv0.Plain, "send ") != 2 {
		t.Errorf("convert without margin = %s", cv0.Plain)
	}
}
