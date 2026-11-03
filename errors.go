// Ports qobuz_dl/exceptions.py from qobuz-dl 2.5.9, plus APIError and the
// sentinels for failures Python handles without an exception class.

package qobuzdl

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Sentinels for the conditions callers check with errors.Is. The first six
// are upstream's exception classes.
var (
	ErrAuthentication   = errors.New("qobuzdl: authentication failed")  // AuthenticationError, and Python's sys.exit on auth
	ErrIneligible       = errors.New("qobuzdl: account not eligible")   // IneligibleError, unused upstream
	ErrInvalidAppID     = errors.New("qobuzdl: invalid app ID")         // InvalidAppIdError, unused upstream
	ErrInvalidAppSecret = errors.New("qobuzdl: invalid app secret")     // InvalidAppSecretError
	ErrInvalidQuality   = errors.New("qobuzdl: invalid quality")        // InvalidQuality
	ErrNonStreamable    = errors.New("qobuzdl: release not streamable") // NonStreamable

	ErrBundle        = errors.New("qobuzdl: bundle not understood") // bundle.py's NotImplementedError
	ErrInvalidURL    = errors.New("qobuzdl: not a Qobuz URL")
	ErrEmptyContent  = errors.New("qobuzdl: content empty or unavailable")
	ErrQueryTooShort = errors.New("qobuzdl: search query shorter than 3 characters")
	ErrUnhandled     = errors.New("qobuzdl: exception upstream does not handle")
)

// apiErrorBodyMax is the most of a response body an APIError keeps.
const apiErrorBodyMax = 200

// APIError is an API answer with a status upstream treats as a failure.
type APIError struct {
	Endpoint string // such as "album/get"
	Status   int
	Body     string // at most 200 bytes, with the token and secrets cut out
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("qobuzdl: %s: HTTP %d", e.Endpoint, e.Status)
	}
	return fmt.Sprintf("qobuzdl: %s: HTTP %d: %s", e.Endpoint, e.Status, e.Body)
}

// newAPIError builds an APIError whose body holds none of the secrets, such
// as the user token and the app secrets. It cuts each secret out before
// shortening the body, so no prefix of a secret survives.
func newAPIError(endpoint string, status int, body []byte, secrets ...string) *APIError {
	s := strings.ToValidUTF8(string(body), "�")
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	if len(s) > apiErrorBodyMax {
		cut := apiErrorBodyMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return &APIError{Endpoint: endpoint, Status: status, Body: s}
}
