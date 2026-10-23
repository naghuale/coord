//go:build unix

package main

import (
	"context"
	"coord/assignment"
	"coord/checkpoint"
	"coord/internal/coorddconfig"
	"coord/roles"
	"coord/store"
	"coord/transport"
	"errors"
	"io"
	"testing"
)

func TestAssignmentFrozenModeConfiguredBeforeTransport(t *testing.T) {
	cfg := testConfig(t)
	cfg.AssignmentLedgerMode = "LEDGER_FROZEN"
	var canonical *store.CanonicalStore
	stop := errors.New("stop fixture before transport and listeners")
	err := runWithWriterFactories(context.Background(), cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		if canonical == nil {
			t.Fatal("missing canonical store")
		}
		if err := canonical.OfferAssignment(assignment.Principal{}, assignment.Record{}, "fixture"); !errors.Is(err, store.ErrLedgerFrozen) {
			t.Fatalf("startup did not apply frozen mode: %v", err)
		}
		return nil, nil, stop
	}, func(path string) (store.RunStore, error) {
		var err error
		canonical, err = store.NewCanonical(checkpoint.CanonicalFile{Path: path})
		return canonical, err
	}, roles.New, io.Discard)
	if !errors.Is(err, stop) {
		t.Fatalf("err=%v", err)
	}
}
