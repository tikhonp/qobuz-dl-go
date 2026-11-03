package qobuzdl

import (
	"testing"
)

func attrsOf(d *pyDict) pyAttrs {
	a := pyAttrs{}
	for _, k := range d.keysInOrder() {
		a[k] = d.get(k, nil)
	}
	return a
}

func errKind(err error) string {
	if pe, ok := err.(*pyError); ok {
		return pe.kind.String()
	}
	return "not a Python error"
}

func TestPyFormatGolden(t *testing.T) {
	for _, c := range loadGolden(t, "format") {
		in := c.Input.(*pyDict)
		tmpl := in.get("template", nil).(string)
		got, err := pyFormat(tmpl, attrsOf(in.get("attrs", nil).(*pyDict)))
		switch {
		case c.Error != "" && err == nil:
			t.Errorf("%s: got %q, want %s", c.Name, got, c.Error)
		case c.Error != "" && errKind(err) != c.Error:
			t.Errorf("%s: error %v, want %s", c.Name, err, c.Error)
		case c.Error == "" && err != nil:
			t.Errorf("%s: error %v, want %q", c.Name, err, c.Output)
		case c.Error == "" && got != c.Output:
			t.Errorf("%s: got %q, want %q", c.Name, got, c.Output)
		}
	}
}

func TestFormatSpecOnStringsAndNone(t *testing.T) {
	// From Python 3.14: format(v, spec).
	cases := []struct {
		v    any
		spec string
		want string
		kind pyKind
	}{
		{"1", "02", "10", 0},
		{nil, "02", "", pyTypeError},
		{true, "02", "01", 0},
		{1.5, "02", "1.5", 0},
		{int64(7), "02", "07", 0},
		{"1", ">5", "    1", 0},
		{true, ">5", "    1", 0},
		{"1", "x", "", pyValueError},
		{nil, "x", "", pyTypeError},
		{true, "x", "1", 0},
		{1.5, "d", "", pyValueError},
		{true, "s", "", pyValueError},
		{int64(7), "s", "", pyValueError},
		{"1", ".1", "1", 0},
		{true, ".1", "", pyValueError},
		{1.5, ".1", "2e+00", 0},
		{nil, "", "None", 0},
		{true, "", "True", 0},
		{1.5, "", "1.5", 0},
	}
	for _, c := range cases {
		got, err := pyFormatValue(c.v, c.spec)
		if c.kind != 0 {
			if !isPy(err, c.kind) {
				t.Errorf("format(%s, %q) = %q, %v; want %v", pyRepr(c.v), c.spec, got, err, c.kind)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("format(%s, %q) = %q, %v; want %q", pyRepr(c.v), c.spec, got, err, c.want)
		}
	}
	_, err := pyFormatValue(nil, "02")
	if err == nil || err.Error() != "TypeError: unsupported format string passed to NoneType.__format__" {
		t.Errorf("message %v", err)
	}
}

func TestFormatErrors(t *testing.T) {
	// From Python 3.14: "...".format(s="x", i=7).
	attrs := pyAttrs{"s": "x", "i": int64(7)}
	cases := []struct {
		tmpl string
		kind pyKind
	}{
		{"{", pyValueError},
		{"}", pyValueError},
		{"a{s", pyValueError},
		{"a}b", pyValueError},
		{"{s}}", pyValueError},
		{"{}", pyIndexError},
		{"{0}", pyIndexError},
		{"{s}{}", pyIndexError},
		{"{s:{}}", pyIndexError},
		{"{missing}{", pyKeyError},
		{"{s!z}", pyValueError},
		{"{missing!z}", pyKeyError},
		{"{s:{i:{i}}}", pyValueError},
		{"{99999999999999999999}", pyValueError},
		{"{s[0]x}", pyValueError},
		{"{s[}", pyValueError},
		{"{i.foo}", pyAttributeError},
		{"{s[-1]}", pyTypeError},
	}
	for _, c := range cases {
		got, err := pyFormat(c.tmpl, attrs)
		if !isPy(err, c.kind) {
			t.Errorf("%q: got %q, %v; want %v", c.tmpl, got, err, c.kind)
		}
	}
	if _, err := pyFormat("{x:999999999}", pyAttrs{"x": "a"}); !isPy(err, pyValueError) {
		t.Errorf("huge width: %v", err)
	}
}
