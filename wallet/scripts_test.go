package wallet

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOverdraftJSON(t *testing.T) {
	limit := Money{Asset: "KWD/3", Amount: 1_000}
	// Names are what is written; names and the numbers of v0.1.0 are both read.
	for _, c := range []struct {
		in   string
		want Overdraft
		out  string
	}{
		{`{"kind":"refuse"}`, NoOverdraft, `"refuse"`},
		{`{"kind":"unbounded"}`, Unbounded, `"unbounded"`},
		{`{"kind":"up_to","limit":{"asset":"KWD/3","amount":1000}}`, UpTo(limit), `"up_to"`},
		{`{"kind":0}`, NoOverdraft, `"refuse"`},
		{`{"kind":1,"limit":{"asset":"KWD/3","amount":1000}}`, UpTo(limit), `"up_to"`},
		{`{"kind":2}`, Unbounded, `"unbounded"`},
		{`{}`, NoOverdraft, `"refuse"`},
	} {
		var got Overdraft
		if err := json.Unmarshal([]byte(c.in), &got); err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
		if err := got.validate("KWD/3"); err != nil {
			t.Errorf("%s: read a policy that does not validate: %v", c.in, err)
		}
		out, err := json.Marshal(got.Kind)
		if err != nil || string(out) != c.out {
			t.Errorf("%s: kind written as %s (%v), want %s", c.in, out, err, c.out)
		}
	}
	// An unknown kind is refused when it is read, never taken as some other policy.
	for _, bad := range []string{`{"kind":"lots"}`, `{"kind":""}`, `{"kind":7}`, `{"kind":-1}`, `{"kind":1.5}`, `{"kind":true}`, `{"kind":null}`} {
		got := Unbounded
		if err := json.Unmarshal([]byte(bad), &got); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := json.Marshal(OverdraftKind(7)); err == nil {
		t.Error("an unknown kind was written")
	}
}
