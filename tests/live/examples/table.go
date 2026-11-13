// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

// Package examples runs the downloadable .mm examples from doc/content as a
// live test group, and checks offline that the table below covers every one.
// See tests/live/README.md.
package examples

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// docRoot is the doc/content directory: LIVE_DOC_ROOT, or the one next to
// this source file (the test binary may run from any directory).
var docRoot = func() string {
	if d := os.Getenv("LIVE_DOC_ROOT"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "doc", "content")
}()

// Kind says how an example is tested.
type Kind int

const (
	// Standalone examples run as written, in an empty namespace.
	Standalone Kind = iota
	// Fragment examples assume state created by earlier steps; Setup names
	// the state (see Setups).
	Fragment
	// Exempt examples are not run; Reason says why.
	Exempt
)

// LineRule picks lines of an example: any command line containing Match.
type LineRule struct {
	Match  string
	Reason string
}

// Example is one doc/content .mm file and how the live test treats it.
type Example struct {
	Path   string     // relative to doc/content
	Kind   Kind       //
	Setup  string     // Fragment: key of Setups
	Reason string     // Exempt: why it cannot run
	Skip   []LineRule // lines that are not run
	Errors []LineRule // lines that are expected to fail (documentation bugs or placeholders)
	// CCResponses: after the example, every "cc exec" it issued must have a response.
	CCResponses bool
	// Check names an extra assertion in the live test (a key of Checks).
	Check string
}

// Setup is the state a Fragment assumes. Lines run first, through the same
// runner as the example (so kvm launches get the same overrides). A line
// "@path" runs that doc example instead.
type Setup struct {
	Lines []string
	// Running waits for every VM to be RUNNING; CC additionally waits for a
	// miniccc client per VM, stages the files the cc examples send and
	// issues a few commands so "cc responses N" has something to show.
	Running, CC bool
	// After runs once the VMs are up (and cc is staged).
	After []string
}

const fooBar = "vm launch kvm foo\nvm launch kvm bar\nvm start all"

// Setups are the named states fragments start from.
var Setups = map[string]Setup{
	"vms-built":        {Lines: strings.Split("vm launch kvm foo\nvm launch kvm bar[1-5]", "\n")},
	"vms-running":      {Lines: strings.Split("vm launch kvm foo\nvm launch kvm bar[1-5]\nvm start all", "\n"), Running: true},
	"vnc-fb-recording": {Lines: strings.Split(fooBar, "\n"), Running: true, After: []string{"vnc record fb bar recording.fb"}},
	"vnc-kb-recording": {Lines: strings.Split(fooBar, "\n"), Running: true, After: []string{"vnc record kb bar recording.kb"}},
	"vms-foo-bar":      {Lines: strings.Split(fooBar, "\n"), Running: true},
	"cc-clients":       {Lines: strings.Split(fooBar, "\n"), Running: true, CC: true},
	"router-foo": {Lines: []string{
		"vm config net a b c", "vm launch kvm foo", "vm start all"}, Running: true},
	"sandwich":         {Lines: []string{"@training/module01_content/vm_sandwich_p1.mm", "@training/module01_content/vm_sandwich_p2.mm"}},
	"sandwich-running": {Lines: []string{"@training/module01_content/vm_sandwich_p1.mm", "@training/module01_content/vm_sandwich_p2.mm", "@training/module01_content/vm_sandwich_p3.mm"}, Running: true, CC: true},
	"mirror":           {Lines: []string{"@training/module11_content/mirror_01.mm"}},
}

// Checks are the extra assertions Example.Check may name; the live test
// implements each one.
var Checks = map[string]bool{"dnsmasq-leases": true}

func std(path string) Example              { return Example{Path: path} }
func frag(path, setup string) Example      { return Example{Path: path, Kind: Fragment, Setup: setup} }
func exempt(path, why string) Example      { return Example{Path: path, Kind: Exempt, Reason: why} }
func (e Example) cc() Example              { e.CCResponses = true; return e }
func (e Example) skip(m, r string) Example { e.Skip = append(e.Skip, LineRule{m, r}); return e }
func (e Example) fails(m, r string) Example {
	e.Errors = append(e.Errors, LineRule{m, r})
	return e
}
func (e Example) check(name string) Example { e.Check = name; return e }

const (
	containers = "containers need cgroup v2 support the runner's minimega container does not have; kvm launches are covered"
	guestLine  = "runs inside the guest (documented for the reader), not a minimega command"
	hostMutate = "changes the CI host's firewall/sysctl, outside the experiment"
)

// Table has one entry per example under doc/content (except releases/ and
// presentations/). The meta test (sync_test.go) fails when it drifts from the
// docs.
var Table = []Example{
	// --- articles/cc_content
	frag("articles/cc_content/mounts.mm", "cc-clients"),
	frag("articles/cc_content/plumber.mm", "cc-clients"),
	frag("articles/cc_content/process.mm", "cc-clients").cc(),
	frag("articles/cc_content/tunnels.mm", "cc-clients"),

	// --- articles/tutorials/cc
	frag("articles/tutorials/cc/clients.mm", "cc-clients"),
	frag("articles/tutorials/cc/exec.mm", "cc-clients").cc(),
	frag("articles/tutorials/cc/exec2.mm", "cc-clients"),
	frag("articles/tutorials/cc/filter.mm", "cc-clients"),
	frag("articles/tutorials/cc/prefix.mm", "cc-clients").cc(),
	frag("articles/tutorials/cc/prefix2.mm", "cc-clients"),
	frag("articles/tutorials/cc/process.mm", "cc-clients"),
	frag("articles/tutorials/cc/recv.mm", "cc-clients").cc(),
	frag("articles/tutorials/cc/recv2.mm", "cc-clients"),
	frag("articles/tutorials/cc/recv3.mm", "cc-clients"),
	frag("articles/tutorials/cc/recv4.mm", "cc-clients"),
	frag("articles/tutorials/cc/send.mm", "cc-clients"),
	frag("articles/tutorials/cc/send2.mm", "cc-clients").cc(),
	frag("articles/tutorials/cc/send3.mm", "cc-clients"),

	// --- articles/vmtypes, articles/vnc
	std("articles/vmtypes/example1.mm"),
	frag("articles/vnc/vnc1.mm", "vms-foo-bar"),
	frag("articles/vnc/vnc10.mm", "vms-foo-bar").
		fails("vnc step", "needs a keyboard playback in progress, which the fragment cannot keep running"),
	frag("articles/vnc/vnc11.mm", "vms-foo-bar"),
	frag("articles/vnc/vnc12.mm", "vms-foo-bar").
		skip("vnc inject foo LoadFile", "blocks forever when foo has no running playback (the example assumes one, and bar.kbr does not exist)"),
	frag("articles/vnc/vnc2.mm", "vnc-fb-recording"),
	frag("articles/vnc/vnc3.mm", "vms-foo-bar"),
	frag("articles/vnc/vnc4.mm", "vnc-kb-recording"),
	frag("articles/vnc/vnc5.mm", "vms-foo-bar"),
	frag("articles/vnc/vnc6.mm", "vms-foo-bar").
		fails("vnc stop bar", "needs a keyboard playback in progress, which the fragment cannot keep running"),
	frag("articles/vnc/vnc7.mm", "vms-foo-bar").
		fails("vnc pause", "needs a keyboard playback in progress, which the fragment cannot keep running"),
	frag("articles/vnc/vnc8.mm", "vms-foo-bar").
		fails("vnc continue", "needs a keyboard playback in progress, which the fragment cannot keep running"),
	frag("articles/vnc/vnc9.mm", "vms-foo-bar").
		fails("vnc getstep", "needs a keyboard playback in progress, which the fragment cannot keep running"),

	// --- training/module01
	std("training/module01_content/vm_sandwich_p1.mm"),
	std("training/module01_content/vm_sandwich_p2.mm"),
	frag("training/module01_content/vm_sandwich_p3.mm", "sandwich"),
	frag("training/module01_content/vm_sandwich_p4.mm", "sandwich-running"),
	frag("training/module01_content/vm_sandwich_p5.mm", "sandwich-running"),
	std("training/module01_content/vmconfig.mm"),

	// --- training/module02
	exempt("training/module02_content/dot.mm", "a transcript of a terminal session (prompts and table output), not a command script"),
	exempt("training/module02_content/help.mm", "a transcript of `help` output, not a command script"),
	exempt("training/module02_content/nuke.mm", "`nuke` kills every QEMU and dnsmasq process and tap on the host, including the test deployment's own"),

	// --- training/module03
	std("training/module03_content/disk.mm"),
	std("training/module03_content/flush.mm"),
	std("training/module03_content/launch_c.mm").
		skip("vm launch container", containers),
	std("training/module03_content/launch.mm"),
	frag("training/module03_content/mdk.mm", "vms-running"),
	std("training/module03_content/simple.mm").
		skip("vm launch container", containers).
		skip("vm config filesystem", containers),
	frag("training/module03_content/start.mm", "vms-built"),
	std("training/module03_content/vminfo.mm"),

	// --- training/module04
	exempt("training/module04_content/mw_help.mm", "the output of `miniweb -help`, not a command script"),

	// --- training/module06
	std("training/module06_content/dnsmasq.mm").check("dnsmasq-leases"),
	std("training/module06_content/nat.mm").
		skip("shell sysctl", hostMutate).
		skip("shell iptables", hostMutate).
		skip("ip addr add", guestLine).
		skip("ip route add", guestLine),
	frag("training/module06_content/router01.mm", "router-foo"),
	frag("training/module06_content/router02.mm", "router-foo"),
	frag("training/module06_content/router03.mm", "router-foo"),
	frag("training/module06_content/router04.mm", "router-foo"),
	frag("training/module06_content/router05.mm", "router-foo"),
	frag("training/module06_content/router06.mm", "router-foo"),
	frag("training/module06_content/router07.mm", "router-foo").
		skip("* ", "documentation bug: markdown bullets ('* Or to specify...') pasted into the example are not commands"),
	frag("training/module06_content/router08.mm", "router-foo"),

	// --- training/module07
	frag("training/module07_content/cc01.mm", "cc-clients"),
	frag("training/module07_content/cc02.mm", "cc-clients"),
	frag("training/module07_content/cc03.mm", "cc-clients").cc(),
	frag("training/module07_content/cc04.mm", "cc-clients"),
	frag("training/module07_content/cc05.mm", "cc-clients").
		fails("cc process kill <pid>", "<pid> is a placeholder for the reader to fill in"),
	frag("training/module07_content/cc06.mm", "cc-clients"),
	frag("training/module07_content/cc07.mm", "cc-clients").
		fails("cc send /somedirectory/*", "/somedirectory is a placeholder; cc send only reads from the files directory"),
	frag("training/module07_content/cc08.mm", "cc-clients").cc(),
	frag("training/module07_content/cc09.mm", "cc-clients").
		skip("clear cc mount [", "[uuid or name or path] is a placeholder for the reader to fill in"),
	frag("training/module07_content/cc10.mm", "cc-clients").
		fails("cc rtunnel 4444 myserver 80", "myserver is a placeholder; rtunnel needs the name of an existing VM"),

	// --- training/module11
	std("training/module11_content/mirror_01.mm"),
	frag("training/module11_content/mirror_02.mm", "mirror").cc(),
}

// CommandLines returns the lines of an example that minimega would run:
// everything but blank lines and comments.
func CommandLines(content string) []string {
	var out []string
	for _, l := range strings.Split(content, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
