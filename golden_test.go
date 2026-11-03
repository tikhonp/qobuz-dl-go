// Test helpers for the golden files testdata/gen/gen.py writes from qobuz-dl
// 2.5.9. They port no Python file.

package qobuzdl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goldenGenerator = "qobuz-dl 2.5.9"

// goldenCase is one case of a golden file. Exactly one of Output and Error is
// set; Error is the Python exception class name, such as "KeyError".
type goldenCase struct {
	Name      string
	Input     any
	Output    any
	HasOutput bool
	Error     string
}

// goldenDict is a JSON object with its keys in file order, which is Python's
// insertion order.
type goldenDict struct {
	keys []string
	vals map[string]any
}

func (d *goldenDict) Keys() []string { return d.keys }

func (d *goldenDict) Get(key string) (any, bool) {
	v, ok := d.vals[key]
	return v, ok
}

// loadGolden reads testdata/golden/<name>.json. Values come back as nil, bool,
// int64, float64, string, []any or *goldenDict.
func loadGolden(tb testing.TB, name string) []goldenCase {
	tb.Helper()
	path := filepath.Join("testdata", "golden", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("golden %s: %v", name, err)
	}
	cases, err := parseGolden(data)
	if err != nil {
		tb.Fatalf("golden %s: %v", path, err)
	}
	return cases
}

func parseGolden(data []byte) ([]goldenCase, error) {
	raw, err := decodeGoldenJSON(data)
	if err != nil {
		return nil, err
	}
	doc, ok := raw.(*goldenDict)
	if !ok {
		return nil, errors.New("top level is not an object")
	}
	if g, _ := doc.Get("generator"); g != goldenGenerator {
		return nil, fmt.Errorf("generator %v, want %q", g, goldenGenerator)
	}
	list, _ := doc.Get("cases")
	items, ok := list.([]any)
	if !ok {
		return nil, errors.New("cases is not a list")
	}
	cases := make([]goldenCase, 0, len(items))
	for i, item := range items {
		obj, ok := item.(*goldenDict)
		if !ok {
			return nil, fmt.Errorf("case %d is not an object", i)
		}
		c, err := goldenCaseFrom(obj)
		if err != nil {
			return nil, fmt.Errorf("case %d: %w", i, err)
		}
		cases = append(cases, c)
	}
	return cases, nil
}

func goldenCaseFrom(obj *goldenDict) (goldenCase, error) {
	var c goldenCase
	for _, k := range obj.keys {
		v := obj.vals[k]
		var err error
		switch k {
		case "name":
			c.Name, err = goldenString(k, v)
		case "error":
			c.Error, err = goldenString(k, v)
		case "input":
			c.Input, err = untagGolden(v)
		case "output":
			c.Output, err = untagGolden(v)
			c.HasOutput = true
		default:
			err = fmt.Errorf("unknown key %q", k)
		}
		if err != nil {
			return c, err
		}
	}
	if c.Name == "" {
		return c, errors.New("no name")
	}
	if c.HasOutput == (c.Error != "") {
		return c, fmt.Errorf("%s: needs exactly one of output and error", c.Name)
	}
	return c, nil
}

func goldenString(key string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s is %T, not a string", key, v)
	}
	return s, nil
}

// jsonNull stands for a JSON null until untagGolden refuses it: gen.py tags
// every None, so a plain null is a generator bug.
type jsonNull struct{}

// decodeGoldenJSON parses JSON keeping object key order, numbers as
// json.Number and nulls as jsonNull.
func decodeGoldenJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeGoldenToken(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the top-level value")
	}
	return v, nil
}

func decodeGoldenToken(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			d := &goldenDict{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				if _, dup := d.vals[k]; dup {
					return nil, fmt.Errorf("duplicate key %q", k)
				}
				v, err := decodeGoldenToken(dec)
				if err != nil {
					return nil, err
				}
				d.keys = append(d.keys, k)
				d.vals[k] = v
			}
			_, err := dec.Token()
			return d, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeGoldenToken(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case nil:
		return jsonNull{}, nil
	default:
		return t, nil // string, bool, json.Number
	}
}

// untagGolden turns gen.py's tagged values into Go values and refuses untagged
// numbers and nulls, whose Python type is lost.
func untagGolden(v any) (any, error) {
	switch t := v.(type) {
	case string, bool:
		return t, nil
	case json.Number:
		return nil, fmt.Errorf("untagged number %s", t)
	case jsonNull:
		return nil, errors.New("untagged null")
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			var err error
			if out[i], err = untagGolden(x); err != nil {
				return nil, err
			}
		}
		return out, nil
	case *goldenDict:
		if _, tagged := t.vals["py"]; tagged {
			return untagPython(t)
		}
		out := &goldenDict{keys: t.keys, vals: make(map[string]any, len(t.vals))}
		for _, k := range t.keys {
			x, err := untagGolden(t.vals[k])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out.vals[k] = x
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected %T", v)
}

func untagPython(d *goldenDict) (any, error) {
	kind, _ := d.vals["py"].(string)
	val, hasVal := d.vals["v"]
	wantKeys := 1
	if hasVal {
		wantKeys = 2
	}
	if len(d.keys) != wantKeys {
		return nil, fmt.Errorf("tagged value with keys %v", d.keys)
	}
	switch kind {
	case "none":
		if hasVal {
			return nil, errors.New("none with a value")
		}
		return nil, nil
	case "int":
		n, ok := val.(json.Number)
		if !ok {
			return nil, fmt.Errorf("int value %v", val)
		}
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("int %s: %w", n, err)
		}
		return i, nil
	case "float":
		switch f := val.(type) {
		case json.Number:
			return f.Float64()
		case string:
			switch f {
			case "inf":
				return math.Inf(1), nil
			case "-inf":
				return math.Inf(-1), nil
			case "nan":
				return math.NaN(), nil
			}
		}
		return nil, fmt.Errorf("float value %v", val)
	}
	return nil, fmt.Errorf("unknown tag %q", kind)
}

func TestLoadGoldenKeepsPythonTypes(t *testing.T) {
	inputs := map[string]any{}
	for _, c := range loadGolden(t, "pystr") {
		inputs[c.Name] = c.Input
	}
	checks := []struct {
		name string
		want any
	}{
		{"int 96", int64(96)},
		{"float 96.0", float64(96)},
		{"float 44.1", 44.1},
		{"float -0.0", math.Copysign(0, -1)},
		{"float inf", math.Inf(1)},
		{"NoneType None", nil},
		{"bool True", true},
		{"int 9223372036854775807", int64(math.MaxInt64)},
	}
	for _, c := range checks {
		got, ok := inputs[c.name]
		if !ok {
			t.Errorf("pystr has no case %q", c.name)
			continue
		}
		if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", c.want) || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %T %v, want %T %v", c.name, got, got, c.want, c.want)
		}
	}
	if got := inputs["float -0.0"].(float64); !math.Signbit(got) {
		t.Errorf("float -0.0 lost its sign")
	}
	if got := inputs["float nan"].(float64); !math.IsNaN(got) {
		t.Errorf("float nan: got %v", got)
	}
}

func TestGoldenObjectsKeepKeyOrder(t *testing.T) {
	cases, err := parseGolden([]byte(`{"generator": "qobuz-dl 2.5.9", "cases": [
		{"name": "x", "input": {"z": "1", "a": {"py": "int", "v": 2}, "m": [{"py": "none"}]}, "output": "ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := cases[0].Input.(*goldenDict)
	if got := strings.Join(d.Keys(), ","); got != "z,a,m" {
		t.Errorf("keys %s, want z,a,m", got)
	}
	if v, _ := d.Get("a"); v != int64(2) {
		t.Errorf("a = %#v", v)
	}
	if v, _ := d.Get("m"); len(v.([]any)) != 1 || v.([]any)[0] != nil {
		t.Errorf("m = %#v", v)
	}
}

func TestGoldenRefusesUntaggedValues(t *testing.T) {
	for _, in := range []string{
		`{"generator": "qobuz-dl 2.5.9", "cases": [{"name": "x", "input": 96, "output": "96"}]}`,
		`{"generator": "qobuz-dl 2.5.9", "cases": [{"name": "x", "input": null, "output": "None"}]}`,
		`{"generator": "qobuz-dl 2.5.9", "cases": [{"name": "x", "input": "a", "output": "b", "error": "KeyError"}]}`,
		`{"generator": "qobuz-dl 2.5.9", "cases": [{"name": "x", "input": {"py": "int", "v": 1.5}, "output": "b"}]}`,
		`{"generator": "qobuz-dl 2.5.0", "cases": []}`,
	} {
		if _, err := parseGolden([]byte(in)); err == nil {
			t.Errorf("parsed without error: %s", in)
		}
	}
}

func TestEveryGoldenLoads(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden files")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		t.Run(name, func(t *testing.T) {
			cases := loadGolden(t, name)
			if len(cases) == 0 {
				t.Error("no cases")
			}
			seen := map[string]bool{}
			for _, c := range cases {
				if seen[c.Name] {
					t.Errorf("duplicate case %q", c.Name)
				}
				seen[c.Name] = true
			}
		})
	}
}
