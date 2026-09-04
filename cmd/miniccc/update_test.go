// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sandia-minimega/minimega/v2/internal/ron"
)

type restartCall struct {
	path, update string
	data         []byte
	perm         os.FileMode
}

func newTestUpdater(t *testing.T, perm os.FileMode) (*updater, string, *[]restartCall) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "miniccc")
	if err := os.WriteFile(path, []byte("old"), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}

	var calls []restartCall
	u := &updater{
		executable: func() (string, error) { return path, nil },
		restart: func(path, update string) error {
			data, err := os.ReadFile(update)
			if err != nil {
				return err
			}
			fi, err := os.Stat(update)
			if err != nil {
				return err
			}
			calls = append(calls, restartCall{path, update, data, fi.Mode().Perm()})
			return nil
		},
	}

	return u, path, &calls
}

func TestUpdateKeepsInstalledPermissions(t *testing.T) {
	u, path, calls := newTestUpdater(t, 0755)

	chunks := []*ron.File{
		{Name: "miniccc", Perm: 0644, Data: []byte("new "), Offset: 0},
		{Name: "miniccc", Perm: 0644, Data: []byte("binary"), Offset: 4, EOF: true},
	}
	for _, f := range chunks {
		if err := u.handle(f); err != nil {
			t.Fatal(err)
		}
	}

	if len(*calls) != 1 {
		t.Fatalf("got %d restarts, want 1", len(*calls))
	}
	got := (*calls)[0]
	if got.path != path || got.update != updateTempPath(path) {
		t.Errorf("restart(%q, %q), want restart(%q, %q)", got.path, got.update, path, updateTempPath(path))
	}
	if string(got.data) != "new binary" {
		t.Errorf("staged data = %q, want %q", got.data, "new binary")
	}
	if got.perm != 0755 {
		t.Errorf("staged permissions = %v, want %v", got.perm, os.FileMode(0755))
	}
}

func TestUpdateWriteFailureDiscardsTransfer(t *testing.T) {
	u, path, calls := newTestUpdater(t, 0755)

	if err := u.handle(&ron.File{Data: []byte("new"), Offset: 0}); err != nil {
		t.Fatal(err)
	}
	if err := u.handle(&ron.File{Data: []byte("bad"), Offset: -1}); err == nil {
		t.Fatal("negative offset write succeeded, want error")
	}
	if err := u.handle(&ron.File{Offset: 3, EOF: true}); err == nil {
		t.Fatal("EOF after a failed chunk succeeded, want error")
	}

	if len(*calls) != 0 {
		t.Fatalf("restart ran %d times after a failed chunk", len(*calls))
	}
	if _, err := os.Stat(updateTempPath(path)); !os.IsNotExist(err) {
		t.Errorf("staged file still exists after a failed chunk: %v", err)
	}
}

func TestUpdateServerErrorDiscardsTransfer(t *testing.T) {
	u, path, calls := newTestUpdater(t, 0755)

	if err := u.handle(&ron.File{Data: []byte("new"), Offset: 0}); err != nil {
		t.Fatal(err)
	}
	u.abort()
	if err := u.handle(&ron.File{Offset: 3, EOF: true}); err == nil {
		t.Fatal("EOF after an aborted transfer succeeded, want error")
	}

	if len(*calls) != 0 {
		t.Fatalf("restart ran %d times after an aborted transfer", len(*calls))
	}
	if _, err := os.Stat(updateTempPath(path)); !os.IsNotExist(err) {
		t.Errorf("staged file still exists after an aborted transfer: %v", err)
	}
}

func TestInstallUpdateRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "miniccc")
	update := updateTempPath(path)
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(update, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}

	restore, err := installUpdate(path, update)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("installed binary = %q, want %q", got, "new")
	}

	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("restored binary = %q, want %q", got, "old")
	}
}
