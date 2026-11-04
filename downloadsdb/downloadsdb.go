// Ports qobuz_dl/db.py from qobuz-dl 2.5.9: create_db and
// handle_download_id. get_stats is not ported.

// Package downloadsdb is qobuz-dl's downloads database in SQLite: the table
// Python's db.py creates, read and written with the same SQL, so Python and
// Go can share one qobuz_dl.db. It implements qobuzdl.DownloadsDB.
package downloadsdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// createTable is db.py's CREATE TABLE, whitespace included, since SQLite keeps
// the statement's text in sqlite_master and it must match a table Python made.
const createTable = `CREATE TABLE downloads (
                  "id" text NOT NULL,
                  "media_type" text NOT NULL DEFAULT 'album',
                  "quality" integer NOT NULL DEFAULT 27,
                  "file_format" text NOT NULL DEFAULT 'FLAC',
                  "quality_met" integer NOT NULL DEFAULT 0,
                  "bit_depth" text,
                  "sampling_rate" text,
                  "saved_path" text NOT NULL DEFAULT '',
                  "status" text NOT NULL DEFAULT 'downloaded',
                  "url" text NOT NULL DEFAULT '',
                  "release_date" text NOT NULL DEFAULT '',
                  "artist" text NOT NULL DEFAULT '',
                  "album" text NOT NULL DEFAULT '',
                  PRIMARY KEY ("id", "quality")
                );`

const insertRow = `INSERT INTO downloads (id, media_type, quality, file_format, quality_met, bit_depth,
                    sampling_rate, saved_path, url, release_date, status, artist, album) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// busyTimeout is Python sqlite3's default timeout of 5 seconds, so a Go and
// a Python process wait for each other's locks alike.
const busyTimeout = "_pragma=busy_timeout(5000)"

// DB is an open downloads database. It is safe for concurrent use.
type DB struct {
	db *sql.DB
}

var _ qobuzdl.DownloadsDB = (*DB)(nil)

// Open ports create_db: it opens or creates the file, creates the table when
// it is missing, and migrates the two older layouts as Python does. The
// journal mode is left as it is.
func Open(ctx context.Context, path string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	if err := createDB(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("downloadsdb: %s: %w", path, err)
	}
	return &DB{db: db}, nil
}

// dsn is a SQLite URI for path, so that a "?" or "#" in a file name stays
// part of the name.
func dsn(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	p := r.Replace(path)
	if strings.HasPrefix(p, "/") {
		p = "//" + p
	}
	return "file:" + p + "?" + busyTimeout
}

// createDB is create_db's body. Python runs the v1 migration's statements
// one by one, partly outside a transaction; one transaction here leaves the
// same table when it succeeds and the old one when it fails.
func createDB(ctx context.Context, db *sql.DB) error {
	var n int
	err := db.QueryRowContext(ctx, "SELECT count(name) FROM sqlite_master WHERE type='table' AND name='downloads'").Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		// Python ignores an OperationalError here, which a table made by
		// another process in between would raise.
		if _, err := db.ExecContext(ctx, createTable); err != nil && !isCode(err, sqlite3.SQLITE_ERROR) {
			return err
		}
		return nil
	}
	cols, err := columns(ctx, db)
	if err != nil {
		return err
	}
	switch {
	case !cols["quality"]:
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, q := range []string{"ALTER TABLE downloads RENAME TO downloads_old", createTable} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		// Python logs a failed copy and goes on to drop the old table.
		if _, err := tx.ExecContext(ctx, "SAVEPOINT copy"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO downloads (id) SELECT id FROM downloads_old"); err != nil {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO copy"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "RELEASE copy"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE downloads_old"); err != nil {
			return err
		}
		return tx.Commit()
	case !cols["artist"]:
		// Python logs a failure and goes on; the second column is added
		// only after the first.
		if _, err := db.ExecContext(ctx, "ALTER TABLE downloads ADD COLUMN artist text NOT NULL DEFAULT ''"); err == nil {
			_, _ = db.ExecContext(ctx, "ALTER TABLE downloads ADD COLUMN album text NOT NULL DEFAULT ''")
		}
	}
	return nil
}

// columns is the set of the table's column names, from PRAGMA table_info.
func columns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(downloads)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// Has ports handle_download_id without add_id: a row with the ID and the
// quality, whatever its media type.
func (db *DB) Has(ctx context.Context, mediaType, id string, quality int) (bool, error) {
	_ = mediaType // Python's query doesn't read it
	var got any
	err := db.db.QueryRowContext(ctx, "SELECT id FROM downloads WHERE id=? AND quality=?", id, quality).Scan(&got)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("downloadsdb: %w", err)
	}
	return true, nil
}

// Add ports handle_download_id with add_id. A row that breaks a constraint,
// such as one already there, is not an error: Python logs "Already in the
// database" for any IntegrityError.
func (db *DB) Add(ctx context.Context, rec qobuzdl.DownloadRecord) error {
	met := 0
	if rec.QualityMet {
		met = 1
	}
	_, err := db.db.ExecContext(ctx, insertRow, rec.ID, rec.MediaType, rec.Quality, rec.FileFormat, met,
		nullable(rec.BitDepth), nullable(rec.SamplingRate), rec.SavedPath, rec.URL, rec.ReleaseDate, rec.Status,
		rec.Artist, rec.Album)
	if err != nil && !isCode(err, sqlite3.SQLITE_CONSTRAINT) {
		return fmt.Errorf("downloadsdb: %w", err)
	}
	return nil
}

// Close closes the database.
func (db *DB) Close() error { return db.db.Close() }

func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// isCode reports whether err is a SQLite error of the primary result code.
func isCode(err error, code int) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == code
}
