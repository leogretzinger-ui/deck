package db_test

// This file is package db_test — an EXTERNAL test package. It sees only the
// exported API, exactly as a real caller does, and it deliberately imports
// nothing but internal/db.
//
// Its job is to catch the class of bug where the package works in its own
// tests but not for anybody else. The sqlite3 driver must be registered by
// production code; if that import is ever removed from db.go, this fails with
// `sql: unknown driver "sqlite3"` while the in-package tests might not.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/db"
)

func TestOpenWorksForAnExternalCaller(t *testing.T) {
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("Open from an external package: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.VerifyWritable(context.Background()); err != nil {
		t.Errorf("VerifyWritable: %v", err)
	}

	var n int
	if err := d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Errorf("query through the exported reader: %v", err)
	}
}
