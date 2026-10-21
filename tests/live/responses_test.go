// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

package live

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// This file has no build tag so its unit tests run without a deployment.

// ccResult is the outcome of one cc command on one VM.
type ccResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// noExitCode is the ExitCode of a response that recorded none: commands such
// as send and recv do not, and an exec response without one must not read as
// success.
const noExitCode = -1

// readResponses reads the per-client responses to one cc command from its
// miniccc_responses/<id> directory. byUUID names clients by VM; a client it
// does not know is keyed by its UUID. A command with no responses yet has no
// directory and gives an empty result.
func readResponses(dir string, byUUID map[string]string) (map[string]ccResult, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ccResult{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("read responses: %w", err)
	}

	res := map[string]ccResult{}
	for _, e := range entries {
		r, err := readResponse(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("response %v: %w", e.Name(), err)
		}
		name := byUUID[e.Name()]
		if name == "" {
			name = e.Name()
		}
		res[name] = r
	}
	return res, nil
}

// readResponse reads one client's response directory. Missing files are
// empty output and noExitCode.
func readResponse(path string) (ccResult, error) {
	read := func(f string) (string, error) {
		b, err := os.ReadFile(filepath.Join(path, f))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}

	r := ccResult{ExitCode: noExitCode}
	var code string
	var err error
	if r.Stdout, err = read("stdout"); err != nil {
		return r, err
	}
	if r.Stderr, err = read("stderr"); err != nil {
		return r, err
	}
	if code, err = read("exitcode"); err != nil {
		return r, err
	}
	if code = strings.TrimSpace(code); code != "" {
		if r.ExitCode, err = strconv.Atoi(code); err != nil {
			return r, fmt.Errorf("exit code %q: %w", code, err)
		}
	}
	return r, nil
}
