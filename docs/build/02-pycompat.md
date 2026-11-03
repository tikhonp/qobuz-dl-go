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

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Differences from the contract

- A sixth kind, `pyOverflowError`. `format(-1, "c")` and `format(1234567, "c")` raise it in `format.json`, and `int(inf)` does too.
- Python's `\w` is the L and N categories plus `_`, with no marks; design.md had `\p{Mn}\p{Mc}`. The classes are `var` strings built at init from Go's tables, not `const`, and come as `reW`, `reNotW`, `reS`, `reNotS`, `reD`, `reNotD`, with the insides `reWordChars`, `reSpaceChars` and `reDigitChars` for combining with other characters.
- Go's `unicode` and x/text's `cases` and `norm` are all Unicode 17.0; Python 3.14 is 16.0. `newerThanPython` in `pystr.go` lists the 4,803 code points 17.0 added, and every predicate and mapping leaves them out. Two more differences needed code: U+0295 ʕ is Ll in 16.0 and Lo in 17.0, and x/text maps U+A7D3 and U+A7D5 to capitals that are new in 17.0, so any mapping to a code point Python doesn't know keeps the character.
- x/text's `Fold` lowers Cherokee capitals U+13A0 to U+13F5; CaseFolding.txt and Python leave them, since Cherokee folds to its capitals. `pyCasefold` keeps them.
- The case methods map one character at a time with x/text, as CPython does, and handle final sigma with CPython's `handle_capital_sigma`. Title and capitalize follow CPython's code; the port uses no whole-string x/text caser.
- A new golden, `unicode.json`, from `gen_unicode` in `gen.py`: Python's Unicode version, the assigned, `isdigit`, `isdecimal`, `isspace`, `isprintable`, Cased and Case_Ignorable sets, and every character `lower`, `upper`, `title` and `casefold` change. The tests check every code point against it. The other goldens came out byte for byte the same.
- More helpers than the signature list: `pyRepr`, `pyASCII`, `pyTypeName`, `pyIter`, `pyIsDecimal`, `pyDecimalValue`, `pyIsDigitRune`, `pyIsPrintable`, `pyIsCased`, `pyIsCaseIgnorable`.
- `(*pyDict).list` iterates as Python does: a string gives its characters and a dict its keys; None and numbers are a TypeError. `asDict` and `dict()` raise AttributeError with "has no attribute 'get'".
- `decodeJSON` keeps the first place of a repeated key with the last value, as Python's dict does, and decodes out-of-range floats as ±inf or 0. It refuses ints beyond int64.
- In templates, `{x.attr}` knows only `real`, `imag`, `numerator` and `denominator`; widths and float precisions over 2^24 are a ValueError. Both are in UPSTREAM.md.
- `golden_test.go` now loads goldens with `decodeJSON` into `*pyDict`; `goldenDict` is gone. It also has `goldenRanges`, `inRanges`, `goldenByName` and `eachRune`.
- `textwrap.go` replaces `wordsep_re` with a scanner, since the pattern needs lookbehind.

### Checks

Beyond the goldens, throwaway differential runs against the venv's Python, not committed: 140,000 random format specs over ints, big ints, floats, bools, None and strings; 60,000 random templates; 200,000 floats by bit pattern with ten specs; 100,000 `textwrap.fill` calls at widths 1 to 70; 100,000 strings of Greek sigma, combining marks, digraphs and ligatures through the five case methods. No differences.

### Notes for later steps

- Step 3: build every regex with the classes from `pyre.go` and name its Python source. `[-\w\d]` in `get_url_info` becomes `"[-" + reWordChars + reDigitChars + "]"`. The lookaround in `clean_filename` step 4 still needs a scanner; `wordsepSplit` in `textwrap.go` shows the shape.
- Step 3: `pyStrip(s, "")` is `s.strip()`. To strip a literal empty set, don't call it.
- Step 4: `normalize_json.json` loads; `TestNormalizeJSONStringsGolden` comes with `normalizeJSONStrings`.
- The test run is about 8 seconds under `-race`, most of it the per-code-point checks in `TestUnicodePropertiesGolden` and `TestCaseMapsGolden`, which run in parallel.
- When Go's Unicode version changes, `TestUnicodePropertiesGolden` fails until `newerThanPython` is updated. When the bot's Python moves past 3.14, regenerate the goldens and shrink the table.
