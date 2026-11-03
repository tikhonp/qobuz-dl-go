// str.format and format(value, spec) for the values qobuz-dl 2.5.9 puts in
// its templates. It ports no Python file; it follows CPython 3.14.10's
// Objects/stringlib/unicode_format.h, Python/formatter_unicode.c and
// Python/pystrtod.c, whose function names the comments below use.

package qobuzdl

import (
	"math"
	"strconv"
	"strings"
)

// pyAttrs are the keyword arguments of str.format(**attrs).
type pyAttrs map[string]any

// maxFormatWidth caps widths, and precisions of floats. Python would try to
// build the string and fail with MemoryError; a template typo must not take
// the process down.
const maxFormatWidth = 1 << 24

// pyFormat is tmpl.format(**attrs). There are no positional arguments, so
// {} and {0} raise IndexError.
func pyFormat(tmpl string, attrs pyAttrs) (string, error) {
	return buildString([]rune(tmpl), attrs, 2)
}

// buildString renders a template; depth stops nesting in specs after one
// level, as CPython's recursion_depth of 2 does.
func buildString(s []rune, attrs pyAttrs, depth int) (string, error) {
	if depth <= 0 {
		return "", pyErr(pyValueError, "Max string recursion exceeded")
	}
	var b strings.Builder
	pos := 0
	for pos < len(s) {
		// MarkupIterator_next: literal text up to the first brace.
		start := pos
		var c rune
		markup := false
		for pos < len(s) {
			c = s[pos]
			pos++
			if c == '{' || c == '}' {
				markup = true
				break
			}
		}
		atEnd := pos >= len(s)
		n := pos - start
		if c == '}' && (atEnd || s[pos] != '}') {
			return "", pyErr(pyValueError, "Single '}' encountered in format string")
		}
		if atEnd && c == '{' {
			return "", pyErr(pyValueError, "Single '{' encountered in format string")
		}
		if !atEnd {
			if c == s[pos] {
				pos++
				markup = false
			} else {
				n--
			}
		}
		b.WriteString(string(s[start : start+n]))
		if !markup {
			continue
		}
		f, next, err := parseField(s, pos)
		if err != nil {
			return "", err
		}
		pos = next
		out, err := renderMarkup(f, attrs, depth)
		if err != nil {
			return "", err
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

type field struct {
	name       []rune
	conversion rune
	spec       []rune
	expand     bool // the spec has fields of its own
}

// parseField reads a field after its '{' and returns the position after its
// closing '}'.
func parseField(s []rune, pos int) (field, int, error) {
	var f field
	nameStart := pos
	var c rune
loop:
	for pos < len(s) {
		c = s[pos]
		pos++
		switch c {
		case '{':
			return f, 0, pyErr(pyValueError, "unexpected '{' in field name")
		case '[':
			for pos < len(s) && s[pos] != ']' {
				pos++
			}
		case '}', ':', '!':
			break loop
		}
	}
	f.name = s[nameStart:max(nameStart, pos-1)]
	if c != '!' && c != ':' {
		if c != '}' {
			return f, 0, pyErr(pyValueError, "expected '}' before end of string")
		}
		return f, pos, nil
	}
	if c == '!' {
		if pos >= len(s) {
			return f, 0, pyErr(pyValueError, "end of string while looking for conversion specifier")
		}
		f.conversion = s[pos]
		pos++
		if pos < len(s) {
			c = s[pos]
			pos++
			if c == '}' {
				return f, pos, nil
			}
			if c != ':' {
				return f, 0, pyErr(pyValueError, "expected ':' after conversion specifier")
			}
		}
	}
	specStart := pos
	count := 1
	for pos < len(s) {
		c = s[pos]
		pos++
		switch c {
		case '{':
			f.expand = true
			count++
		case '}':
			count--
			if count == 0 {
				f.spec = s[specStart : pos-1]
				return f, pos, nil
			}
		}
	}
	return f, 0, pyErr(pyValueError, "unmatched '{' in format spec")
}

// renderMarkup is output_markup. It looks the field up, converts it, expands
// its spec and formats it, in that order, so the first failing step decides
// the error.
func renderMarkup(f field, attrs pyAttrs, depth int) (string, error) {
	v, err := fieldValue(f.name, attrs)
	if err != nil {
		return "", err
	}
	switch f.conversion {
	case 0:
	case 'r':
		v = pyRepr(v)
	case 's':
		v = pyStr(v)
	case 'a':
		v = pyASCII(v)
	default:
		if f.conversion > 32 && f.conversion < 127 {
			return "", pyErr(pyValueError, "Unknown conversion specifier %c", f.conversion)
		}
		return "", pyErr(pyValueError, "Unknown conversion specifier \\x%x", f.conversion)
	}
	spec := string(f.spec)
	if f.expand {
		if spec, err = buildString(f.spec, attrs, depth-1); err != nil {
			return "", err
		}
	}
	return pyFormatValue(v, spec)
}

// fieldInteger is get_integer in unicode_format.h: -1 unless every
// character is a decimal digit.
func fieldInteger(s []rune) (int64, error) {
	if len(s) == 0 {
		return -1, nil
	}
	var acc int64
	for _, r := range s {
		d, ok := pyDecimalValue(r)
		if !ok {
			return -1, nil
		}
		if acc > (math.MaxInt64-int64(d))/10 {
			return -1, pyErr(pyValueError, "Too many decimal digits in format string")
		}
		acc = acc*10 + int64(d)
	}
	return acc, nil
}

// fieldValue is get_field_object. The first part names a keyword argument,
// then each .attr or [key] applies in turn.
func fieldValue(name []rune, attrs pyAttrs) (any, error) {
	i := 0
	for i < len(name) && name[i] != '.' && name[i] != '[' {
		i++
	}
	first, rest := name[:i], name[i:]
	idx, err := fieldInteger(first)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 || idx >= 0 {
		if len(first) == 0 {
			idx = 0 // automatic numbering starts at 0
		}
		return nil, pyErr(pyIndexError, "Replacement index %d out of range for positional args tuple", idx)
	}
	key := string(first)
	v, ok := attrs[key]
	if !ok {
		return nil, pyErr(pyKeyError, "%s", pyRepr(key))
	}
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case '.':
			j := 0
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			attr := rest[:j]
			rest = rest[j:]
			if len(attr) == 0 {
				return nil, pyErr(pyValueError, "Empty attribute in format string")
			}
			if v, err = getAttr(v, string(attr)); err != nil {
				return nil, err
			}
		case '[':
			j := 0
			for j < len(rest) && rest[j] != ']' {
				j++
			}
			if j == len(rest) {
				return nil, pyErr(pyValueError, "Missing ']' in format string")
			}
			key := rest[:j]
			rest = rest[j+1:]
			idx, err := fieldInteger(key)
			if err != nil {
				return nil, err
			}
			if len(key) == 0 {
				return nil, pyErr(pyValueError, "Empty attribute in format string")
			}
			if v, err = getItem(v, string(key), idx); err != nil {
				return nil, err
			}
		default:
			return nil, pyErr(pyValueError, "Only '.' or '[' may follow ']' in format field specifier")
		}
	}
	return v, nil
}

// getAttr knows the data attributes of numbers. Methods such as str.upper
// would print as "<built-in method ...>" with an address, so every other name
// is an AttributeError.
func getAttr(v any, name string) (any, error) {
	switch t := v.(type) {
	case bool:
		n := int64(0)
		if t {
			n = 1
		}
		return getAttr(n, name)
	case int64:
		switch name {
		case "real", "numerator":
			return t, nil
		case "imag":
			return int64(0), nil
		case "denominator":
			return int64(1), nil
		}
	case float64:
		switch name {
		case "real":
			return t, nil
		case "imag":
			return 0.0, nil
		}
	}
	return nil, pyErr(pyAttributeError, "'%s' object has no attribute %s", pyTypeName(v), pyRepr(name))
}

// getItem is v[key] in a field. An all-digit key is an int, any other key a
// str.
func getItem(v any, key string, idx int64) (any, error) {
	switch t := v.(type) {
	case string:
		if idx < 0 {
			return nil, pyErr(pyTypeError, "string indices must be integers, not 'str'")
		}
		rs := []rune(t)
		if idx >= int64(len(rs)) {
			return nil, pyErr(pyIndexError, "string index out of range")
		}
		return string(rs[idx]), nil
	case []any:
		if idx < 0 {
			return nil, pyErr(pyTypeError, "list indices must be integers or slices, not str")
		}
		if idx >= int64(len(t)) {
			return nil, pyErr(pyIndexError, "list index out of range")
		}
		return t[idx], nil
	case *pyDict:
		if idx >= 0 {
			// The keys of a JSON dict are strings, so an int never matches.
			return nil, pyErr(pyKeyError, "%d", idx)
		}
		return t.item(key)
	}
	return nil, pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(v))
}

// pyFormatValue is format(v, spec), which str.format calls for each field.
func pyFormatValue(v any, spec string) (string, error) {
	switch t := v.(type) {
	case string:
		if spec == "" {
			return t, nil
		}
		sp, err := parseSpec([]rune(spec), 's', '<', "str")
		if err != nil {
			return "", err
		}
		if sp.typ != 's' {
			return "", unknownType(sp.typ, "str")
		}
		return formatString(t, sp)
	case bool:
		if spec == "" {
			return pyStr(t), nil
		}
		n := int64(0)
		if t {
			n = 1
		}
		return formatIntSpec(n, spec, "bool")
	case int64:
		if spec == "" {
			return pyStr(t), nil
		}
		return formatIntSpec(t, spec, "int")
	case float64:
		if spec == "" {
			return pyStr(t), nil
		}
		sp, err := parseSpec([]rune(spec), 0, '>', "float")
		if err != nil {
			return "", err
		}
		switch sp.typ {
		case 0, 'e', 'E', 'f', 'F', 'g', 'G', 'n', '%':
			return formatFloat(t, sp)
		}
		return "", unknownType(sp.typ, "float")
	}
	// object.__format__
	if spec == "" {
		return pyStr(v), nil
	}
	return "", pyErr(pyTypeError, "unsupported format string passed to %s.__format__", pyTypeName(v))
}

func formatIntSpec(n int64, spec, typeName string) (string, error) {
	sp, err := parseSpec([]rune(spec), 'd', '>', typeName)
	if err != nil {
		return "", err
	}
	switch sp.typ {
	case 'b', 'c', 'd', 'o', 'x', 'X', 'n':
		return formatInt(n, sp)
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		return formatFloat(float64(n), sp)
	}
	return "", unknownType(sp.typ, typeName)
}

func unknownType(t rune, typeName string) error {
	if t > 32 && t < 128 {
		return pyErr(pyValueError, "Unknown format code '%c' for object of type '%s'", t, typeName)
	}
	return pyErr(pyValueError, "Unknown format code '\\x%x' for object of type '%s'", t, typeName)
}

// localeType is LocaleType in formatter_unicode.c.
type localeType int

const (
	ltNone       localeType = 0
	ltComma      localeType = ','
	ltUnderscore localeType = '_'
	ltUnderFour  localeType = 4
)

// formatSpec is InternalFormatSpec.
type formatSpec struct {
	fill          rune
	align         rune
	alternate     bool
	noNeg0        bool
	sign          rune
	width         int64
	thousands     localeType
	precision     int64
	fracThousands localeType
	typ           rune
}

// specInteger is get_integer in formatter_unicode.c: the digits at pos, how
// many there were, and a ValueError on overflow.
func specInteger(s []rune, pos *int) (int64, int, error) {
	var acc int64
	n := 0
	for ; *pos < len(s); *pos, n = *pos+1, n+1 {
		d, ok := pyDecimalValue(s[*pos])
		if !ok {
			break
		}
		if acc > (math.MaxInt64-int64(d))/10 {
			return 0, 0, pyErr(pyValueError, "Too many decimal digits in format string")
		}
		acc = acc*10 + int64(d)
	}
	return acc, n, nil
}

func isAlign(r rune) bool { return r == '<' || r == '>' || r == '=' || r == '^' }

func commaAndUnderscore() error {
	return pyErr(pyValueError, "Cannot specify both ',' and '_'.")
}

func badSeparator(sep localeType, t rune) error {
	if t > 32 && t < 128 {
		return pyErr(pyValueError, "Cannot specify '%c' with '%c'.", rune(sep), t)
	}
	return pyErr(pyValueError, "Cannot specify '%c' with '\\x%x'.", rune(sep), t)
}

// parseSpec is parse_internal_render_format_spec:
// [[fill]align][sign][z][#][0][width][grouping][.precision[grouping]][type].
func parseSpec(s []rune, defType, defAlign rune, typeName string) (formatSpec, error) {
	sp := formatSpec{fill: ' ', align: defAlign, width: -1, precision: -1, typ: defType}
	pos, end := 0, len(s)
	alignSet, fillSet := false, false
	if end-pos >= 2 && isAlign(s[pos+1]) {
		sp.align, sp.fill = s[pos+1], s[pos]
		alignSet, fillSet = true, true
		pos += 2
	} else if end-pos >= 1 && isAlign(s[pos]) {
		sp.align = s[pos]
		alignSet = true
		pos++
	}
	if end-pos >= 1 && (s[pos] == ' ' || s[pos] == '+' || s[pos] == '-') {
		sp.sign = s[pos]
		pos++
	}
	if end-pos >= 1 && s[pos] == 'z' {
		sp.noNeg0 = true
		pos++
	}
	if end-pos >= 1 && s[pos] == '#' {
		sp.alternate = true
		pos++
	}
	if !fillSet && end-pos >= 1 && s[pos] == '0' {
		sp.fill = '0'
		if !alignSet && defAlign == '>' {
			sp.align = '='
		}
		pos++
	}
	w, n, err := specInteger(s, &pos)
	if err != nil {
		return sp, err
	}
	if n > 0 {
		sp.width = w
	}
	if end-pos > 0 && s[pos] == ',' {
		sp.thousands = ltComma
		pos++
	}
	if end-pos > 0 && s[pos] == '_' {
		if sp.thousands != ltNone {
			return sp, commaAndUnderscore()
		}
		sp.thousands = ltUnderscore
		pos++
	}
	if end-pos > 0 && s[pos] == ',' && sp.thousands == ltUnderscore {
		return sp, commaAndUnderscore()
	}
	if end-pos > 0 && s[pos] == '.' {
		pos++
		p, n, err := specInteger(s, &pos)
		if err != nil {
			return sp, err
		}
		sp.precision = p
		if end-pos > 0 && s[pos] == ',' {
			if n == 0 {
				sp.precision = -1
			}
			sp.fracThousands = ltComma
			pos++
			n++
		}
		if end-pos > 0 && s[pos] == '_' {
			if sp.fracThousands != ltNone {
				return sp, commaAndUnderscore()
			}
			if n == 0 {
				sp.precision = -1
			}
			sp.fracThousands = ltUnderscore
			pos++
			n++
		}
		if end-pos > 0 && s[pos] == ',' && sp.fracThousands == ltUnderscore {
			return sp, commaAndUnderscore()
		}
		if n == 0 {
			return sp, pyErr(pyValueError, "Format specifier missing precision")
		}
	}
	if end-pos > 1 {
		return sp, pyErr(pyValueError, "Invalid format specifier '%s' for object of type '%s'", string(s), typeName)
	}
	if end-pos == 1 {
		sp.typ = s[pos]
	}
	if sp.thousands != ltNone {
		switch sp.typ {
		case 'd', 'e', 'f', 'g', 'E', 'G', '%', 'F', 0:
		case 'b', 'o', 'x', 'X':
			if sp.thousands != ltUnderscore {
				return sp, badSeparator(sp.thousands, sp.typ)
			}
			sp.thousands = ltUnderFour
		default:
			return sp, badSeparator(sp.thousands, sp.typ)
		}
	}
	if sp.typ == 'n' && sp.fracThousands != ltNone {
		return sp, badSeparator(sp.fracThousands, sp.typ)
	}
	if sp.width > maxFormatWidth {
		return sp, pyErr(pyValueError, "width too big")
	}
	return sp, nil
}

// calcPadding is calc_padding for strings.
func calcPadding(n int64, sp formatSpec) (left, right int64) {
	total := n
	if sp.width >= 0 && sp.width > n {
		total = sp.width
	}
	switch sp.align {
	case '>':
		left = total - n
	case '^':
		left = (total - n) / 2
	}
	return left, total - n - left
}

// formatString is format_string_internal.
func formatString(s string, sp formatSpec) (string, error) {
	switch {
	case sp.sign == ' ':
		return "", pyErr(pyValueError, "Space not allowed in string format specifier")
	case sp.sign != 0:
		return "", pyErr(pyValueError, "Sign not allowed in string format specifier")
	case sp.noNeg0:
		return "", pyErr(pyValueError, "Negative zero coercion (z) not allowed in string format specifier")
	case sp.alternate:
		return "", pyErr(pyValueError, "Alternate form (#) not allowed in string format specifier")
	case sp.align == '=':
		return "", pyErr(pyValueError, "'=' alignment not allowed in string format specifier")
	}
	rs := []rune(s)
	n := int64(len(rs))
	if (sp.width == -1 || sp.width <= n) && (sp.precision == -1 || sp.precision >= n) {
		return s, nil
	}
	if sp.precision >= 0 && n >= sp.precision {
		n = sp.precision
	}
	left, right := calcPadding(n, sp)
	fill := string(sp.fill)
	return strings.Repeat(fill, int(left)) + string(rs[:n]) + strings.Repeat(fill, int(right)), nil
}

// formatInt is format_long_internal.
func formatInt(x int64, sp formatSpec) (string, error) {
	if sp.precision != -1 {
		return "", pyErr(pyValueError, "Precision not allowed in integer format specifier")
	}
	if sp.noNeg0 {
		return "", pyErr(pyValueError, "Negative zero coercion (z) not allowed in integer format specifier")
	}
	var p numberParts
	if sp.typ == 'c' {
		if sp.sign != 0 {
			return "", pyErr(pyValueError, "Sign not allowed with integer format specifier 'c'")
		}
		if sp.alternate {
			return "", pyErr(pyValueError, "Alternate form (#) not allowed with integer format specifier 'c'")
		}
		if x < 0 || x > 0x10ffff {
			return "", pyErr(pyOverflowError, "%%c arg not in range(0x110000)")
		}
		// As in CPython, the character goes out as a remainder, which is
		// copied and never grouped.
		p.remainder = string(rune(x))
	} else {
		base, prefix := 10, ""
		switch sp.typ {
		case 'b':
			base, prefix = 2, "0b"
		case 'o':
			base, prefix = 8, "0o"
		case 'x', 'X':
			base, prefix = 16, "0x"
		}
		u := uint64(x)
		if x < 0 {
			p.negative = true
			u = -u
		}
		p.digits = strconv.FormatUint(u, base)
		if sp.alternate {
			p.prefix = prefix
		}
	}
	loc := localeFor(sp.thousands, ltNone)
	if sp.typ == 'n' {
		loc = cLocale()
	}
	return renderNumber(p, sp, loc, sp.typ == 'X'), nil
}

// formatFloat is format_float_internal.
func formatFloat(v float64, sp formatSpec) (string, error) {
	if sp.precision > maxFormatWidth {
		return "", pyErr(pyValueError, "precision too big")
	}
	typ := sp.typ
	addDot0 := false
	defaultPrecision := int64(6)
	if typ == 0 {
		// No type: repr without a precision, else 'g' that keeps a ".0".
		addDot0 = true
		typ = 'r'
		defaultPrecision = 0
	}
	if typ == 'n' {
		typ = 'g'
	}
	pct := false
	if typ == '%' {
		typ = 'f'
		v *= 100
		pct = true
	}
	precision := sp.precision
	if precision < 0 {
		precision = defaultPrecision
	} else if typ == 'r' {
		typ = 'g'
	}
	buf := doubleToString(v, typ, int(precision), addDot0, sp.alternate, sp.noNeg0)
	if pct {
		buf += "%"
	}
	var p numberParts
	if strings.HasPrefix(buf, "-") {
		p.negative = true
		buf = buf[1:]
	}
	// parse_number: digits, an optional '.', more digits, then the rest.
	i := 0
	for i < len(buf) && buf[i] >= '0' && buf[i] <= '9' {
		i++
	}
	p.digits = buf[:i]
	if i < len(buf) && buf[i] == '.' {
		p.decimal = true
		i++
		j := i
		for j < len(buf) && buf[j] >= '0' && buf[j] <= '9' {
			j++
		}
		p.frac = buf[i:j]
		i = j
	}
	p.remainder = buf[i:]
	loc := localeFor(sp.thousands, sp.fracThousands)
	if sp.typ == 'n' {
		loc = cLocale()
	}
	return renderNumber(p, sp, loc, false), nil
}

// numberLocale is LocaleInfo. grouping is the group size, 0 for none.
type numberLocale struct {
	grouping int
	sep      string
	fracSep  string
}

// localeFor is get_locale_info for the hard-coded locales.
func localeFor(t, frac localeType) numberLocale {
	var loc numberLocale
	switch t {
	case ltComma:
		loc = numberLocale{grouping: 3, sep: ","}
	case ltUnderscore:
		loc = numberLocale{grouping: 3, sep: "_"}
	case ltUnderFour:
		loc = numberLocale{grouping: 4, sep: "_"}
	}
	if frac != ltNone {
		loc.fracSep = string(rune(frac))
		if loc.grouping == 0 {
			loc.grouping = 3
		}
	}
	return loc
}

// cLocale is the current locale for 'n'. Python sets only LC_CTYPE at
// startup, so LC_NUMERIC is "C": a '.' and no grouping.
func cLocale() numberLocale {
	return numberLocale{}
}

// numberParts is a number split as calc_number_widths sees it.
type numberParts struct {
	negative  bool
	prefix    string // 0x, 0o or 0b
	digits    string // before the decimal point; grouped
	decimal   bool
	frac      string // after the decimal point
	remainder string // exponent, %, inf, nan, or the character of 'c'
}

// renderNumber is calc_number_widths followed by fill_number:
// <lpad><sign><prefix><spad><grouped digits><decimal><frac><remainder><rpad>.
func renderNumber(p numberParts, sp formatSpec, loc numberLocale, upper bool) string {
	var sign string
	switch {
	case p.negative:
		sign = "-"
	case sp.sign == '+':
		sign = "+"
	case sp.sign == ' ':
		sign = " "
	}
	nFrac := int64(len(p.frac))
	frac := ""
	if nFrac > 0 {
		frac = groupDigits(p.frac, nFrac, loc.grouping, loc.fracSep, true)
	}
	nGroupedFrac := int64(pyLen(frac))
	nDecimal := int64(0)
	if p.decimal {
		nDecimal = 1
	}
	nonDigit := int64(len(sign)+len(p.prefix)) + nDecimal + nFrac + int64(pyLen(p.remainder))
	minWidth := int64(0)
	if sp.fill == '0' && sp.align == '=' {
		minWidth = sp.width - nonDigit + nFrac - nGroupedFrac
	}
	digits := ""
	if p.digits != "" {
		digits = groupDigits(p.digits, minWidth, loc.grouping, loc.sep, false)
	}
	var lpad, spad, rpad int64
	if n := sp.width - (nonDigit + int64(pyLen(digits)) + nGroupedFrac - nFrac); n > 0 {
		switch sp.align {
		case '<':
			rpad = n
		case '^':
			lpad = n / 2
			rpad = n - lpad
		case '=':
			spad = n
		case '>':
			lpad = n
		}
	}
	prefix := p.prefix
	if upper {
		prefix = strings.ToUpper(prefix)
		digits = strings.ToUpper(digits)
	}
	fill := string(sp.fill)
	var b strings.Builder
	b.WriteString(strings.Repeat(fill, int(lpad)))
	b.WriteString(sign)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat(fill, int(spad)))
	b.WriteString(digits)
	if p.decimal {
		b.WriteByte('.')
	}
	b.WriteString(frac)
	b.WriteString(p.remainder)
	b.WriteString(strings.Repeat(fill, int(rpad)))
	return b.String()
}

// groupDigits is _PyUnicode_InsertThousandsGrouping: digits in groups of
// size from the right, or from the left when forward, zero-padded to
// minWidth with separators between the zeros too.
func groupDigits(digits string, minWidth int64, size int, sep string, forward bool) string {
	minWidth = max(0, minWidth)
	remaining := int64(len(digits))
	sepLen := int64(len(sep))
	var groups []string
	take := func(l int64) {
		zeros := strings.Repeat("0", int(max(0, l-remaining)))
		n := max(0, min(remaining, l))
		var chars string
		if forward {
			done := int64(len(digits)) - remaining
			chars = digits[done : done+n]
			groups = append(groups, chars+zeros)
		} else {
			chars = digits[remaining-n : remaining]
			groups = append(groups, zeros+chars)
		}
		remaining -= n
	}
	broken := false
	if size > 0 {
		for {
			l := min(int64(size), max(remaining, minWidth, 1))
			take(l)
			minWidth -= l
			if remaining <= 0 && minWidth <= 0 {
				broken = true
				break
			}
			minWidth -= sepLen
		}
	}
	if !broken {
		take(max(remaining, minWidth, 1))
	}
	if !forward {
		for i, j := 0, len(groups)-1; i < j; i, j = i+1, j-1 {
			groups[i], groups[j] = groups[j], groups[i]
		}
	}
	return strings.Join(groups, sep)
}

// floatRepr is repr(f), which str(f) also gives.
func floatRepr(f float64) string {
	return doubleToString(f, 'r', 0, true, false, false)
}

// doubleToString is PyOS_double_to_string: code is e, E, f, F, g, G or r.
func doubleToString(v float64, code rune, precision int, addDot0, alt, noNeg0 bool) string {
	upper := false
	mode := 0
	switch code {
	case 'E':
		upper = true
		code = 'e'
		fallthrough
	case 'e':
		mode = 2
		precision++
	case 'F':
		upper = true
		code = 'f'
		fallthrough
	case 'f':
		mode = 3
	case 'G':
		upper = true
		code = 'g'
		fallthrough
	case 'g':
		mode = 2
		if precision == 0 {
			precision = 1
		}
	case 'r':
		precision = 0
	}
	return formatFloatShort(v, code, mode, precision, addDot0, alt, noNeg0, upper)
}

// dtoa gives the digits _Py_dg_dtoa would: no trailing zeros, decpt the
// position of the decimal point. Mode 0 is the shortest repr, mode 2 n
// significant digits, mode 3 n digits after the point. Go's strconv rounds
// exactly with ties to even, as dtoa does.
func dtoa(v float64, mode, n int) (digits string, decpt int) {
	if v == 0 {
		return "0", 1
	}
	v = math.Abs(v)
	if mode == 3 {
		s := strconv.FormatFloat(v, 'f', n, 64)
		intPart, fracPart, _ := strings.Cut(s, ".")
		all := intPart + fracPart
		decpt = len(intPart)
		trimmed := strings.TrimLeft(all, "0")
		decpt -= len(all) - len(trimmed)
		trimmed = strings.TrimRight(trimmed, "0")
		if trimmed == "" {
			// A value that rounds to zero has no digits, as fcvt gives.
			return "", -n
		}
		return trimmed, decpt
	}
	prec := -1
	if mode == 2 {
		prec = n - 1
	}
	s := strconv.FormatFloat(v, 'e', prec, 64)
	mant, exp, _ := strings.Cut(s, "e")
	e, _ := strconv.Atoi(exp)
	digits = strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	return digits, e + 1
}

// formatFloatShort is format_float_short in pystrtod.c.
func formatFloatShort(v float64, code rune, mode, precision int, addDot0, alt, noNeg0, upper bool) string {
	neg := math.Signbit(v)
	if math.IsInf(v, 0) || math.IsNaN(v) {
		s := "inf"
		if math.IsNaN(v) {
			s = "nan"
			neg = false
		}
		if upper {
			s = strings.ToUpper(s)
		}
		if neg {
			s = "-" + s
		}
		return s
	}
	digits, decpt := dtoa(v, mode, precision)
	if noNeg0 && neg && (digits == "" || digits == "0") {
		neg = false
	}
	nd := len(digits)
	vEnd := nd
	useExp := false
	switch code {
	case 'e':
		useExp = true
		vEnd = precision
	case 'f':
		vEnd = decpt + precision
	case 'g':
		limit := precision
		if addDot0 {
			limit = precision - 1
		}
		if decpt <= -4 || decpt > limit {
			useExp = true
		}
		if alt {
			vEnd = precision
		}
	case 'r':
		if decpt <= -4 || decpt > 16 {
			useExp = true
		}
	}
	exp := 0
	if useExp {
		exp = decpt - 1
		decpt = 1
	}
	vStart := 0
	if decpt <= 0 {
		vStart = decpt - 1
	}
	if !useExp && addDot0 {
		vEnd = max(vEnd, decpt+1)
	} else {
		vEnd = max(vEnd, decpt)
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	zeros := func(n int) { b.WriteString(strings.Repeat("0", max(0, n))) }
	if decpt <= 0 {
		zeros(decpt - vStart)
		b.WriteByte('.')
		zeros(-decpt)
	} else {
		zeros(-vStart)
	}
	if decpt > 0 && decpt <= nd {
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	} else {
		b.WriteString(digits)
	}
	if nd < decpt {
		zeros(decpt - nd)
		b.WriteByte('.')
		zeros(vEnd - decpt)
	} else {
		zeros(vEnd - nd)
	}
	s := b.String()
	if strings.HasSuffix(s, ".") && !alt {
		s = s[:len(s)-1]
	}
	if useExp {
		e := "e"
		if upper {
			e = "E"
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		es := strconv.Itoa(exp)
		if len(es) < 2 {
			es = "0" + es
		}
		s += e + sign + es
	}
	return s
}
