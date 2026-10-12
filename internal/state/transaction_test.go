package state

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestTransactionCanceledBeforeBeginPreservesCancellation(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := s.transaction(ctx, func(*sql.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled transaction began: called=%v err=%v", called, err)
	}
}

func TestTransactionBodyErrorAndCancellationAreBothPreserved(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bodyErr := errors.New("fixture body operation failed")
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,payload) VALUES('rolled-back','{}')"); err != nil {
			t.Fatal(err)
		}
		cancel()
		return bodyErr
	})
	if !errors.Is(err, bodyErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("body failure lost its cause: %v", err)
	}
	if _, err := s.Session(context.Background(), "rolled-back"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("body failure committed data: %v", err)
	}
}

func TestTransactionAutoRollbackBeforeCommitPreservesCancellationAndSQLError(t *testing.T) {
	s, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,payload) VALUES('rolled-back','{}')"); err != nil {
			t.Fatal(err)
		}
		cancel()
		// The store has exactly one connection. Ping cannot acquire it until
		// database/sql completes the canceled transaction's automatic rollback.
		// This forces ErrTxDone at Commit without timing sleeps or polling.
		waitCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := s.db.PingContext(waitCtx); err != nil {
			t.Fatalf("automatic rollback did not release the connection: %v", err)
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("commit failure lost cancellation or SQLite lifecycle error: %v", err)
	}
	if _, err := s.Session(context.Background(), "rolled-back"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("canceled transaction committed data: %v", err)
	}
}

func TestSuccessfulTransactionDoesNotReportAnError(t *testing.T) {
	s, _ := testStore(t)
	err := s.transaction(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO sessions(id,payload) VALUES('committed','{}')")
		return err
	})
	if err != nil {
		t.Fatalf("successful transaction reported failure: %v", err)
	}
	if _, err := s.Session(context.Background(), "committed"); err != nil {
		t.Fatalf("successful transaction lost committed data: %v", err)
	}
}
