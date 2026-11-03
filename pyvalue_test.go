package qobuzdl

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, s string) any {
	t.Helper()
	v, err := decodeJSON(strings.NewReader(s))
	if err != nil {
		t.Fatalf("decodeJSON(%s): %v", s, err)
	}
	return v
}

func TestDecodeJSONKeepsOrderAndNumberTypes(t *testing.T) {
	v := mustDecode(t, `{"z": 96, "a": 96.0, "m": 1e3, "b": -0, "n": null, "l": [1, 2.5, true, "s"]}`)
	d, ok := v.(*pyDict)
	if !ok {
		t.Fatalf("got %T", v)
	}
	if got := strings.Join(d.keysInOrder(), ","); got != "z,a,m,b,n,l" {
		t.Errorf("keys %s", got)
	}
	want := map[string]any{"z": int64(96), "a": 96.0, "m": 1000.0, "b": int64(0), "n": nil}
	for k, w := range want {
		got := d.get(k, "missing")
		if fmt.Sprintf("%T %v", got, got) != fmt.Sprintf("%T %v", w, w) {
			t.Errorf("%s = %T %v, want %T %v", k, got, got, w, w)
		}
	}
	if got := pyRepr(d.get("l", nil)); got != "[1, 2.5, True, 's']" {
		t.Errorf("l = %s", got)
	}
}

func TestDecodeJSONRepeatedKeyKeepsFirstPlaceAndLastValue(t *testing.T) {
	// json.loads('{"a":1,"b":2,"a":3}') is {'a': 3, 'b': 2}.
	d := mustDecode(t, `{"a":1,"b":2,"a":3}`).(*pyDict)
	if got := pyRepr(d); got != "{'a': 3, 'b': 2}" {
		t.Errorf("got %s", got)
	}
}

func TestDecodeJSONOutOfRangeFloats(t *testing.T) {
	// Python's json gives inf, -inf and 0.0.
	for in, want := range map[string]float64{"1e400": math.Inf(1), "-1e400": math.Inf(-1), "1e-400": 0} {
		if got := mustDecode(t, in); got != want {
			t.Errorf("%s: got %v", in, got)
		}
	}
}

func TestDecodeJSONRejectsTrailingDataAndHugeInts(t *testing.T) {
	for _, in := range []string{`{} {}`, `[1`, `99999999999999999999`, `{"a" 1}`} {
		if _, err := decodeJSON(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestGetDefaultOnlyWhenMissing(t *testing.T) {
	d := mustDecode(t, `{"version": null, "title": "", "n": 0}`).(*pyDict)
	if got := d.get("version", "default"); got != nil {
		t.Errorf("version = %#v, want None", got)
	}
	if got := d.get("title", "default"); got != "" {
		t.Errorf("title = %#v", got)
	}
	if got := d.get("n", int64(5)); got != int64(0) {
		t.Errorf("n = %#v", got)
	}
	if got := d.get("missing", "default"); got != "default" {
		t.Errorf("missing = %#v", got)
	}
	if !d.has("version") || d.has("missing") {
		t.Error("has is wrong")
	}
	if _, err := d.item("missing"); !isPy(err, pyKeyError) || err.Error() != "KeyError: 'missing'" {
		t.Errorf("item(missing) error %v", err)
	}
	if v, err := d.item("version"); err != nil || v != nil {
		t.Errorf("item(version) = %v, %v", v, err)
	}
}

func TestDictOnNullIsAttributeError(t *testing.T) {
	d := mustDecode(t, `{"album": null, "label": "x", "artist": {"name": "A"}}`).(*pyDict)
	for _, k := range []string{"album", "label"} {
		if _, err := d.dict(k); !isPy(err, pyAttributeError) {
			t.Errorf("dict(%s) error %v, want AttributeError", k, err)
		}
	}
	got, err := d.dict("missing")
	if err != nil || len(got.keysInOrder()) != 0 {
		t.Errorf("dict(missing) = %v, %v", got, err)
	}
	a, err := d.dict("artist")
	if err != nil || a.get("name", nil) != "A" {
		t.Errorf("dict(artist) = %v, %v", a, err)
	}
	if _, err := d.dict("album"); err.Error() != "AttributeError: 'NoneType' object has no attribute 'get'" {
		t.Errorf("message %q", err)
	}
}

func TestListIteratesLikePython(t *testing.T) {
	d := mustDecode(t, `{"l": [1], "s": "ab", "d": {"k": 1}, "n": null, "i": 3}`).(*pyDict)
	cases := map[string]string{"l": "[1]", "s": "['a', 'b']", "d": "['k']", "missing": "[]"}
	for k, want := range cases {
		got, err := d.list(k)
		if err != nil || pyRepr(got) != want {
			t.Errorf("list(%s) = %v, %v; want %s", k, pyRepr(got), err, want)
		}
	}
	for _, k := range []string{"n", "i"} {
		if _, err := d.list(k); !isPy(err, pyTypeError) {
			t.Errorf("list(%s) error %v, want TypeError", k, err)
		}
	}
}

func TestSetKeepsPlaceOfExistingKey(t *testing.T) {
	d := newDict()
	d.set("a", int64(1))
	d.set("b", int64(2))
	d.set("a", int64(3))
	if got := pyRepr(d); got != "{'a': 3, 'b': 2}" {
		t.Errorf("got %s", got)
	}
}

func TestCloneIsDeep(t *testing.T) {
	d := mustDecode(t, `{"a": {"b": [1, {"c": 2}]}}`).(*pyDict)
	c := d.clone()
	inner, _ := c.dict("a")
	inner.set("b", "changed")
	inner.set("new", true)
	if got := pyRepr(d); got != "{'a': {'b': [1, {'c': 2}]}}" {
		t.Errorf("original changed: %s", got)
	}
}

func TestMarshalJSONKeepsOrderAndFloats(t *testing.T) {
	d := mustDecode(t, `{"z": 96.0, "a": 1, "u": "é\"<", "l": [null, false, 1e16, 0.1], "o": {}}`).(*pyDict)
	got, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":96.0,"a":1,"u":"é\"` + "\\u003c" + `","l":[null,false,1e+16,0.1],"o":{}}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	back := mustDecode(t, string(got))
	if pyRepr(back) != pyRepr(d) {
		t.Errorf("round trip %s != %s", pyRepr(back), pyRepr(d))
	}
	inf := newDict()
	inf.set("x", math.Inf(1))
	if _, err := json.Marshal(inf); err == nil {
		t.Error("inf marshalled")
	}
}

func TestPyStrGolden(t *testing.T) {
	for _, c := range loadGolden(t, "pystr") {
		if got := pyStr(c.Input); got != c.Output {
			t.Errorf("%s: str() = %q, want %q", c.Name, got, c.Output)
		}
	}
}

func TestPyReprOfStrings(t *testing.T) {
	// From Python 3.14: repr(s) and ascii(s).
	cases := []struct{ in, repr, ascii string }{
		{"it's", `"it's"`, `"it's"`},
		{`say "hi"`, `'say "hi"'`, `'say "hi"'`},
		{`both ' and "`, `'both \' and "'`, `'both \' and "'`},
		{"tab\there", `'tab\there'`, `'tab\there'`},
		{"nul\x00", `'nul\x00'`, `'nul\x00'`},
		{"del\x7f", `'del\x7f'`, `'del\x7f'`},
		{"é", `'é'`, `'\xe9'`},
		{"\u200b", "'\\u200b'", "'\\u200b'"},
		{"\U0001F3B5", "'\U0001F3B5'", `'\U0001f3b5'`},
		{"\u0085", `'\x85'`, `'\x85'`},
		{"\u3000", "'\\u3000'", "'\\u3000'"},
		{`a\b`, `'a\\b'`, `'a\\b'`},
	}
	for _, c := range cases {
		if got := pyRepr(c.in); got != c.repr {
			t.Errorf("repr(%q) = %s, want %s", c.in, got, c.repr)
		}
		if got := pyASCII(c.in); got != c.ascii {
			t.Errorf("ascii(%q) = %s, want %s", c.in, got, c.ascii)
		}
	}
}

func TestPyTruthy(t *testing.T) {
	falsy := []any{"", int64(0), 0.0, math.Copysign(0, -1), []any{}, newDict(), nil, false}
	for _, v := range falsy {
		if pyTruthy(v) {
			t.Errorf("%s is truthy", pyRepr(v))
		}
	}
	d := newDict()
	d.set("k", nil)
	truthy := []any{"0", " ", int64(-1), 0.1, math.NaN(), []any{nil}, d, true}
	for _, v := range truthy {
		if !pyTruthy(v) {
			t.Errorf("%s is falsy", pyRepr(v))
		}
	}
}

func TestPyIntFollowsPython(t *testing.T) {
	// From Python 3.14: int(v), or the exception it raises.
	cases := []struct {
		in   any
		want int64
		kind pyKind
	}{
		{"42", 42, 0},
		{" -7 ", -7, 0},
		{"+3", 3, 0},
		{"1_000", 1000, 0},
		{"_1", 0, pyValueError},
		{"1__0", 0, pyValueError},
		{"1_", 0, pyValueError},
		{"٣", 3, 0},
		{"", 0, pyValueError},
		{" ", 0, pyValueError},
		{"4.5", 0, pyValueError},
		{"x", 0, pyValueError},
		{"-0", 0, 0},
		{"9223372036854775807", math.MaxInt64, 0},
		{"-9223372036854775808", math.MinInt64, 0},
		{3.9, 3, 0},
		{-3.9, -3, 0},
		{true, 1, 0},
		{int64(96), 96, 0},
		{nil, 0, pyTypeError},
		{math.Inf(1), 0, pyOverflowError},
		{math.NaN(), 0, pyValueError},
	}
	for _, c := range cases {
		got, err := pyInt(c.in)
		if c.kind != 0 {
			if !isPy(err, c.kind) {
				t.Errorf("int(%s) = %d, %v; want %v", pyRepr(c.in), got, err, c.kind)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("int(%s) = %d, %v; want %d", pyRepr(c.in), got, err, c.want)
		}
	}
	// Python makes a big int; the port can't.
	if _, err := pyInt("9223372036854775808"); !isPy(err, pyOverflowError) {
		t.Errorf("int beyond int64: %v", err)
	}
}

func TestIsPyMatchesKindsThroughWrapping(t *testing.T) {
	err := fmt.Errorf("context: %w", pyErr(pyKeyError, "%s", pyRepr("title")))
	if !isPy(err, pyKeyError) || !isPy(err, pyValueError, pyKeyError) {
		t.Error("KeyError not matched")
	}
	if isPy(err, pyValueError) || isPy(errors.New("x"), pyKeyError) || isPy(nil, pyKeyError) {
		t.Error("matched the wrong kind")
	}
	if got := err.Error(); got != "context: KeyError: 'title'" {
		t.Errorf("message %q", got)
	}
}

func TestPyCompareLikePython(t *testing.T) {
	// From Python 3.14.
	big := int64(9007199254740993)
	eq := []struct {
		a, b any
		want bool
	}{
		{big, 9007199254740992.0, false},
		{true, int64(1), true},
		{int64(1), 1.0, true},
		{int64(math.MinInt64), -0x1p63, true},
		{math.NaN(), math.NaN(), false},
		{nil, nil, true},
		{"a", "a", true},
		{"1", int64(1), false},
		{[]any{int64(1)}, []any{1.0}, true},
	}
	for _, c := range eq {
		if got := pyEqual(c.a, c.b); got != c.want {
			t.Errorf("%s == %s: %v, want %v", pyRepr(c.a), pyRepr(c.b), got, c.want)
		}
	}
	d1, d2 := newDict(), newDict()
	d1.set("a", int64(1))
	d1.set("b", []any{1.0})
	d2.set("b", []any{true})
	d2.set("a", 1.0)
	if !pyEqual(d1, d2) {
		t.Error("dicts with equal values in another order are equal")
	}

	less := []struct {
		a, b any
		want bool
	}{
		{9007199254740992.0, big, true},
		{int64(math.MaxInt64), 9.223372036854775807e18, true},
		{int64(math.MinInt64), -0x1p63, false},
		{"é", "z", false},
		{[]any{int64(1), int64(2)}, []any{int64(1), int64(3)}, true},
		{[]any{int64(1)}, []any{int64(1), int64(0)}, true},
		{0.5, int64(1), true},
		{-0.5, int64(0), true},
	}
	for _, c := range less {
		if got, err := pyLess(c.a, c.b); err != nil || got != c.want {
			t.Errorf("%s < %s: %v, %v, want %v", pyRepr(c.a), pyRepr(c.b), got, err, c.want)
		}
	}
	for _, c := range [][2]any{{nil, int64(1)}, {"a", int64(1)}} {
		if _, err := pyLess(c[0], c[1]); !isPy(err, pyTypeError) {
			t.Errorf("%s < %s: %v, want TypeError", pyRepr(c[0]), pyRepr(c[1]), err)
		}
	}

	minMax := []struct {
		vals  []any
		isMax bool
		want  string
	}{
		{[]any{int64(24), 24.0}, true, "24"},
		{[]any{int64(1), true}, true, "1"},
		{[]any{96.0, int64(96)}, false, "96.0"},
		{[]any{int64(1), 2.5, int64(2)}, true, "2.5"},
	}
	for _, c := range minMax {
		if got, err := pyMinMax(c.vals, c.isMax); err != nil || pyRepr(got) != c.want {
			t.Errorf("minmax(%s, %v) = %v, %v, want %s", pyRepr(c.vals), c.isMax, got, err, c.want)
		}
	}
	if _, err := pyMinMax(nil, true); !isPy(err, pyValueError) {
		t.Errorf("max([]): %v", err)
	}
}

func TestPyGetItem(t *testing.T) {
	d := newDict()
	d.set("a", "x")
	if v, err := pyGetItem(d, "a"); v != "x" || err != nil {
		t.Errorf("d['a'] = %v, %v", v, err)
	}
	if _, err := pyGetItem(d, "b"); !isPy(err, pyKeyError) {
		t.Errorf("d['b']: %v", err)
	}
	for _, v := range []any{nil, "s", []any{}, int64(1)} {
		if _, err := pyGetItem(v, "a"); !isPy(err, pyTypeError) {
			t.Errorf("%s['a']: %v", pyRepr(v), err)
		}
	}
}
