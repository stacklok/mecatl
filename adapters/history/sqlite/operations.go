package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// Observe returns head and requested-ordinal evidence from one deferred read
// transaction without fetching payloads. Missing or initialized-empty histories
// yield zero evidence and do not create missing storage.
func (b *Backend) Observe(ctx context.Context, id history.HistoryID, sequence int64) (history.Observation, error) {
	ctx, done, err := b.admit(ctx, id)
	if err != nil {
		return history.Observation{}, err
	}
	defer done()
	if sequence < 0 {
		return history.Observation{}, history.ErrInvalid
	}
	db, err := b.database(ctx, id, false)
	if err != nil || db == nil {
		return history.Observation{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return history.Observation{}, fault(err)
	}
	defer func() { _ = tx.Rollback() }()
	o, err := observe(ctx, tx, sequence)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return history.Observation{}, fault(err)
	}
	return o, nil
}

// Append acquires BEGIN IMMEDIATE write authority before comparing heads. A
// mismatch returns coherent indexed evidence without fetching/copying input
// bytes. A matching proposal is submitted exactly once. Only successful COMMIT
// yields acceptance; its known success is never replaced by late cancellation.
func (b *Backend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, commit history.ByteCommit) (history.Observation, error) {
	ctx, done, err := b.admit(ctx, id)
	if err != nil {
		return history.Observation{}, err
	}
	defer done()
	if (expected != (history.CommitRef{}) && expected.Validate() != nil) || commit.Ref.Validate() != nil || expected.Sequence == math.MaxInt64 || commit.Ref.Sequence != expected.Sequence+1 || len(commit.Bytes) == 0 {
		return history.Observation{}, history.ErrInvalid
	}
	db, err := b.database(ctx, id, true)
	if err != nil {
		return history.Observation{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return history.Observation{}, fault(err)
	}
	defer func() { _ = tx.Rollback() }()
	o, err := observe(ctx, tx, commit.Ref.Sequence)
	if err != nil {
		return history.Observation{}, fault(err)
	}
	if o.Head != expected {
		if err = tx.Rollback(); err != nil {
			return history.Observation{}, fault(err)
		}
		return o, nil
	}
	cid := commit.Ref.CID.Binary()
	if _, err = tx.ExecContext(ctx, `INSERT INTO commits(seq,cid,payload) VALUES(?,?,?)`, commit.Ref.Sequence, cid[:], commit.Bytes); err != nil {
		return history.Observation{}, fault(err)
	}
	if err = tx.Commit(); err != nil {
		return history.Observation{}, fault(err)
	}
	return history.Observation{Head: commit.Ref, Commit: commit.Ref}, nil
}

// observe checks head and requested evidence in the same transaction. An absent
// ordinal at or below head is corrupt, not a fabricated zero observation.
func observe(ctx context.Context, tx *sql.Tx, sequence int64) (history.Observation, error) {
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM commits`).Scan(&head); err != nil {
		return history.Observation{}, err
	}
	if head < 0 {
		return history.Observation{}, history.ErrCorrupt
	}
	var o history.Observation
	if head == 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM commits LIMIT 1)`).Scan(&n); err != nil {
			return o, err
		}
		if n != 0 {
			return o, history.ErrCorrupt
		}
		return o, nil
	}
	var first int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM commits ORDER BY seq LIMIT 1`).Scan(&first); err != nil {
		return o, err
	}
	if first != 1 {
		return o, history.ErrCorrupt
	}
	ref, _, err := indexed(ctx, tx, head)
	if err != nil {
		return o, err
	}
	o.Head = ref
	if sequence == head {
		o.Commit = ref
	} else if sequence > 0 && sequence < head {
		o.Commit, _, err = indexed(ctx, tx, sequence)
		if errors.Is(err, sql.ErrNoRows) {
			err = history.ErrCorrupt
		}
	}
	return o, err
}

// indexed preflights types and lengths before fetching the fixed-size CID. It
// never fetches payload bytes, and returns sql.ErrNoRows for an absent ordinal.
func indexed(ctx context.Context, tx *sql.Tx, sequence int64) (history.CommitRef, int64, error) {
	var valid bool
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT typeof(seq)='integer' AND seq>0 AND typeof(cid)='blob' AND length(cid)=36 AND typeof(payload)='blob' AND length(payload)>0, CASE WHEN typeof(payload)='blob' THEN length(payload) ELSE 0 END FROM commits WHERE seq=?`, sequence).Scan(&valid, &size)
	if err != nil {
		return history.CommitRef{}, 0, err
	}
	if !valid {
		return history.CommitRef{}, 0, history.ErrCorrupt
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT cid FROM commits WHERE seq=?`, sequence).Scan(&raw); err != nil {
		return history.CommitRef{}, 0, err
	}
	cid, err := contentid.ParseBinary(raw)
	if err != nil {
		return history.CommitRef{}, 0, history.ErrCorrupt
	}
	return history.CommitRef{Sequence: sequence, CID: cid}, size, nil
}

func validByteReadRequest(r history.ByteReadRequest) bool {
	return (r.After == (history.CommitRef{}) || r.After.Validate() == nil) && r.Through.Validate() == nil && r.After.Sequence <= r.Through.Sequence && (r.After.Sequence != r.Through.Sequence || r.After == r.Through) && r.MaxBytes > 0 && r.MaxCommits > 0 && r.MaxCommitBytes > 0
}

func validateReadBoundaries(ctx context.Context, tx *sql.Tx, r history.ByteReadRequest) error {
	for _, boundary := range []history.CommitRef{r.After, r.Through} {
		if boundary == (history.CommitRef{}) {
			continue
		}
		ref, _, err := indexed(ctx, tx, boundary.Sequence)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && ref != boundary) {
			return history.ErrInvalid
		}
		if err != nil {
			return fault(err)
		}
	}
	return nil
}

func readPageRefs(ctx context.Context, tx *sql.Tx, r history.ByteReadRequest) ([]history.ByteCommit, error) {
	var page []history.ByteCommit
	remaining := int64(r.MaxBytes)
	// No allocation is based directly on the caller's potentially enormous limits.
	for seq := r.After.Sequence; seq < r.Through.Sequence && len(page) < r.MaxCommits; {
		seq++
		ref, size, err := indexed(ctx, tx, seq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, history.ErrCorrupt
		}
		if err != nil {
			return nil, fault(err)
		}
		if size > int64(r.MaxCommitBytes) {
			return nil, history.ErrInvalid
		}
		if size > remaining {
			if len(page) == 0 {
				return nil, history.PageTooSmallError{RequiredBytes: int(size)}
			}
			break
		}
		remaining -= size
		page = append(page, history.ByteCommit{Ref: ref})
	}
	return page, nil
}

// Read checks both indexed boundaries even at EOF or a prefix. Candidate types,
// lengths and caps are checked before fetching any governed payload; a later
// failure discards the entire attempted page. Output owns its exact byte storage.
func (b *Backend) Read(ctx context.Context, id history.HistoryID, r history.ByteReadRequest) ([]history.ByteCommit, error) {
	ctx, done, err := b.admit(ctx, id)
	if err != nil {
		return nil, err
	}
	defer done()
	if !validByteReadRequest(r) {
		return nil, history.ErrInvalid
	}
	db, err := b.database(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, history.ErrNotFound
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fault(err)
	}
	defer func() { _ = tx.Rollback() }()
	o, err := observe(ctx, tx, 0)
	if err != nil {
		return nil, fault(err)
	}
	if o.Head == (history.CommitRef{}) {
		return nil, history.ErrNotFound
	}
	if err = validateReadBoundaries(ctx, tx, r); err != nil {
		return nil, err
	}
	page, err := readPageRefs(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	for i := range page {
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM commits WHERE seq=?`, page[i].Ref.Sequence).Scan(&page[i].Bytes); err != nil {
			return nil, fault(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fault(err)
	}
	return page, nil
}
