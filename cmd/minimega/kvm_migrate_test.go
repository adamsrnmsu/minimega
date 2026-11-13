// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package main

import "testing"

func TestMigrateProgress(t *testing.T) {
	ram := func(total, transferred float64) map[string]interface{} {
		return map[string]interface{}{"total": total, "transferred": transferred}
	}

	cases := []struct {
		name    string
		r       map[string]interface{}
		status  string
		percent float64
		wantErr bool
	}{
		{"no migration", map[string]interface{}{}, "", 0, false},
		// QEMU reports "setup" with no ram section right after a migrate starts;
		// "vm save" polled at that moment used to panic minimega.
		{"setup without ram", map[string]interface{}{"status": "setup"}, "setup", 0, false},
		{"active", map[string]interface{}{"status": "active", "ram": ram(200, 50)}, "active", 25, false},
		{"active without ram", map[string]interface{}{"status": "active"}, "active", 0, true},
		{"active with partial ram", map[string]interface{}{"status": "active", "ram": map[string]interface{}{"total": 100.0}}, "active", 0, true},
		{"active with zero total", map[string]interface{}{"status": "active", "ram": ram(0, 0)}, "active", 0, true},
		{"completed", map[string]interface{}{"status": "completed"}, "completed", 100, false},
		{"failed", map[string]interface{}{"status": "failed"}, "failed", 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, percent, err := migrateProgress(c.r)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error: %v", err, c.wantErr)
			}
			if status != c.status || percent != c.percent {
				t.Errorf("got (%q, %v), want (%q, %v)", status, percent, c.status, c.percent)
			}
		})
	}
}
