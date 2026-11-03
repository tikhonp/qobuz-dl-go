# 2: Python compatibility layer

## Read first

- [design.md](../design.md) section Python compatibility.
- [01-golden.md](01-golden.md) As built.
- The goldens `pystr`, `format`, `strings`, `textwrap`, `regex`, `normalize_json`.
- CPython's `Objects/stringlib/unicode_format.h`, `Python/formatter_unicode.c` and `Lib/textwrap.py` for 3.14, for the rules the goldens don't spell out.

## Done when

The helpers below exist with tests, and every case in their goldens passes.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Value model | `nil`, `bool`, `int64`, `float64`, `string`, `[]any`, `*pyDict`. No wrapper types. | Type switches stay short; `pyStr` and `pyFormat` dispatch on these. |
| JSON decoding | `json.Decoder` with `UseNumber`, walking tokens so object keys keep their order. A number is `int64` when it has no `.`, `e` or `E`, as Python's `json`. | Key order and int/float both matter. |
| Errors | `*pyError` with a kind; messages as Python prints them, such as `KeyError: 'title'`. | `isPy` checks mirror `except` clauses. |
| Float printing | Python's repr rule: shortest round-trip digits, fixed notation for exponents −4 to 15 with `.0` on whole numbers, else scientific with two or more exponent digits. | `strconv`'s `'g'` switches at 21 digits and drops `.0`. |
| Case mapping | `x/text/cases` with `language.Und` for lower, upper and fold; `title` and `capitalize` written by hand after CPython. | Full Unicode mappings, as Python. |
| `\w` and `\s` | Character classes in `pyre.go` built so the `regex.json` golden passes. | RE2's classes are ASCII. |
| Format specs | The full spec mini-language for `str`, `int`, `float`, `bool`, `None`; other types are not reachable. | Users write templates; any valid spec must behave. |

## Files

```
pyvalue.go  pystr.go  pyre.go  format.go  textwrap.go
pyvalue_test.go  pystr_test.go  pyre_test.go  format_test.go  textwrap_test.go
```

## Go signatures

```go
func decodeJSON(r io.Reader) (any, error)
func newDict() *pyDict
func (d *pyDict) get(key string, def any) any
func (d *pyDict) item(key string) (any, error)
func (d *pyDict) set(key string, v any)
func (d *pyDict) has(key string) bool
func (d *pyDict) keysInOrder() []string
func (d *pyDict) dict(key string) (*pyDict, error)
func (d *pyDict) list(key string) ([]any, error)
func (d *pyDict) clone() *pyDict
func (d *pyDict) MarshalJSON() ([]byte, error)
func asDict(v any) (*pyDict, error)
func pyStr(v any) string
func pyRepr(v any) string
func pyTruthy(v any) bool
func pyInt(v any) (int64, error)
func pyErr(kind pyKind, format string, args ...any) error
func isPy(err error, kinds ...pyKind) bool

func pyLen(s string) int
func pySlice(s string, start, end int) string // Python slice semantics, negative and out-of-range indices
func pyLower(s string) string
func pyUpper(s string) string
func pyCasefold(s string) string
func pyTitle(s string) string
func pyCapitalize(s string) string
func pyStrip(s, chars string) string // chars "" means Python whitespace
func pyLstrip(s, chars string) string
func pyRstrip(s, chars string) string
func pyIsDigit(s string) bool
func pyLjust(s string, width int) string
func pyIsSpace(r rune) bool
func pyIsWord(r rune) bool

type pyAttrs map[string]any
func pyFormat(tmpl string, attrs pyAttrs) (string, error)
func pyFormatValue(v any, spec string) (string, error) // format(v, spec)

func textwrapFill(text string, width int) string
```

## Checklist

| Edge case | Test |
| --- | --- |
| JSON keys keep their order; `96` decodes as int, `96.0` and `1e3` as float | `TestDecodeJSONKeepsOrderAndNumberTypes` |
| `get` returns the default only for a missing key; `None` comes back as nil | `TestGetDefaultOnlyWhenMissing` |
| `dict()` on null or a string is an AttributeError | `TestDictOnNullIsAttributeError` |
| `str()` of every value in the golden | `TestPyStrGolden` |
| Truthiness of `""`, `0`, `0.0`, `[]`, `{}`, `None`, `"0"` | `TestPyTruthy` |
| Every string method over the corpus | `TestStringMethodsGolden` |
| `İ`, `ß`, `ǅ`, `ﬁ` in lower, upper, casefold, title | `TestCaseMappingSpecialCases` |
| Python's whitespace set, including `\x1c` and `　` | `TestPyIsSpace` |
| `\w` and `\s` membership over the corpus | `TestRegexClassesGolden` |
| Every template and value in `format.json`, results and exception kinds | `TestPyFormatGolden` |
| `format("1", "02")` is `10`; `format(None, "02")` is a TypeError | `TestFormatSpecOnStringsAndNone` |
| Unbalanced braces, `{}` and `{0}` without positional arguments | `TestFormatErrors` |
| `textwrap.fill` over descriptions, long words, hyphens | `TestTextwrapGolden` |
| `_normalize_json_strings` cases | `TestNormalizeJSONStringsGolden`, in step 4 once `normalizeJSONStrings` exists; this step only decodes the golden |

## As built

Filled in at the end of the step.
