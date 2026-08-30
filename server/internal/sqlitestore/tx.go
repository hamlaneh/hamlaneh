package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// querier is the statement surface shared by *sql.DB and *sql.Tx, so a query
// can be written once and run either on its own connection or inside a
// caller's transaction. It is this package's counterpart to the pgx
// rowQuerier the PostgreSQL driver uses for the same reason.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ querier = (*sql.DB)(nil)
	_ querier = (*sql.Tx)(nil)
)

// withTx runs fn inside one transaction, committing when it returns nil and
// rolling back otherwise. It is the counterpart of pgx.BeginFunc.
//
// The transaction is a WRITE transaction from its first instruction: the DSN
// sets _txlock=immediate, so BEGIN takes the database's write lock rather
// than starting read-only and upgrading at the first write. That is what
// makes this driver's single-writer argument true — under an upgrading
// (deferred) transaction two writers can both hold read locks and then both
// ask to upgrade, which is a deadlock no busy timeout resolves. Here the
// second writer simply waits at BEGIN.
//
// It is also why every method that needs more than one statement to be atomic
// uses this rather than issuing the statements loose: the write lock held for
// the whole transaction IS the serialization the PostgreSQL driver spells out
// with row locks and advisory locks.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// rowsAffected reports how many rows a statement changed, turning the driver's
// own error into a wrapped one so a caller can treat the count as a fact.
func rowsAffected(res sql.Result) (int64, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
