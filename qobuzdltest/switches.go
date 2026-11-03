// The switches tests flip to make the fake misbehave. They are safe to call
// while requests run.

package qobuzdltest

// switches is the fake's misbehavior, guarded by Server.mu.
type switches struct {
	fail404     map[string]bool
	failTimes   map[string]failure
	cutAfter    map[string]int64
	ignoreRange map[string]bool
	sample      map[trackFormat]bool
	empty       map[string]bool
	restrict    map[trackFormat]bool
	deliver     map[string]int
	expired     bool
}

type failure struct{ left, status int }

type trackFormat struct {
	track string
	fmtID int
}

func newSwitches() switches {
	return switches{
		fail404:     map[string]bool{},
		failTimes:   map[string]failure{},
		cutAfter:    map[string]int64{},
		ignoreRange: map[string]bool{},
		sample:      map[trackFormat]bool{},
		empty:       map[string]bool{},
		restrict:    map[trackFormat]bool{},
		deliver:     map[string]int{},
	}
}

// Fail404 makes the CDN answer 404 to every request for the track's file.
func (s *Server) Fail404(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.fail404[trackID] = true
}

// FailTimes makes the CDN answer the next n requests for the track's file
// with status, and serve the file after that.
func (s *Server) FailTimes(trackID string, n, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.failTimes[trackID] = failure{left: n, status: status}
}

// CutAfter makes the CDN close the next answer for the track's file after n
// bytes of its body, while the headers promise the whole length. It happens
// once.
func (s *Server) CutAfter(trackID string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.cutAfter[trackID] = n
}

// IgnoreRange makes the CDN answer requests for the track's file with 200
// and the whole file, whatever Range asks.
func (s *Server) IgnoreRange(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.ignoreRange[trackID] = true
}

// Sample makes getFileUrl for the track at that format add "sample": true,
// as Qobuz answers with a 30-second preview.
func (s *Server) Sample(trackID string, fmtID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.sample[trackFormat{trackID, fmtID}] = true
}

// EmptyAnswer makes getFileUrl answer {} for the track at every format, as
// Qobuz does for a demo.
func (s *Server) EmptyAnswer(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.empty[trackID] = true
}

// Restrict makes getFileUrl for the track at that format deliver the next
// tier down, 27 to 7 to 6 to 5, with the FormatRestrictedByFormatAvailability
// restriction, as Qobuz answers a downgrade. At format 5 it adds the
// restriction and keeps the format, since no tier is lower.
func (s *Server) Restrict(trackID string, fmtID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.restrict[trackFormat{trackID, fmtID}] = true
}

// Deliver makes getFileUrl for the track answer format fmtID, whatever
// format the request asks for, without restrictions; the URL serves that
// format's file.
func (s *Server) Deliver(trackID string, fmtID int) {
	mustFormat(fmtID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.deliver[trackID] = fmtID
}

// ExpireToken makes every API call answer 401, as Qobuz does once a token
// has expired. The bundle and the CDN keep working.
func (s *Server) ExpireToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.expired = true
}

// Reset clears the switches and the request log.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw = newSwitches()
	s.log = nil
}
