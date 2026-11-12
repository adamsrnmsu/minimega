// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package live

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
)

// This file has no build tag so its unit tests run without a deployment.

var revisionRE = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// revisionOf returns the git revision in minimega's "version" output, or "".
func revisionOf(version string) string {
	return revisionRE.FindString(version)
}

// staleClient returns a copy of a miniccc binary that reports a different
// revision: every copy of rev is overwritten with a string of the same
// length, so the binary still runs but no longer matches the server.
func staleClient(bin []byte, rev string) ([]byte, error) {
	if rev == "" || !bytes.Contains(bin, []byte(rev)) {
		return nil, errors.New("binary does not carry the revision")
	}
	fake := strings.Repeat("0", len(rev))
	if fake == rev {
		fake = strings.Repeat("1", len(rev))
	}
	return bytes.ReplaceAll(bin, []byte(rev), []byte(fake)), nil
}

// clientProcs parses "ls -l /proc/*/exe" output into pid -> executable for
// the processes running miniccc. A replaced binary shows as "... (deleted)".
func clientProcs(ls string) map[string]string {
	procs := map[string]string{}
	for _, line := range strings.Split(ls, "\n") {
		link, exe, ok := strings.Cut(line, " -> ")
		if !ok || !strings.Contains(exe, "miniccc") {
			continue
		}
		f := strings.Split(strings.TrimSpace(link[strings.LastIndex(link, " ")+1:]), "/")
		if len(f) == 4 && f[1] == "proc" && f[3] == "exe" {
			procs[f[2]] = strings.TrimSpace(exe)
		}
	}
	return procs
}
