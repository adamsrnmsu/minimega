// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package examples

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// legacy directories are not part of the live tests.
var legacy = []string{"releases/", "presentations/"}

func docExamples(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(docRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".mm" {
			return nil
		}
		rel, _ := filepath.Rel(docRoot, p)
		rel = filepath.ToSlash(rel)
		for _, l := range legacy {
			if strings.HasPrefix(rel, l) {
				return nil
			}
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %v: %v", docRoot, err)
	}
	sort.Strings(out)
	return out
}

// TestTableMatchesDocs fails when tests/live/examples/table.go and the .mm
// examples under doc/content disagree, so the live tests cannot drift from
// the docs.
func TestTableMatchesDocs(t *testing.T) {
	docs := docExamples(t)
	if len(docs) == 0 {
		t.Fatalf("no .mm examples found under %v", docRoot)
	}
	inDocs := map[string]bool{}
	for _, d := range docs {
		inDocs[d] = true
	}

	inTable := map[string]bool{}
	for _, e := range Table {
		if inTable[e.Path] {
			t.Errorf("%v is listed twice in Table (tests/live/examples/table.go); remove one entry", e.Path)
		}
		inTable[e.Path] = true
	}

	for _, d := range docs {
		if !inTable[d] {
			t.Errorf("doc example %v has no entry in tests/live/examples/table.go.\n"+
				"  Add one to Table, for example:\n"+
				"    std(%q),                      // runs as written in an empty namespace\n"+
				"    frag(%q, \"cc-clients\"),      // assumes state; pick a key of Setups\n"+
				"    exempt(%q, \"why it cannot run\"),",
				d, d, d, d)
		}
	}

	for _, e := range Table {
		where := fmt.Sprintf("Table entry %v (tests/live/examples/table.go)", e.Path)
		if !inDocs[e.Path] {
			t.Errorf("%v points at a file that is not a doc example under %v any more; "+
				"delete the entry or update its path if the example was renamed", where, docRoot)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(docRoot, e.Path))
		if err != nil {
			t.Errorf("%v: %v", where, err)
			continue
		}
		lines := CommandLines(string(raw))

		switch e.Kind {
		case Exempt:
			if strings.TrimSpace(e.Reason) == "" {
				t.Errorf("%v is exempt without a Reason; say why it cannot run in the live test", where)
			}
			if e.Setup != "" || len(e.Skip) > 0 || len(e.Errors) > 0 || e.CCResponses || e.Check != "" {
				t.Errorf("%v is exempt but also sets Setup, Skip, Errors or checks; an exempt example is not run", where)
			}
		case Fragment:
			if _, ok := Setups[e.Setup]; !ok {
				t.Errorf("%v names setup %q, which is not a key of Setups; known setups: %v",
					where, e.Setup, setupNames())
			}
		case Standalone:
			if e.Setup != "" {
				t.Errorf("%v is Standalone but names setup %q; use frag(...) for an example that assumes state", where, e.Setup)
			}
		}
		if e.Kind != Exempt && e.Reason != "" {
			t.Errorf("%v has a Reason but is not exempt; Reason is only for exempt(...)", where)
		}

		for kind, rules := range map[string][]LineRule{"Skip": e.Skip, "Errors": e.Errors} {
			for _, r := range rules {
				if strings.TrimSpace(r.Reason) == "" {
					t.Errorf("%v has a %v rule for %q without a Reason", where, kind, r.Match)
				}
				if !matchesAny(lines, r.Match) {
					t.Errorf("%v has a %v rule for %q, but no command line of %v contains it any more; "+
						"update or delete the rule (the example may have changed)", where, kind, r.Match, e.Path)
				}
			}
		}
		if e.Check != "" && !Checks[e.Check] {
			t.Errorf("%v names check %q, which is not a key of Checks", where, e.Check)
		}
	}

	for name, s := range Setups {
		for _, l := range s.Lines {
			if p, ok := strings.CutPrefix(l, "@"); ok && !inDocs[p] {
				t.Errorf("setup %q runs %v, which is not a doc example any more; update Setups in tests/live/examples/table.go", name, p)
			}
		}
	}
}

func matchesAny(lines []string, sub string) bool {
	if sub == "" {
		return false
	}
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func setupNames() []string {
	var n []string
	for k := range Setups {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
