// Ports sanitize_filename and sanitize_filepath from pathvalidate 3.3.1
// (_filename.py, _filepath.py, _base.py, _common.py), the version qobuz-dl
// 2.5.9's pinned venv runs. Only the branches upstream reaches are ported: the
// universal platform, the default max_len, UTF-8 as fs_encoding, the default
// handlers, and no validate_after_sanitize.

package qobuzdl

import (
	"strings"
)

const (
	// sanitize_filename's default max_len.
	pvMaxFilenameLen = 255
	// sanitize_filepath passes max_len=-1, which becomes the universal
	// platform's 260; its per-entry filename sanitizer gets the same limit.
	pvMaxPathLen = 260
)

// pvReservedNames is FileNameValidator.reserved_keywords for the universal
// platform: the Windows device names and macOS's ":".
var pvReservedNames = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "CLOCK$": true, "NUL": true, ":": true}
	for _, base := range []string{"COM", "LPT"} {
		for _, n := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3"} {
			m[base+n] = true
		}
	}
	return m
}()

// pvNTFSReserved is _NTFS_RESERVED_FILE_NAMES. sanitize_filepath compares
// path entries with it case-sensitively.
var pvNTFSReserved = map[string]bool{
	"$Mft": true, "$MftMirr": true, "$LogFile": true, "$Volume": true, "$AttrDef": true,
	"$Bitmap": true, "$Boot": true, "$BadClus": true, "$Secure": true, "$Upcase": true,
	"$Extend": true, "$Quota": true, "$ObjId": true, "$Reparse": true,
}

// pvInvalidWinFilename is _RE_INVALID_WIN_FILENAME's class: the ASCII
// characters outside string.printable, the whitespace controls, and
// / \ : * ? " < > |.
func pvInvalidWinFilename(r rune) bool {
	return r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r)
}

// pvInvalidWinPath is _RE_INVALID_WIN_PATH's class, which keeps / and \.
func pvInvalidWinPath(r rune) bool {
	return r < 0x20 || r == 0x7f || strings.ContainsRune(`:*?"<>|`, r)
}

// pvSub is regexp.sub over a one-character class.
func pvSub(s string, invalid func(rune) bool, replacement string) string {
	var b strings.Builder
	for _, r := range s {
		if invalid(r) {
			b.WriteString(replacement)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pvTruncate is truncate_str with UTF-8: cut to maxBytes and drop a
// character the cut split, as decode(..., "ignore") does.
func pvTruncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && cut < len(s) && s[cut]&0xc0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// pvIsNull is is_null_string: empty after str.strip().
func pvIsNull(s string) bool {
	return pyStrip(s, "") == ""
}

// pvRootName is BaseValidator.__extract_root_name: the base name up to its
// first dot, kept whole for ".", ".." and paths starting with three dots, and
// "" for a base name starting with a dot.
func pvRootName(path string) string {
	if path == "." || path == ".." || strings.HasPrefix(path, "...") {
		return path
	}
	base := posixBasename(path)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		return base[:i]
	}
	return base
}

// posixBasename is posixpath.basename.
func posixBasename(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}

// ntIsAbs is ntpath.isabs from CPython 3.14: a drive with a root, or a UNC
// or device path.
func ntIsAbs(s string) bool {
	rs := []rune(strings.ReplaceAll(s, "/", `\`))
	return (len(rs) >= 2 && rs[0] == '\\' && rs[1] == '\\') || (len(rs) >= 3 && rs[1] == ':' && rs[2] == '\\')
}

// pvReason is the ErrorReason of the ValidationError a validator raises.
type pvReason int

const (
	pvValid pvReason = iota
	pvNullName
	pvFoundAbsPath
	pvInvalidLength
	pvReservedName
	pvInvalidChar
)

// pvValidateFilename is FileNameValidator.validate for the universal
// platform: the reason of the one error it raises, and for a reserved name
// the root name the error carries.
func pvValidateFilename(name string, maxLen int) (pvReason, string) {
	if pvIsNull(name) {
		return pvNullName, ""
	}
	if ntIsAbs(name) || strings.HasPrefix(name, "/") {
		return pvFoundAbsPath, ""
	}
	// A non-blank name is never shorter than min_len, 1 byte.
	if len(name) > maxLen {
		return pvInvalidLength, ""
	}
	root := pvRootName(name)
	if pvReservedNames[pyUpper(root)] || pvReservedNames[pyUpper(posixBasename(name))] {
		return pvReservedName, root
	}
	// The universal check's characters are a subset of the Windows check's,
	// and both raise INVALID_CHARACTER.
	if strings.IndexFunc(name, pvInvalidWinFilename) >= 0 {
		return pvInvalidChar, ""
	}
	if name == "." || name == ".." {
		return pvValid, ""
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") || strings.HasPrefix(name, " ") {
		return pvInvalidChar, ""
	}
	return pvValid, ""
}

// sanitizeFilename is pathvalidate.sanitize_filename(name, replacement_text).
func sanitizeFilename(name, replacement string) string {
	return pvSanitizeFilename(name, replacement, pvMaxFilenameLen)
}

// pvSanitizeFilename is FileNameSanitizer.sanitize. It acts on the first
// error the validator raises only, so a reserved name keeps trailing dots
// and spaces: "CON. " becomes "CON_. ".
func pvSanitizeFilename(name, replacement string, maxLen int) string {
	if pvIsNull(name) {
		return ""
	}
	s := pvSub(name, pvInvalidWinFilename, replacement)
	s = pvTruncate(s, maxLen)
	switch reason, root := pvValidateFilename(s, maxLen); reason {
	case pvReservedName:
		// add_trailing_underscore, then re.sub(re.escape(root), ...), which
		// replaces every occurrence of the root, case-sensitively.
		if root != "." && root != ".." {
			s = strings.ReplaceAll(s, root, root+"_")
		}
	case pvInvalidChar:
		s = strings.TrimLeft(s, " ")
		s = strings.TrimRight(s, " ")
		if s != "." && s != ".." {
			s = strings.TrimRight(s, " .")
		}
	case pvNullName:
		s = ""
	}
	return s
}

// sanitizeFilepath is pathvalidate.sanitize_filepath(path, replacement_text):
// the drive kept as is, invalid characters replaced, the path normalized, and
// each entry sanitized as a file name.
func sanitizeFilepath(path, replacement string) string {
	if pvIsNull(path) {
		return ""
	}
	drive, tail := ntSplitDrive(path)
	tail = pvSub(tail, pvInvalidWinPath, replacement)
	if tail != "" {
		tail = posixNormpath(tail)
	}
	var entries []string
	if drive != "" {
		entries = append(entries, drive)
	}
	for _, entry := range strings.Split(strings.ReplaceAll(tail, `\`, "/"), "/") {
		if pvNTFSReserved[entry] {
			entries = append(entries, entry+"_")
			continue
		}
		e := pvSanitizeFilename(entry, replacement, pvMaxPathLen)
		if e == "" {
			// An empty first entry keeps the leading "/" of an absolute path.
			if len(entries) == 0 {
				entries = append(entries, "")
			}
			continue
		}
		entries = append(entries, e)
	}
	joined := strings.Join(entries, "/")
	if pvPathValidateNull(joined) {
		return ""
	}
	return joined
}

// pvPathValidateNull reports whether FilePathValidator.validate raises
// NULL_NAME, the only error sanitize_filepath acts on. It does for a blank
// path, and for a blank entry the validator reaches before any other error:
// "\u00a0." sanitizes to the entry "\u00a0", which empties a relative path.
func pvPathValidateNull(value string) bool {
	if pvIsNull(value) {
		return true
	}
	// validate_abspath raises MALFORMED_ABS_PATH for these.
	if strings.HasPrefix(value, "/") {
		return false
	}
	drive, tail := ntSplitDrive(value)
	if drive != "" && ntIsAbs(value) {
		return false
	}
	if tail == "" || len(tail) > pvMaxPathLen {
		return false
	}
	// The reserved names of FilePathValidator are "/" and ":", which no
	// base name holds after sanitizing, so that check never raises.
	for _, entry := range strings.Split(strings.ReplaceAll(tail, `\`, "/"), "/") {
		if entry == "" || entry == "." || entry == ".." {
			continue
		}
		switch reason, _ := pvValidateFilename(entry, pvMaxPathLen); reason {
		case pvValid:
		case pvNullName:
			return true
		default:
			return false
		}
	}
	return false
}

// ntSplitDrive is ntpath.splitdrive from CPython 3.14, through splitroot.
func ntSplitDrive(p string) (drive, rest string) {
	norm := strings.ReplaceAll(p, "/", `\`)
	rs := []rune(norm)
	at := func(i int) rune {
		if i < len(rs) {
			return rs[i]
		}
		return 0
	}
	// Indices below are in code points, as Python slices; prefix() converts.
	prefix := func(n int) string { return string([]rune(p)[:min(n, len(rs))]) }
	switch {
	case at(0) == '\\' && at(1) == '\\':
		// UNC drives, \\server\share or \\?\UNC\server\share, and device
		// drives, \\.\device or \\?\device.
		start := 2
		if pyUpper(string(rs[:min(8, len(rs))])) == `\\?\UNC\` {
			start = 8
		}
		i := runeIndex(rs, '\\', start)
		if i < 0 {
			return p, ""
		}
		j := runeIndex(rs, '\\', i+1)
		if j < 0 {
			return p, ""
		}
		d := prefix(j)
		return d, p[len(d):]
	case at(0) == '\\':
		return "", p
	case at(1) == ':':
		// Any first character, not only a letter, makes a drive.
		d := prefix(2)
		return d, p[len(d):]
	}
	return "", p
}

func runeIndex(rs []rune, r rune, from int) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// posixNormpath is posixpath.normpath. It differs from path.Clean in keeping
// exactly two leading slashes, which POSIX leaves implementation-defined.
func posixNormpath(p string) string {
	if p == "" {
		return "."
	}
	var initial string
	switch {
	case !strings.HasPrefix(p, "/"):
	case !strings.HasPrefix(p, "//") || strings.HasPrefix(p, "///"):
		initial, p = "/", p[1:]
	default:
		initial, p = "//", p[2:]
	}
	var comps []string
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "" || c == ".":
		case c != ".." || (initial == "" && len(comps) == 0) || (len(comps) > 0 && comps[len(comps)-1] == ".."):
			comps = append(comps, c)
		case len(comps) > 0:
			comps = comps[:len(comps)-1]
		}
	}
	out := initial + strings.Join(comps, "/")
	if out == "" {
		return "."
	}
	return out
}
