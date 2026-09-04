// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package main

import (
	"strconv"
	"sync"
	"testing"

	"github.com/sandia-minimega/minimega/v2/internal/ron"
	"github.com/sandia-minimega/minimega/v2/pkg/minicli"
)

func TestCCAutoUpdateGetterParses(t *testing.T) {
	c, err := minicli.Compile("cc auto-update")
	if err != nil {
		t.Fatalf("cc auto-update does not parse: %v", err)
	}
	if !c.BoolArgs["auto-update"] {
		t.Fatalf("cc auto-update matched the wrong pattern: %v", c.BoolArgs)
	}
}

func TestCCAutoUpdateConcurrentRequests(t *testing.T) {
	ns := &Namespace{Name: "test", ccServer: &ron.Server{}}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(enable bool) {
			defer wg.Done()

			arg := "false"
			if enable {
				arg = "true"
			}
			c := &minicli.Command{BoolArgs: map[string]bool{"auto-update": true, arg: true}}
			if err := cliCCAutoUpdate(ns, c, &minicli.Response{}); err != nil {
				t.Error(err)
			}

			resp := &minicli.Response{}
			if err := cliCCAutoUpdate(ns, &minicli.Command{BoolArgs: map[string]bool{"auto-update": true}}, resp); err != nil {
				t.Error(err)
			}
		}(i%2 == 0)
	}
	wg.Wait()

	resp := &minicli.Response{}
	if err := cliCCAutoUpdate(ns, &minicli.Command{BoolArgs: map[string]bool{"auto-update": true}}, resp); err != nil {
		t.Fatal(err)
	}
	if want := strconv.FormatBool(ns.ccServer.AutoUpdate()); resp.Response != want {
		t.Fatalf("cc auto-update = %q, want %q from the server", resp.Response, want)
	}
}
