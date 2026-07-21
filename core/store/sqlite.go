package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
	_ "modernc.org/sqlite"
)

// schemaVersion is stamped into the database's PRAGMA user_version at open.
// SQLite gains it as a forward-compatibility guard: NewSQLite refuses to open
// a file written by a newer, unknown schema rather than corrupt it. There is
// no migration framework (ADR-0004 keeps the store a plain document table);
// bumping this is a deliberate, separate decision.
const schemaVersion = 1

// One JSON column per writer mirrors the Store contract's write separation
// (ADR-0004): Put owns metadata/references/spec, UpdateStatus owns status,
// MarkForDeletion owns metadata. Document-style, never per-type tables — the
// store stays ignorant of any resource shape. references_json is new since
// ADR-0004's row sketch (it predates the M1 references envelope, ADR-0012).
const createTableSQL = `
CREATE TABLE IF NOT EXISTS resources (
	kind            TEXT NOT NULL,
	name            TEXT NOT NULL,
	metadata_json   TEXT NOT NULL,
	references_json TEXT NOT NULL,
	spec_json       TEXT NOT NULL,
	status_json     TEXT NOT NULL,
	PRIMARY KEY (kind, name)
);`

const selectColumns = "metadata_json, references_json, spec_json, status_json"

// SQLite is the persistent Store implementation (ADR-0004). It swaps in behind
// the same interface as Memory with no caller changes; the shared conformance
// suite proves the two honour one behavioral spec.
type SQLite struct {
	db *sql.DB
}

// NewSQLite opens (creating if absent) the database at path and returns a
// ready Store. Pass ":memory:" for an ephemeral database (tests). The schema
// is applied idempotently; opening a database stamped with a newer
// schemaVersion is refused.
//
// The pool is pinned to a single connection. That serialises access — the
// store is a single-node sandbox component, not a throughput tier — and, with
// ":memory:", keeps every call talking to the same in-memory database (a
// private ":memory:" database lives only as long as its connection). The
// pragmas below still earn their place: WAL makes committed writes durable
// across a process kill -9 (the M2 crash-recovery story), and busy_timeout
// guards against a second process touching the same file.
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &SQLite{db: db}
	if err := s.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// pragmaQuery is the connection-pragma tail appended to every DSN. Keeping the
// pragmas in the DSN (not just an init-time Exec) applies them on every open,
// so they survive even if the pool were to reopen the connection.
const pragmaQuery = "?_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

// dsn builds a modernc DSN for path. The path is percent-encoded into the
// file: URI so a filename containing URI metacharacters (?, #, %, spaces —
// all legal on POSIX) can't truncate the URI or inject query parameters ahead
// of the pragma tail. ":memory:" is SQLite's ephemeral-database token, passed
// through as-is rather than encoded.
func dsn(path string) string {
	if path == ":memory:" {
		return "file::memory:" + pragmaQuery
	}
	return "file:" + url.PathEscape(path) + pragmaQuery
}

func (s *SQLite) init(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported %d", version, schemaVersion)
	}
	if _, err := s.db.ExecContext(ctx, createTableSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if version < schemaVersion {
		// user_version takes no bind parameters; the value is a trusted
		// in-process constant, never user input.
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return fmt.Errorf("stamp schema version: %w", err)
		}
	}
	return nil
}

// Close releases the underlying database handle.
func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Put(ctx context.Context, res *api.Resource) error {
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingMeta []byte
	err = tx.QueryRowContext(ctx,
		"SELECT metadata_json FROM resources WHERE kind = ? AND name = ?",
		res.Kind, res.Name).Scan(&existingMeta)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	meta := res.Metadata
	if exists {
		var prev api.Metadata
		if err := json.Unmarshal(existingMeta, &prev); err != nil {
			return fmt.Errorf("decode stored metadata: %w", err)
		}
		// Preserve CreatedAt and the deletion mark across upserts: re-applying
		// a Spec must not reset creation time or resurrect a resource already
		// being torn down (ADR-0004, ADR-0007).
		meta.CreatedAt = prev.CreatedAt
		meta.DeletedAt = prev.DeletedAt
	} else {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	refsJSON, err := json.Marshal(res.References)
	if err != nil {
		return err
	}
	specJSON := specColumn(res.Spec)

	if exists {
		// Put owns metadata/references/spec only; status_json is left
		// untouched so a re-applied Spec can never clobber reconciler state.
		_, err = tx.ExecContext(ctx,
			"UPDATE resources SET metadata_json = ?, references_json = ?, spec_json = ? WHERE kind = ? AND name = ?",
			metaJSON, refsJSON, specJSON, res.Kind, res.Name)
	} else {
		statusJSON, mErr := json.Marshal(res.Status)
		if mErr != nil {
			return mErr
		}
		_, err = tx.ExecContext(ctx,
			"INSERT INTO resources (kind, name, metadata_json, references_json, spec_json, status_json) VALUES (?, ?, ?, ?, ?, ?)",
			res.Kind, res.Name, metaJSON, refsJSON, specJSON, statusJSON)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Get(ctx context.Context, kind, name string) (*api.Resource, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+selectColumns+" FROM resources WHERE kind = ? AND name = ?",
		kind, name)
	return scanResource(row, kind, name)
}

func (s *SQLite) List(ctx context.Context, kind string) ([]*api.Resource, error) {
	query := "SELECT kind, name, " + selectColumns + " FROM resources"
	var args []any
	if kind != "" {
		query += " WHERE kind = ?"
		args = append(args, kind)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*api.Resource
	for rows.Next() {
		var k, n string
		res, err := scanResourceWithKey(rows, &k, &n)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateStatus(ctx context.Context, kind, name string, status api.Status) error {
	// Marshalling snapshots the caller's Status (including its Observed map),
	// so the stored value never aliases caller state — the copy-out invariant
	// Memory keeps via copyStatus.
	statusJSON, err := json.Marshal(status)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE resources SET status_json = ? WHERE kind = ? AND name = ?",
		statusJSON, kind, name)
	if err != nil {
		return err
	}
	return errNotFoundIfNoRows(res)
}

func (s *SQLite) MarkForDeletion(ctx context.Context, kind, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var metaJSON []byte
	err = tx.QueryRowContext(ctx,
		"SELECT metadata_json FROM resources WHERE kind = ? AND name = ?",
		kind, name).Scan(&metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var meta api.Metadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return fmt.Errorf("decode stored metadata: %w", err)
	}
	if !meta.DeletedAt.IsZero() {
		// Idempotent: an existing mark must not move.
		return tx.Commit()
	}
	meta.DeletedAt = time.Now().UTC()

	updated, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE resources SET metadata_json = ? WHERE kind = ? AND name = ?",
		updated, kind, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Delete(ctx context.Context, kind, name string) error {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM resources WHERE kind = ? AND name = ?", kind, name)
	if err != nil {
		return err
	}
	return errNotFoundIfNoRows(res)
}

// scanner is the read surface shared by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanResource decodes a row selected in selectColumns order for a known
// (kind, name), translating the empty-result case into ErrNotFound.
func scanResource(sc scanner, kind, name string) (*api.Resource, error) {
	var meta, refs, spec, status []byte
	if err := sc.Scan(&meta, &refs, &spec, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return buildResource(kind, name, meta, refs, spec, status)
}

// scanResourceWithKey decodes a List row, which carries its own kind and name.
func scanResourceWithKey(sc scanner, kind, name *string) (*api.Resource, error) {
	var meta, refs, spec, status []byte
	if err := sc.Scan(kind, name, &meta, &refs, &spec, &status); err != nil {
		return nil, err
	}
	return buildResource(*kind, *name, meta, refs, spec, status)
}

func buildResource(kind, name string, meta, refs, spec, status []byte) (*api.Resource, error) {
	res := &api.Resource{Kind: kind, Name: name, Spec: specValue(spec)}
	if err := json.Unmarshal(meta, &res.Metadata); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	if err := json.Unmarshal(refs, &res.References); err != nil {
		return nil, fmt.Errorf("decode references: %w", err)
	}
	if err := json.Unmarshal(status, &res.Status); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return res, nil
}

// specColumn normalises an empty Spec to a JSON null so the NOT NULL column
// always holds valid JSON (a nil bind would violate the constraint).
func specColumn(spec json.RawMessage) []byte {
	if len(spec) == 0 {
		return []byte("null")
	}
	return spec
}

// specValue inverts specColumn: the stored "null" placeholder reads back as a
// nil RawMessage, matching what Memory returns for a Spec-less resource.
func specValue(spec []byte) json.RawMessage {
	if string(spec) == "null" {
		return nil
	}
	return json.RawMessage(spec)
}

func errNotFoundIfNoRows(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
