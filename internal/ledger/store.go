// Package ledger is ftop's record of who is using which card, and why.
//
// It never starts, stops, throttles or kills anything. Agents run their own
// programs; ftop records claims, chooses which idle cards a claim gets when the
// agent does not name them (see place), releases claims when their owner says so or
// when the declared time runs out and the card is observed idle, and answers "who
// holds this card".
//
// The store is one SQLite database in the user's state directory, in WAL mode. Every
// command opens it, reading or writing in one transaction, and exits — there is no
// daemon. The whole concurrency design is SQLite's: a single writer lock taken up
// front (see Open), so two agents claiming at once get one success and one "held"
// rather than a lock error. Agents reach the GPU hosts over ssh, so the machine the
// ledger lives on is the only one that can hold the authority, and pretending
// otherwise would buy nothing but distributed-consensus problems.
package ledger

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// SchemaVersion is bumped when the on-disk shape changes. A store from a newer
// ftop is refused rather than guessed at.
//
// 2 adds cards.present: whether the card was in its host's most recent COMPLETE
// observation. A card row is never deleted (its history is the point), so absence
// has to be recorded on the row or a card that left the fleet stays claimable
// forever.
//
// 3 adds claims.grp: the cards one claim command took, released and renewed
// together.
const SchemaVersion = 3

// Errors callers act on. They are values, not strings, so the CLI can map them to
// stable exit codes without matching on prose.
var (
	ErrHeld          = errors.New("card is already held")
	ErrNotFound      = errors.New("no such lease")
	ErrNotOwner      = errors.New("lease belongs to another agent")
	ErrInvalid       = errors.New("invalid request")
	ErrSchemaTooNew  = errors.New("ledger was written by a newer ftop")
	ErrNoSuchCard    = errors.New("no such card")
	ErrAlreadyClosed = errors.New("lease is already closed")
	ErrNoCapacity    = errors.New("not enough idle cards")
	ErrNotIdle       = errors.New("card is in use by work nobody claimed")
)

// Claim is one lease on one card.
//
// Granularity is deliberately coarse: a whole card, one owner at a time. The user
// was explicit that several processes may share a card — one agent running four
// instances — and that splitting VRAM is not this tool's business. So Card is the
// unit, and Instances is informational only.
type Claim struct {
	ID       string    `json:"id"`
	Host     string    `json:"host"`
	GPUUUID  string    `json:"gpu_uuid"`
	Owner    string    `json:"owner"` // declared agent identity, untrusted display string
	Session  string    `json:"session,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Cwd      string    `json:"cwd,omitempty"`
	Why      string    `json:"why"` // untrusted: never parsed, never executed
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Expires  time.Time `json:"expires"`
	Closed   bool      `json:"closed"`
	ClosedAt time.Time `json:"closed_at,omitempty"`
	// CloseCause is one of: released, expired, adopted, superseded.
	CloseCause string `json:"close_cause,omitempty"`
	// Directives are the operator's own instructions that produced this claim
	// ("use gpu-4", "take 2 cards"). They outrank any agent-to-agent negotiation.
	Directive string `json:"directive,omitempty"`
	// Group is shared by every card one claim command took. Release and renew
	// accept it, so a job on four cards is let go of in one step rather than four,
	// and cannot leave one card behind. Claims made before groups existed have none.
	Group string `json:"group,omitempty"`
}

// Store is the ledger database.
type Store struct {
	db   *sql.DB
	path string
}

// DefaultPath is where the ledger lives. It sits under the user's data dir, not
// the config dir: it is state, and losing it is losing history, not preferences.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "ftop", "ledger.db")
}

// Open opens (and, on first use, creates) the ledger.
//
// busy_timeout is not optional: a TUI refresh and a claim can overlap, and the
// alternative to waiting a few hundred milliseconds is a spurious failure on a
// perfectly ordinary read. WAL is what lets those reads proceed while a claim is
// being written.
//
// Two details here are load-bearing and each was measured:
//
//   - The DSN does NOT set journal_mode. A `_pragma` in the DSN runs when the
//     connection opens, before the rest, and switching a non-WAL file to WAL needs a
//     write lock; if another process holds one, the open fails with SQLITE_BUSY
//     before busy_timeout has been applied to the connection at all. Measured: a
//     reader opening a non-WAL file under a held write lock got
//     "database is locked" in 1.3 ms with busy_timeout already read back as 0. WAL is
//     a property of the FILE, so it is set once, after the connection is configured.
//   - _txlock=immediate makes every transaction take the write lock up front. A
//     SQLite transaction that starts as a reader and then writes does not consult
//     the busy handler on upgrade: it returns SQLITE_BUSY at once. Measured on 24
//     processes claiming at the same moment: deferred transactions gave 5 lock
//     failures out of 24, immediate gave none.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return nil, fmt.Errorf("ledger: cannot determine a store path")
	}
	// A missing parent directory is an error, not something to create.
	//
	// It used to be made on demand, so `-store /tmp/typodir/ledger.db` printed success
	// and a claim id while writing to a brand-new empty database: no cards, no history,
	// no collision protection. A mistyped path looked exactly like an empty fleet, and
	// the claim was recorded nowhere anyone would look for it. The default path's
	// directory is created by DefaultPath's own callers; an explicit -store must name a
	// directory that exists.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("ledger: %s does not exist (check -store)", filepath.Dir(path))
	}
	db, err := sql.Open("sqlite", path+
		"?_pragma=busy_timeout(5000)"+
		"&_txlock=immediate"+
		"&_pragma=foreign_keys(1)"+
		"&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	// A single connection serialises writes inside this process and removes the
	// "database is locked" retry loop SQLite's shared-cache mode would need.
	db.SetMaxOpenConns(1)
	if err := enableWAL(db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// enableWAL turns the file into a WAL database.
//
// It is idempotent and cheap: on a file that is already WAL, SQLite answers "wal"
// from the header without taking a write lock. On a fresh or non-WAL file it needs
// one, which is exactly the moment a second process may be opening the same ledger —
// so the failure is retried for as long as busy_timeout would have waited, and then
// reported rather than guessed at.
func enableWAL(db *sql.DB) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var mode string
		err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode)
		if err == nil {
			if !strings.EqualFold(mode, "wal") {
				return fmt.Errorf("ledger: could not enable WAL (journal_mode = %s)", mode)
			}
			return nil
		}
		if time.Now().After(deadline) || !isBusy(err) {
			return fmt.Errorf("ledger: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isBusy reports a lock contention error, which is worth retrying.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file, for diagnostics.
func (s *Store) Path() string { return s.path }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if v > SchemaVersion {
		return fmt.Errorf("%w (file version %d, this build knows %d)",
			ErrSchemaTooNew, v, SchemaVersion)
	}
	if v == SchemaVersion {
		return nil
	}
	// Migrations run in one write transaction with the version bump, and the version
	// is read again once the lock is held.
	//
	// Every agent on the machine runs a new build at once after an upgrade, so several
	// processes open an old ledger together. Run outside a transaction, two of them
	// both saw the old version and both added the column, and the loser failed on
	// "duplicate column"; an interruption between the ALTER and the version bump left
	// a file no build could open again.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if v >= SchemaVersion {
		return nil // another process migrated it while this one waited
	}
	// Migrations are additive and ordered. v1 created the original shape; v2 adds
	// cards.present; v3 adds claims.grp. A fresh database runs them all.
	for _, m := range []struct {
		to    int
		stmts []string
	}{{1, v1Schema}, {2, v2Schema}, {3, v3Schema}} {
		if v >= m.to {
			continue
		}
		// One statement per call: the driver does not run multi-statement strings.
		for _, q := range m.stmts {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("ledger: migrate to v%d: %w", m.to, err)
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

var v1Schema = []string{
	`CREATE TABLE IF NOT EXISTS claims (
		id          TEXT PRIMARY KEY,
		host        TEXT NOT NULL,
		gpu_uuid    TEXT NOT NULL,
		owner       TEXT NOT NULL,
		session     TEXT NOT NULL DEFAULT '',
		repo        TEXT NOT NULL DEFAULT '',
		branch      TEXT NOT NULL DEFAULT '',
		cwd         TEXT NOT NULL DEFAULT '',
		why         TEXT NOT NULL DEFAULT '',
		note        TEXT NOT NULL DEFAULT '',
		directive   TEXT NOT NULL DEFAULT '',
		created_ns  INTEGER NOT NULL,
		updated_ns  INTEGER NOT NULL,
		expires_ns  INTEGER NOT NULL,
		closed_ns   INTEGER NOT NULL DEFAULT 0,
		close_cause TEXT NOT NULL DEFAULT '',
		closed      INTEGER NOT NULL DEFAULT 0
	)`,
	// The one invariant the schema itself enforces: a card has at most one open
	// claim. Enforcing it here rather than in Go means a crash between the check
	// and the insert cannot book the same card twice.
	`CREATE UNIQUE INDEX IF NOT EXISTS claims_one_open_per_card
		ON claims(gpu_uuid) WHERE closed = 0`,
	`CREATE INDEX IF NOT EXISTS claims_by_host ON claims(host, closed)`,
	`CREATE INDEX IF NOT EXISTS claims_recent ON claims(updated_ns DESC)`,
	`CREATE TABLE IF NOT EXISTS cards (
		gpu_uuid   TEXT PRIMARY KEY,
		host       TEXT NOT NULL,
		idx        INTEGER NOT NULL,
		name       TEXT NOT NULL DEFAULT '',
		mem_total  INTEGER NOT NULL DEFAULT 0,
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		at_ns     INTEGER NOT NULL,
		kind      TEXT NOT NULL,
		claim_id  TEXT NOT NULL DEFAULT '',
		host      TEXT NOT NULL DEFAULT '',
		gpu_uuid  TEXT NOT NULL DEFAULT '',
		actor     TEXT NOT NULL DEFAULT '',
		detail    TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS events_recent ON events(at_ns DESC)`,
}

var v2Schema = []string{
	// Default 1: a row written by v1 was seen by the observation that created it,
	// and guessing "absent" for every pre-existing card would make the whole fleet
	// unclaimable on upgrade.
	`ALTER TABLE cards ADD COLUMN present INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE cards ADD COLUMN gone_since_ns INTEGER NOT NULL DEFAULT 0`,
}

var v3Schema = []string{
	// Empty for every earlier claim: those were taken one card at a time, and each is
	// still released by its own id.
	`ALTER TABLE claims ADD COLUMN grp TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS claims_by_group ON claims(grp) WHERE grp != ''`,
}

// sanitizeText makes a free-text field safe to store and display.
//
// The reason field is written by an agent, so it is attacker-controlled as far as
// any reader is concerned. Control characters (including the bidi overrides that
// can make "safe" look like "unsafe" on screen) are stripped here, once, rather than
// in each of the readers. It is stored as data and never enters a command line,
// a SQL string, or a shell, so this is about display integrity, not parsing.
func sanitizeText(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop other control characters
		case r >= 0x80 && r <= 0x9f:
			// C1 controls. ESC's single-byte range is only the ASCII half; U+009B is
			// the C1 CSI and U+009B-based sequences are honoured by some terminals
			// exactly as the ASCII form is.
		case unicode.Is(unicode.Cf, r), // format characters: bidi overrides, zero-width
			r >= 0x200b && r <= 0x200f,
			r >= 0x202a && r <= 0x202e,
			r >= 0x2066 && r <= 0x2069:
			// drop invisible directionality controls
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	return truncateRunes(out, limit)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	i := 0
	for off := range s {
		if i == n {
			return s[:off]
		}
		i++
	}
	return s
}

func nowNS() int64 { return time.Now().UnixNano() }

// newID is a short, sortable claim id: time plus a random suffix. Agents quote it in
// messages to each other and humans type it, so it has to be short; it also has to
// sort by creation time, because the ledger is read newest-first.
func newID(t time.Time) string { return stampedID("L", t) }

// newGroupID names the cards one claim command took. The G prefix is what lets
// release and renew tell a group from a single claim.
func newGroupID(t time.Time) string { return stampedID("G", t) }

// IsGroupID reports whether an id names a group rather than one claim.
func IsGroupID(id string) bool { return strings.HasPrefix(id, "G") }

func stampedID(prefix string, t time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A predictable id is not a security problem here (nothing is authorised by
		// knowing one), but it must still be unique.
		binary.BigEndian.PutUint32(b[:], uint32(t.UnixNano()))
	}
	return prefix + t.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

func fromNS(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}
