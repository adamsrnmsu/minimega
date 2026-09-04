// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestartUpdatedKeepsBinaryWhenExecFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "miniccc")
	update := updateTempPath(path)
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	// Not an ELF file and no "#!" line, so execve fails with ENOEXEC.
	if err := os.WriteFile(update, []byte{0, 1, 2, 3}, 0755); err != nil {
		t.Fatal(err)
	}

	if err := restartUpdated(path, update); err == nil {
		t.Fatal("restartUpdated succeeded with an invalid binary")
	}

	if got, err := os.ReadFile(path); err != nil || string(got) != "old" {
		t.Fatalf("installed binary = %q, %v; want %q", got, err, "old")
	}
}
