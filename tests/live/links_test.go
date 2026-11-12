// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package live

import "strings"

// This file has no build tag so its unit tests run without a deployment.

// linksPresent returns the names that appear in lsNet, the output of
// `ls /sys/class/net`: the tap devices that still exist on the host.
func linksPresent(names []string, lsNet string) []string {
	have := map[string]bool{}
	for _, f := range strings.Fields(lsNet) {
		have[f] = true
	}
	var out []string
	for _, n := range names {
		if have[n] {
			out = append(out, n)
		}
	}
	return out
}
