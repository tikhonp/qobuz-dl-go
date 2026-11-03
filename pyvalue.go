// Python value semantics for the data qobuz-dl 2.5.9 handles: JSON values,
// dict.get, str(), repr(), truthiness, int() and exception kinds. It ports no
// Python file; the Python code it stands in for is CPython 3.14's.

package qobuzdl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A Python value decoded from JSON is one of nil (None), bool, int64,
// float64, string, []any or *pyDict.

// pyDict is a Python dict with string keys. Python 3.7+ dicts keep insertion
// order, which decides tag order and m3u8 order downstream.
type pyDict struct {
	keys []string
	vals map[string]any
}

func newDict() *pyDict {
	return &pyDict{vals: map[string]any{}}
}

// get is dict.get: def comes back only when the key is missing, not when its
// value is None.
func (d *pyDict) get(key string, def any) any {
	if v, ok := d.vals[key]; ok {
		return v
	}
	return def
}

// item is d[key].
func (d *pyDict) item(key string) (any, error) {
	if v, ok := d.vals[key]; ok {
		return v, nil
	}
	return nil, pyErr(pyKeyError, "%s", pyRepr(key))
}

// set is d[key] = v. A key that exists keeps its place, as in Python.
func (d *pyDict) set(key string, v any) {
	if _, ok := d.vals[key]; !ok {
		d.keys = append(d.keys, key)
	}
	d.vals[key] = v
}

// pop is d.pop(key, def).
func (d *pyDict) pop(key string, def any) any {
	v, ok := d.vals[key]
	if !ok {
		return def
	}
	delete(d.vals, key)
	d.keys = slices.DeleteFunc(d.keys, func(k string) bool { return k == key })
	return v
}

func (d *pyDict) has(key string) bool {
	_, ok := d.vals[key]
	return ok
}

func (d *pyDict) keysInOrder() []string {
	return slices.Clone(d.keys)
}

// dict is d.get(key, {}) followed by a dict method such as .get. None or any
// other non-dict raises AttributeError there.
func (d *pyDict) dict(key string) (*pyDict, error) {
	v, ok := d.vals[key]
	if !ok {
		return newDict(), nil
	}
	return asDict(v)
}

// list is d.get(key, []) iterated. A string iterates its characters and a dict
// its keys, as in Python; None, numbers and bools raise TypeError.
func (d *pyDict) list(key string) ([]any, error) {
	v, ok := d.vals[key]
	if !ok {
		return []any{}, nil
	}
	return pyIter(v)
}

// clone is a deep copy.
func (d *pyDict) clone() *pyDict {
	c := &pyDict{keys: slices.Clone(d.keys), vals: make(map[string]any, len(d.vals))}
	for k, v := range d.vals {
		c.vals[k] = cloneValue(v)
	}
	return c
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case *pyDict:
		return t.clone()
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneValue(x)
		}
		return out
	}
	return v
}

// MarshalJSON writes the keys in order. Floats keep a decimal point or an
// exponent so that they decode as floats again.
func (d *pyDict) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return fmt.Errorf("json: unsupported float %v", t)
		}
		b.WriteString(pyStr(t))
	case string:
		s, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(s)
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, x); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case *pyDict:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			s, err := json.Marshal(k)
			if err != nil {
				return err
			}
			b.Write(s)
			b.WriteByte(':')
			if err := writeJSON(b, t.vals[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("json: not a Python value: %T", v)
	}
	return nil
}

// asDict is v used as a dict. Upstream calls .get on such values, so a
// non-dict is an AttributeError.
func asDict(v any) (*pyDict, error) {
	if d, ok := v.(*pyDict); ok {
		return d, nil
	}
	return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'get'", pyTypeName(v))
}

// pyIter is iter(v) collected into a list.
func pyIter(v any) ([]any, error) {
	switch t := v.(type) {
	case []any:
		return t, nil
	case *pyDict:
		out := make([]any, len(t.keys))
		for i, k := range t.keys {
			out[i] = k
		}
		return out, nil
	case string:
		out := make([]any, 0, utf8.RuneCountInString(t))
		for _, r := range t {
			out = append(out, string(r))
		}
		return out, nil
	}
	return nil, pyErr(pyTypeError, "'%s' object is not iterable", pyTypeName(v))
}

// decodeJSON is json.load. Numbers without '.', 'e' or 'E' are ints, the
// rest floats, as in Python's json; objects keep their key order, and a
// repeated key keeps its first place with the last value.
func decodeJSON(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("extra data after the JSON value")
		}
		return nil, fmt.Errorf("json: %w", err)
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			d := newDict()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("json: %w", err)
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("json: object key %v", kt)
				}
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				d.set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("json: %w", err)
			}
			return d, nil
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("json: %w", err)
			}
			return list, nil
		}
		return nil, fmt.Errorf("json: unexpected %v", t)
	case json.Number:
		return decodeNumber(string(t))
	}
	return tok, nil // nil, bool or string
}

func decodeNumber(s string) (any, error) {
	if !strings.ContainsAny(s, ".eE") {
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// Python would make a big int; nothing in the Qobuz API needs one.
			return nil, fmt.Errorf("json: int %s out of range", s)
		}
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, fmt.Errorf("json: %w", err)
	}
	// Out of range parses as ±inf or ±0, which is what Python's float() gives.
	return f, nil
}

// pyTypeName is type(v).__name__.
func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case *pyDict:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// pyStr is str(v).
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return floatRepr(t)
	case string:
		return t
	}
	return pyRepr(v)
}

// pyRepr is repr(v).
func pyRepr(v any) string {
	return reprValue(v, false)
}

// pyASCII is ascii(v): repr with every non-ASCII character escaped.
func pyASCII(v any) string {
	return reprValue(v, true)
}

func reprValue(v any, ascii bool) string {
	switch t := v.(type) {
	case string:
		return reprString(t, ascii)
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(reprValue(x, ascii))
		}
		b.WriteByte(']')
		return b.String()
	case *pyDict:
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(reprString(k, ascii))
			b.WriteString(": ")
			b.WriteString(reprValue(t.vals[k], ascii))
		}
		b.WriteByte('}')
		return b.String()
	case nil, bool, int64, float64:
		return pyStr(t)
	}
	return fmt.Sprintf("<%T>", v)
}

// reprString follows unicode_repr in CPython's unicodeobject.c: single quotes
// unless the string has a single quote and no double quote.
func reprString(s string, ascii bool) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case ascii || !pyIsPrintable(r):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyTruthy is bool(v).
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case *pyDict:
		return len(t.keys) > 0
	}
	return true
}

// pyInt is int(v). Ints beyond int64 are an OverflowError, where Python
// would make a big int.
func pyInt(v any) (int64, error) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case int64:
		return t, nil
	case float64:
		switch {
		case math.IsNaN(t):
			return 0, pyErr(pyValueError, "cannot convert float NaN to integer")
		case math.IsInf(t, 0):
			return 0, pyErr(pyOverflowError, "cannot convert float infinity to integer")
		}
		f := math.Trunc(t)
		if f < -(1<<63) || f >= 1<<63 {
			return 0, pyErr(pyOverflowError, "int %s does not fit in 64 bits", pyStr(f))
		}
		return int64(f), nil
	case string:
		return parsePyInt(t)
	}
	return 0, pyErr(pyTypeError, "int() argument must be a string, a bytes-like object or a real number, not '%s'", pyTypeName(v))
}

// parsePyInt is int(s) for a str: Python whitespace around, an optional sign,
// Unicode decimal digits, single underscores between digits.
func parsePyInt(s string) (int64, error) {
	invalid := pyErr(pyValueError, "invalid literal for int() with base 10: %s", pyRepr(s))
	t := []rune(pyStrip(s, ""))
	neg := false
	if len(t) > 0 && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if len(t) == 0 {
		return 0, invalid
	}
	var n uint64
	prevDigit := false
	for _, r := range t {
		if r == '_' {
			if !prevDigit {
				return 0, invalid
			}
			prevDigit = false
			continue
		}
		d, ok := pyDecimalValue(r)
		if !ok {
			return 0, invalid
		}
		if n > (math.MaxUint64-uint64(d))/10 {
			return 0, pyErr(pyOverflowError, "int %s does not fit in 64 bits", pyRepr(s))
		}
		n = n*10 + uint64(d)
		prevDigit = true
	}
	if !prevDigit {
		return 0, invalid
	}
	switch {
	case neg && n == 1<<63:
		return math.MinInt64, nil
	case n < 1<<63 && neg:
		return -int64(n), nil
	case n < 1<<63:
		return int64(n), nil
	}
	return 0, pyErr(pyOverflowError, "int %s does not fit in 64 bits", pyRepr(s))
}

// pyKind is a Python exception class. Each Python except clause becomes an
// isPy check for its kinds.
type pyKind int

const (
	pyKeyError pyKind = iota + 1
	pyTypeError
	pyValueError
	pyAttributeError
	pyIndexError
	pyOverflowError
)

func (k pyKind) String() string {
	switch k {
	case pyKeyError:
		return "KeyError"
	case pyTypeError:
		return "TypeError"
	case pyValueError:
		return "ValueError"
	case pyAttributeError:
		return "AttributeError"
	case pyIndexError:
		return "IndexError"
	case pyOverflowError:
		return "OverflowError"
	}
	return fmt.Sprintf("pyKind(%d)", int(k))
}

// pyError is a Python exception returned as an error. Error prints it as
// Python's traceback does on its last line, such as "KeyError: 'title'".
type pyError struct {
	kind pyKind
	msg  string
}

func (e *pyError) Error() string {
	return e.kind.String() + ": " + e.msg
}

func pyErr(kind pyKind, format string, args ...any) error {
	return &pyError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// isPy reports whether err is a Python exception of one of the kinds.
func isPy(err error, kinds ...pyKind) bool {
	var pe *pyError
	if !errors.As(err, &pe) {
		return false
	}
	return slices.Contains(kinds, pe.kind)
}

// pyGetItem is v[key] with a string key: KeyError on a dict without it,
// TypeError on anything but a dict.
func pyGetItem(v any, key string) (any, error) {
	switch t := v.(type) {
	case *pyDict:
		return t.item(key)
	case nil, bool, int64, float64:
		return nil, pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(v))
	case string, []any:
		return nil, pyErr(pyTypeError, "%s indices must be integers or slices, not str", pyTypeName(v))
	}
	return nil, pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(v))
}

// pyNumber returns a bool, int or float as an int64 or a float64; bools are
// ints in Python.
func pyNumber(v any) (i int64, f float64, isFloat, ok bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, 0, false, true
		}
		return 0, 0, false, true
	case int64:
		return t, 0, false, true
	case float64:
		return 0, t, true, true
	}
	return 0, 0, false, false
}

// cmpIntFloat compares an int with a float exactly, as Python does, without
// rounding the int to a float. ok is false when f is NaN.
func cmpIntFloat(i int64, f float64) (c int, ok bool) {
	switch {
	case math.IsNaN(f):
		return 0, false
	case f >= 0x1p63:
		return -1, true
	case f < -0x1p63:
		return 1, true
	}
	whole := math.Trunc(f)
	t := int64(whole)
	switch {
	case i < t:
		return -1, true
	case i > t:
		return 1, true
	case f > whole:
		return -1, true
	case f < whole:
		return 1, true
	}
	return 0, true
}

// cmpNumbers compares two numbers; ok is false when either is NaN.
func cmpNumbers(a, b any) (c int, ok bool) {
	ai, af, aFloat, _ := pyNumber(a)
	bi, bf, bFloat, _ := pyNumber(b)
	switch {
	case !aFloat && !bFloat:
		return cmpOrdered(ai, bi), true
	case aFloat && bFloat:
		if math.IsNaN(af) || math.IsNaN(bf) {
			return 0, false
		}
		return cmpOrdered(af, bf), true
	case aFloat:
		c, ok := cmpIntFloat(bi, af)
		return -c, ok
	}
	return cmpIntFloat(ai, bf)
}

func cmpOrdered[T int64 | float64 | string](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// pyEqual is a == b.
func pyEqual(a, b any) bool {
	if _, _, _, ok := pyNumber(a); ok {
		if _, _, _, ok := pyNumber(b); ok {
			c, ok := cmpNumbers(a, b)
			return ok && c == 0
		}
		return false
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !pyEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case *pyDict:
		y, ok := b.(*pyDict)
		if !ok || len(x.keys) != len(y.keys) {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.vals[k]
			if !ok || !pyEqual(x.vals[k], yv) {
				return false
			}
		}
		return true
	}
	return false
}

// pyLess is a < b for numbers, strings and lists; other pairs are a TypeError.
func pyLess(a, b any) (bool, error) {
	_, _, _, aNum := pyNumber(a)
	_, _, _, bNum := pyNumber(b)
	switch {
	case aNum && bNum:
		c, ok := cmpNumbers(a, b)
		return ok && c < 0, nil
	case !aNum && !bNum:
		switch x := a.(type) {
		case string:
			if y, ok := b.(string); ok {
				// Byte order of UTF-8 is code point order.
				return x < y, nil
			}
		case []any:
			if y, ok := b.([]any); ok {
				for i := 0; i < len(x) && i < len(y); i++ {
					if !pyEqual(x[i], y[i]) {
						return pyLess(x[i], y[i])
					}
				}
				return len(x) < len(y), nil
			}
		}
	}
	return false, pyErr(pyTypeError, "'<' not supported between instances of '%s' and '%s'", pyTypeName(a), pyTypeName(b))
}

// pyMinMax is min(vals) or max(vals): the first value no other beats.
func pyMinMax(vals []any, isMax bool) (any, error) {
	name := "min"
	if isMax {
		name = "max"
	}
	if len(vals) == 0 {
		return nil, pyErr(pyValueError, "%s() iterable argument is empty", name)
	}
	best := vals[0]
	for _, v := range vals[1:] {
		var beats bool
		var err error
		if isMax {
			beats, err = pyLess(best, v)
		} else {
			beats, err = pyLess(v, best)
		}
		if err != nil {
			return nil, err
		}
		if beats {
			best = v
		}
	}
	return best, nil
}
