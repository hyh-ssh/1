package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestNewMoney(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "integer", in: "42", want: "42"},
		{name: "decimal", in: "1.23", want: "1.23"},
		{name: "trailing zero trimmed", in: "10.50", want: "10.5"},
		{name: "leading plus", in: "+3.5", want: "3.5"},
		{name: "negative", in: "-0.75", want: "-0.75"},
		{name: "zero", in: "0", want: "0"},
		{name: "exponent notation", in: "1e3", want: "1000"},
		{name: "very large", in: "99999999999999999999.99", want: "99999999999999999999.99"},
		{name: "empty", in: "", wantErr: true},
		{name: "not a number", in: "abc", wantErr: true},
		{name: "double dot", in: "1.2.3", wantErr: true},
		{name: "trailing junk", in: "12x", wantErr: true},
		{name: "whitespace", in: " 1 ", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewMoney(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewMoney(%q) = %s, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewMoney(%q) unexpected error: %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Fatalf("NewMoney(%q) = %s, want %s", tc.in, got.String(), tc.want)
			}
		})
	}
}

func TestMustMoney(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "integer", in: "7", want: "7"},
		{name: "decimal", in: "0.000001", want: "0.000001"},
		{name: "negative", in: "-12.34", want: "-12.34"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MustMoney(tc.in).String(); got != tc.want {
				t.Fatalf("MustMoney(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}

	t.Run("panics on invalid input", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("MustMoney(\"nope\") did not panic")
			}
		}()
		_ = MustMoney("nope")
	})
}

func TestNewMoneyIntAndZeroMoney(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{name: "positive", in: 42, want: "42"},
		{name: "zero", in: 0, want: "0"},
		{name: "negative", in: -7, want: "-7"},
		{name: "max int64", in: 9223372036854775807, want: "9223372036854775807"},
		{name: "min int64", in: -9223372036854775808, want: "-9223372036854775808"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewMoneyInt(tc.in).String(); got != tc.want {
				t.Fatalf("NewMoneyInt(%d) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}

	if !ZeroMoney.IsZero() {
		t.Fatalf("ZeroMoney = %s, want zero", ZeroMoney)
	}
	if got := ZeroMoney.String(); got != "0" {
		t.Fatalf("ZeroMoney.String() = %s, want 0", got)
	}
	if !MustMoney("0.000").IsZero() {
		t.Fatal("MustMoney(\"0.000\") should be zero")
	}
	// ZeroMoney is a value copy of decimal.Zero; arithmetic on it must not
	// perturb the package variable.
	_ = ZeroMoney.Add(NewMoneyInt(5))
	if !ZeroMoney.IsZero() {
		t.Fatalf("ZeroMoney mutated to %s", ZeroMoney)
	}
}

func TestMoneyArithmeticIsExact(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		op   string
		want string
	}{
		{name: "add avoids float error", a: "0.1", b: "0.2", op: "add", want: "0.3"},
		{name: "add negative", a: "5", b: "-7.5", op: "add", want: "-2.5"},
		{name: "sub", a: "0.3", b: "0.1", op: "sub", want: "0.2"},
		{name: "sub negative result", a: "1", b: "2", op: "sub", want: "-1"},
		{name: "mul", a: "1.5", b: "2", op: "mul", want: "3"},
		{name: "mul rounding boundary", a: "2.675", b: "1", op: "mul", want: "2.675"},
		{name: "div exact", a: "1", b: "8", op: "div", want: "0.125"},
		{name: "div repeating", a: "1", b: "3", op: "div", want: "0.3333333333333333"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := MustMoney(tc.a), MustMoney(tc.b)
			var got Money
			switch tc.op {
			case "add":
				got = a.Add(b)
			case "sub":
				got = a.Sub(b)
			case "mul":
				got = a.Mul(b)
			case "div":
				got = a.Div(b)
			default:
				t.Fatalf("unknown op %q", tc.op)
			}
			if got.String() != tc.want {
				t.Fatalf("%s %s %s = %s, want %s", tc.a, tc.op, tc.b, got.String(), tc.want)
			}
		})
	}

	t.Run("very large product stays exact", func(t *testing.T) {
		got := MustMoney("123456789012345678901234567890.12").Mul(MustMoney("1000000000000"))
		if want := "123456789012345678901234567890120000000000"; got.String() != want {
			t.Fatalf("large mul = %s, want %s", got.String(), want)
		}
	})

	t.Run("div by zero panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("division by zero did not panic")
			}
		}()
		_ = MustMoney("1").Div(ZeroMoney)
	})
}

func TestMoneyCompareAndFormat(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		cmp  int
		eq   bool
		gt   bool
		lt   bool
	}{
		{name: "equal values different scale", a: "2", b: "2.0", cmp: 0, eq: true},
		{name: "greater", a: "2.5", b: "2.49", cmp: 1, gt: true},
		{name: "less", a: "2.49", b: "2.5", cmp: -1, lt: true},
		{name: "negative greater", a: "-1", b: "-2", cmp: 1, gt: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := MustMoney(tc.a), MustMoney(tc.b)
			if got := a.Cmp(b); got != tc.cmp {
				t.Fatalf("Cmp = %d, want %d", got, tc.cmp)
			}
			if got := a.Equals(b); got != tc.eq {
				t.Fatalf("Equals = %v, want %v", got, tc.eq)
			}
			if got := a.GreaterThan(b); got != tc.gt {
				t.Fatalf("GreaterThan = %v, want %v", got, tc.gt)
			}
			if got := a.LessThan(b); got != tc.lt {
				t.Fatalf("LessThan = %v, want %v", got, tc.lt)
			}
		})
	}

	t.Run("rounding half away from zero", func(t *testing.T) {
		cases := []struct {
			in     string
			places int32
			want   string
		}{
			{in: "1.005", places: 2, want: "1.01"},
			{in: "2.675", places: 2, want: "2.68"},
			{in: "-1.005", places: 2, want: "-1.01"},
			{in: "2.5", places: 0, want: "3"},
			{in: "-2.5", places: 0, want: "-3"},
		}
		for _, tc := range cases {
			if got := MustMoney(tc.in).Round(tc.places).String(); got != tc.want {
				t.Errorf("Round(%s, %d) = %s, want %s", tc.in, tc.places, got, tc.want)
			}
		}
	})

	t.Run("format helpers", func(t *testing.T) {
		m := MustMoney("1.5")
		if got := m.StringFixed(2); got != "1.50" {
			t.Fatalf("StringFixed(2) = %s, want 1.50", got)
		}
		if got := MustMoney("10.50").String(); got != "10.5" {
			t.Fatalf("String() = %s, want 10.5", got)
		}
		if got := m.Exponent(); got != -1 {
			t.Fatalf("Exponent() = %d, want -1", got)
		}
		if got := MustMoney("150").Exponent(); got != 0 {
			t.Fatalf("Exponent() of 150 = %d, want 0", got)
		}
		if got := m.Coefficient().String(); got != "15" {
			t.Fatalf("Coefficient() = %s, want 15", got)
		}
		if got := MustMoney("0.1").InexactFloat64(); got != 0.1 {
			t.Fatalf("InexactFloat64() = %v, want 0.1", got)
		}
		if got := MustMoney("-3.5").Abs().String(); got != "3.5" {
			t.Fatalf("Abs() = %s, want 3.5", got)
		}
		if got := MustMoney("5").Neg().String(); got != "-5" {
			t.Fatalf("Neg() = %s, want -5", got)
		}
	})

	t.Run("json round trip on Money", func(t *testing.T) {
		// decimal marshals as a quoted string, preserving precision.
		raw, err := json.Marshal(MustMoney("0.10"))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(raw) != `"0.1"` {
			t.Fatalf("marshalled Money = %s, want \"0.1\"", raw)
		}
		var back Money
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !back.Equals(MustMoney("0.1")) {
			t.Fatalf("round tripped Money = %s, want 0.1", back)
		}
	})
}

func TestNowIsUTC(t *testing.T) {
	before := time.Now().UTC()
	got := Now()
	after := time.Now().UTC()

	if got.Location() != time.UTC {
		t.Fatalf("Now() location = %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestNewUsageAndSetGet(t *testing.T) {
	t.Run("NewUsage has empty non-nil map", func(t *testing.T) {
		u := NewUsage()
		if u.Meters == nil {
			t.Fatal("NewUsage().Meters is nil, want empty map")
		}
		if len(u.Meters) != 0 {
			t.Fatalf("NewUsage() meters = %v, want empty", u.Meters)
		}
		if u.Estimated {
			t.Fatal("NewUsage() should not be estimated")
		}
	})

	t.Run("Set then Get", func(t *testing.T) {
		u := NewUsage()
		u.Set("input", 11)
		u.Set("input", 12) // overwrite
		u.Set("output", 0)
		if got := u.Get("input"); got != 12 {
			t.Fatalf("Get(input) = %d, want 12", got)
		}
		if got := u.Get("output"); got != 0 {
			t.Fatalf("Get(output) = %d, want 0", got)
		}
	})

	t.Run("Get unknown key and nil map return zero", func(t *testing.T) {
		if got := NewUsage().Get("missing"); got != 0 {
			t.Fatalf("Get(missing) = %d, want 0", got)
		}
		var nilUsage Usage
		if got := nilUsage.Get("input"); got != 0 {
			t.Fatalf("nil map Get(input) = %d, want 0", got)
		}
	})
}

func TestUsageAdd(t *testing.T) {
	tests := []struct {
		name      string
		a, b      Usage
		want      map[string]int64
		estimated bool
	}{
		{
			name: "disjoint meters",
			a:    Usage{Meters: map[string]int64{"input": 1}},
			b:    Usage{Meters: map[string]int64{"output": 2}},
			want: map[string]int64{"input": 1, "output": 2},
		},
		{
			name: "overlapping meters",
			a:    Usage{Meters: map[string]int64{"input": 1, "output": 2}},
			b:    Usage{Meters: map[string]int64{"input": 3}},
			want: map[string]int64{"input": 4, "output": 2},
		},
		{
			name: "nil maps",
			a:    Usage{},
			b:    Usage{},
			want: map[string]int64{},
		},
		{
			name: "negative meter values",
			a:    Usage{Meters: map[string]int64{"input": 5}},
			b:    Usage{Meters: map[string]int64{"input": -8}},
			want: map[string]int64{"input": -3},
		},
		{
			name:      "estimated propagates from left",
			a:         Usage{Meters: map[string]int64{"input": 1}, Estimated: true},
			b:         Usage{Meters: map[string]int64{"input": 1}},
			want:      map[string]int64{"input": 2},
			estimated: true,
		},
		{
			name:      "estimated propagates from right",
			a:         Usage{Meters: map[string]int64{"input": 1}},
			b:         Usage{Meters: map[string]int64{"input": 1}, Estimated: true},
			want:      map[string]int64{"input": 2},
			estimated: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.a.Add(tc.b)
			if !reflect.DeepEqual(got.Meters, tc.want) {
				t.Fatalf("Add meters = %v, want %v", got.Meters, tc.want)
			}
			if got.Estimated != tc.estimated {
				t.Fatalf("Add estimated = %v, want %v", got.Estimated, tc.estimated)
			}
		})
	}

	t.Run("does not mutate operands", func(t *testing.T) {
		a := Usage{Meters: map[string]int64{"input": 1}}
		b := Usage{Meters: map[string]int64{"input": 2}}
		_ = a.Add(b)
		if !reflect.DeepEqual(a.Meters, map[string]int64{"input": 1}) {
			t.Fatalf("left operand mutated: %v", a.Meters)
		}
		if !reflect.DeepEqual(b.Meters, map[string]int64{"input": 2}) {
			t.Fatalf("right operand mutated: %v", b.Meters)
		}
	})
}

func TestUsageTotalTokens(t *testing.T) {
	tests := []struct {
		name  string
		meter map[string]int64
		want  int64
	}{
		{name: "empty", meter: nil, want: 0},
		{name: "provider total wins over subset sum", meter: map[string]int64{"input": 10, "output": 5, "total": 15}, want: 15},
		{name: "sums countable meters", meter: map[string]int64{"input": 10, "output": 5}, want: 15},
		{name: "includes cache and reasoning when no total", meter: map[string]int64{"input": 10, "output": 5, "cache_input": 4, "cache_creation": 3, "reasoning": 2}, want: 24},
		{name: "ignores non-token meters", meter: map[string]int64{"input": 10, "image": 100, "audio": 50, "requests": 1}, want: 10},
		{name: "zero total falls back to sum", meter: map[string]int64{"input": 7, "total": 0}, want: 7},
		{name: "negative total falls back to sum", meter: map[string]int64{"input": 7, "total": -1}, want: 7},
		{name: "negative countable meter", meter: map[string]int64{"input": 10, "output": -3}, want: 7},
		{name: "only unknown meters", meter: map[string]int64{"foo": 9, "bar": 1}, want: 0},
		{name: "large values", meter: map[string]int64{"input": 9223372036854775806, "output": 1}, want: 9223372036854775807},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := Usage{Meters: tc.meter}
			if got := u.TotalTokens(); got != tc.want {
				t.Fatalf("TotalTokens(%v) = %d, want %d", tc.meter, got, tc.want)
			}
		})
	}
}

func TestUsageMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   Usage
		want string
	}{
		{
			name: "nil meters is unreported",
			in:   Usage{},
			want: `{"meters":null,"estimated":false,"source":"unreported"}`,
		},
		{
			name: "empty initialized meters is unreported",
			in:   NewUsage(),
			want: `{"meters":{},"estimated":false,"source":"unreported"}`,
		},
		{
			name: "meters present",
			in:   Usage{Meters: map[string]int64{"input": 3}},
			want: `{"meters":{"input":3},"estimated":false,"source":"upstream"}`,
		},
		{
			name: "estimated overrides source",
			in:   Usage{Meters: map[string]int64{"input": 3}, Estimated: true},
			want: `{"meters":{"input":3},"estimated":true,"source":"text_bytes_v1"}`,
		},
		{
			name: "estimated with no meters still estimated",
			in:   Usage{Estimated: true},
			want: `{"meters":null,"estimated":true,"source":"text_bytes_v1"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("MarshalJSON = %s, want %s", raw, tc.want)
			}
		})
	}

	t.Run("multiple meters serialize to the documented wire form", func(t *testing.T) {
		u := Usage{Meters: map[string]int64{"input": 1, "output": 2, "cache_read": 3}}
		raw, err := json.Marshal(u)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		const want = `{"meters":{"cache_read":3,"input":1,"output":2},"estimated":false,"source":"upstream"}`
		if string(raw) != want {
			t.Fatalf("MarshalJSON = %s, want %s", raw, want)
		}
	})
}

func TestUsageUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      map[string]int64
		estimated bool
	}{
		{name: "meters and estimated", in: `{"meters":{"input":5},"estimated":true}`, want: map[string]int64{"input": 5}, estimated: true},
		{name: "missing meters becomes empty map", in: `{"estimated":false}`, want: map[string]int64{}},
		{name: "explicit null meters becomes empty map", in: `{"meters":null}`, want: map[string]int64{}},
		{name: "null document becomes empty map", in: `null`, want: map[string]int64{}},
		{name: "extra source field is ignored", in: `{"meters":{"input":1},"estimated":false,"source":"text_bytes_v1"}`, want: map[string]int64{"input": 1}},
		{name: "empty object", in: `{}`, want: map[string]int64{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var u Usage
			if err := json.Unmarshal([]byte(tc.in), &u); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if !reflect.DeepEqual(u.Meters, tc.want) {
				t.Fatalf("meters = %v, want %v", u.Meters, tc.want)
			}
			if u.Meters == nil {
				t.Fatal("unmarshalled meters must be non-nil")
			}
			if u.Estimated != tc.estimated {
				t.Fatalf("estimated = %v, want %v", u.Estimated, tc.estimated)
			}
		})
	}

	t.Run("invalid json returns error", func(t *testing.T) {
		var u Usage
		if err := json.Unmarshal([]byte(`{`), &u); err == nil {
			t.Fatal("expected error for malformed json")
		}
	})

	t.Run("wrong type returns error", func(t *testing.T) {
		var u Usage
		if err := json.Unmarshal([]byte(`{"meters":{"input":"not-a-number"}}`), &u); err == nil {
			t.Fatal("expected error for non-numeric meter")
		}
	})

	t.Run("round trip through request json", func(t *testing.T) {
		orig := Usage{Meters: map[string]int64{"input": 10, "total": 15}, Estimated: true}
		raw, err := json.Marshal(struct {
			Usage Usage `json:"usage"`
		}{Usage: orig})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got struct {
			Usage Usage `json:"usage"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(got.Usage.Meters, orig.Meters) || got.Usage.Estimated != orig.Estimated {
			t.Fatalf("round trip = %+v, want %+v", got.Usage, orig)
		}
	})
}
