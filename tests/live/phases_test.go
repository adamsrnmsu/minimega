// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package live

import (
	"fmt"
	"testing"
)

// This file has no build tag so its unit tests run without a deployment.

// phase is one ordered step of TestLive. It runs only when every phase it
// needs has passed, so one root cause produces one failure and phases that do
// not depend on it still run.
type phase struct {
	name  string
	fn    func(*testing.T)
	needs []string
}

// missingNeed returns the first of needs that has not passed, or "".
func missingNeed(needs []string, passed map[string]bool) string {
	for _, n := range needs {
		if !passed[n] {
			return n
		}
	}
	return ""
}

// checkPhases rejects a phase that needs a phase which does not run before it.
func checkPhases(phases []phase) error {
	earlier := map[string]bool{}
	for _, p := range phases {
		for _, n := range p.needs {
			if !earlier[n] {
				return fmt.Errorf("phase %v needs %v, which does not run before it", p.name, n)
			}
		}
		earlier[p.name] = true
	}
	return nil
}

// runPhases runs phases in order as subtests of t.
func runPhases(t *testing.T, phases []phase) {
	if err := checkPhases(phases); err != nil {
		t.Fatal(err)
	}
	passed := map[string]bool{}
	for _, p := range phases {
		ran, completed := false, false
		ok := t.Run(p.name, func(t *testing.T) {
			ran = true
			if n := missingNeed(p.needs, passed); n != "" {
				t.Skipf("skipped: needs %v, which did not pass", n)
			}
			p.fn(t)
			completed = true
		})
		// a skipped or Fatal phase never completes; an Error one is not ok.
		// A phase that -test.run filtered out never ran: do not hold back the
		// phases that were selected (e.g. ClearNamespace alone at teardown).
		passed[p.name] = !ran || (ok && completed)
	}
}
