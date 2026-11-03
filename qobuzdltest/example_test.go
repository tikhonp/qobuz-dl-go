package qobuzdltest_test

import (
	"context"
	"fmt"
	"testing"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/qobuzdltest"
)

// exampleTB stands in for the *testing.T a test passes to New, which an
// Example doesn't have. New calls only Helper, Cleanup and Fatalf.
type exampleTB struct {
	testing.TB
	cleanups []func()
}

func (t *exampleTB) Helper()                        {}
func (t *exampleTB) Cleanup(f func())               { t.cleanups = append(t.cleanups, f) }
func (t *exampleTB) Fatalf(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }

func (t *exampleTB) close() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}

// In a test, pass t to New; the server closes when the test ends.
func ExampleNew() {
	t := &exampleTB{}
	defer t.close()
	ctx := context.Background()

	s := qobuzdltest.New(t)
	c, err := qobuzdl.NewClient(ctx, s.ClientOptions())
	if err != nil {
		panic(err)
	}
	album, err := c.GetAlbumMeta(ctx, qobuzdltest.AlbumMultiDisc)
	if err != nil {
		panic(err)
	}
	fmt.Println(album.Title, len(album.Tracks))

	// The fake answers a downgrade, and logs what the client asked.
	s.Restrict(qobuzdltest.TrackSingle, 7)
	u, err := c.GetTrackURL(ctx, qobuzdltest.TrackSingle, 7, false)
	if err != nil {
		panic(err)
	}
	fmt.Println(u.FormatID, u.Restrictions)
	last := s.Requests()[len(s.Requests())-1]
	fmt.Println(last.Path, last.Query.Get("format_id"), last.Status)

	// Output:
	// Ummagumma 16
	// 6 [FormatRestrictedByFormatAvailability]
	// /api.json/0.2/track/getFileUrl 7 200
}
