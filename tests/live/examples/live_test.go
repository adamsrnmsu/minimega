// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

//go:build live

package examples

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sandia-minimega/minimega/v2/pkg/ranges"
	"github.com/sandia-minimega/minimega/v2/tests/live/harness"
)

func TestMain(m *testing.M) {
	if err := harness.Connect(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// baseNS is the namespace topology.mm creates; examples never touch it.
var baseNS = harness.Namespace

// TestExamples runs every doc example in its own namespace. The example's
// lines run over the command socket as written, except that each
// "vm launch kvm" is preceded by overrides that point the VM at the vmbetter
// live guest, with 384 MB, one vCPU and no disk or cdrom.
func TestExamples(t *testing.T) {
	for name := range Checks {
		if extraChecks[name] == nil {
			t.Fatalf("check %q is in Checks but has no implementation", name)
		}
	}
	for _, e := range Table {
		t.Run(e.Path, func(t *testing.T) {
			if e.Kind == Exempt {
				t.Skipf("exempt: %v", e.Reason)
			}
			runExample(t, e)
		})
	}
}

// extraChecks are per-example assertions named by Example.Check.
var extraChecks = map[string]func(*runner){
	// dnsmasq.mm: every VM on the dnsmasq-served VLAN learns a lease.
	"dnsmasq-leases": func(r *runner) {
		harness.Eventually(r.t, 3*time.Minute, 5*time.Second, func() error {
			n := 0
			for name, row := range r.vmInfo() {
				if strings.HasPrefix(name, "linux") && strings.Contains(row["ip"], "10.0.0.") {
					n++
				}
			}
			if n < 10 {
				return fmt.Errorf("%d of 10 VMs have a 10.0.0.x address", n)
			}
			return nil
		})
	},
}

// runner executes example lines and tracks what they configured.
type runner struct {
	t         *testing.T
	ns        string         // active namespace
	nets      int            // networks in the current "vm config net"
	kernel    string         // current "vm config kernel"
	appendSet bool           // we set "vm config append" for a router
	named     map[string]int // launched VM -> networks
	anon      int            // VMs launched by count
	life      string         // last of start, stop, kill
	flushed   bool
	failed    bool
}

func runExample(t *testing.T, e Example) {
	r := &runner{t: t, ns: "ex", named: map[string]int{}}
	t.Cleanup(r.cleanup)
	if err := r.exec("namespace ex"); err != nil {
		t.Fatal(err)
	}

	if e.Kind == Fragment {
		r.setup(e.Setup)
	}
	r.runFile(e.Path, &e)
	if r.failed {
		return
	}
	r.verify(e)
}

func (r *runner) switchNS(ns string) {
	r.ns = ns
	harness.Namespace = ns
}

// raw runs a command in the active namespace. A command that never returns
// (the socket client has no timeout) would hang the whole run, so it aborts
// the process with a clear message instead.
func (r *runner) raw(cmd string) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
		case <-time.After(3 * time.Minute):
			fmt.Fprintf(os.Stderr, "FATAL: %v: %q did not return within 3m; aborting the run. "+
				"Mark the line with Skip in tests/live/examples/table.go.\n", r.t.Name(), cmd)
			os.Exit(1)
		}
	}()
	_, err := harness.Raw(cmd)
	return err
}

// inNS runs a command in the runner's namespace and returns its rows.
func (r *runner) rows(cmd string) ([]harness.Row, error) {
	resps, err := harness.Raw(fmt.Sprintf("namespace %v %v", r.ns, cmd))
	return harness.ToRows(resps), err
}

func (r *runner) vmInfo() map[string]harness.Row {
	rows, err := r.rows("vm info")
	if err != nil {
		r.t.Logf("vm info: %v", err)
	}
	out := map[string]harness.Row{}
	for _, row := range rows {
		out[row["name"]] = row
	}
	return out
}

// setup runs the named Setup's lines and waits for what it promises.
func (r *runner) setup(name string) {
	s := Setups[name]
	r.t.Logf("setup %v", name)
	for _, l := range s.Lines {
		if p, ok := strings.CutPrefix(l, "@"); ok {
			r.runFile(p, nil)
		} else if err := r.exec(l); err != nil {
			r.t.Fatalf("setup %v: %v", name, err)
		}
	}
	if r.failed {
		r.t.Fatalf("setup %v: a line failed", name)
	}
	if s.Running || s.CC {
		r.waitRunning()
	}
	if s.CC {
		r.waitCC()
		r.stage()
		r.warmCC()
	}
	for _, l := range s.After {
		if err := r.exec(l); err != nil {
			r.t.Fatalf("setup %v: %v", name, err)
		}
	}
}

var (
	reNS     = regexp.MustCompile(`^namespace\s+(\S+)$`)
	reNet    = regexp.MustCompile(`^vm config net(?:\s+(.*))?$`)
	reKernel = regexp.MustCompile(`^vm config kernel\s+(\S+)`)
	reLaunch = regexp.MustCompile(`^vm launch kvm\s+(\S+)`)
)

// exec runs one command line, applying the launch overrides and tracking
// the configuration it changes.
func (r *runner) exec(line string) error {
	if m := reLaunch.FindStringSubmatch(line); m != nil {
		if err := r.overrides(); err != nil {
			return err
		}
	}
	if err := r.raw(line); err != nil {
		return err
	}

	switch {
	case reNS.MatchString(line):
		r.switchNS(reNS.FindStringSubmatch(line)[1])
	case line == "clear vm config":
		r.nets, r.kernel = 0, ""
	case reNet.MatchString(line):
		nets, _, _ := strings.Cut(reNet.FindStringSubmatch(line)[1], "#")
		r.nets = len(strings.Fields(nets))
	case reKernel.MatchString(line):
		r.kernel = reKernel.FindStringSubmatch(line)[1]
	case reLaunch.MatchString(line):
		target := reLaunch.FindStringSubmatch(line)[1]
		if n, err := strconv.Atoi(target); err == nil {
			r.anon += n
		} else if names, err := ranges.SplitList(target); err == nil {
			for _, n := range names {
				r.named[n] = r.nets
			}
		}
	case strings.HasPrefix(line, "vm start"):
		r.life = "start"
	case strings.HasPrefix(line, "vm stop"):
		r.life = "stop"
	case strings.HasPrefix(line, "vm kill"):
		r.life = "kill"
	case strings.HasPrefix(line, "vm flush"):
		r.flushed = true
	}
	return nil
}

// overrides points the next kvm launch at the live guest. Its initramfs
// lives in RAM; it boots at 304 MB, so give it the clients' 384 (core topology).
func (r *runner) overrides() error {
	cmds := []string{
		"vm config kernel live/live.kernel",
		"vm config initrd live/live.initrd",
		"vm config memory 384",
		"vm config vcpus 1",
		"clear vm config disks",
		"clear vm config cdrom",
	}
	if q := os.Getenv("LIVE_QEMU"); q != "" {
		cmds = append(cmds, "vm config qemu "+q)
	}
	if c := os.Getenv("LIVE_CPU"); c != "" {
		cmds = append(cmds, "vm config cpu "+c)
	}
	// a minirouter image becomes a router in the live guest
	switch {
	case strings.Contains(r.kernel, "minirouter"):
		cmds = append(cmds, "vm config append live.role=router")
		r.appendSet = true
	case r.appendSet:
		cmds = append(cmds, "clear vm config append")
		r.appendSet = false
	}
	for _, c := range cmds {
		if err := r.raw(c); err != nil {
			return fmt.Errorf("override: %w", err)
		}
	}
	return nil
}

func match(rules []LineRule, line string) (LineRule, bool) {
	for _, r := range rules {
		if strings.Contains(line, r.Match) {
			return r, true
		}
	}
	return LineRule{}, false
}

// runFile runs a doc example. With e == nil (a setup step) failures are fatal.
func (r *runner) runFile(path string, e *Example) {
	raw, err := os.ReadFile(filepath.Join(docRoot, path))
	if err != nil {
		r.t.Fatal(err)
	}
	for _, line := range CommandLines(string(raw)) {
		if e != nil {
			if rule, ok := match(e.Skip, line); ok {
				r.t.Logf("skipped %q: %v", line, rule.Reason)
				continue
			}
		}
		err := r.exec(line)
		var expected bool
		if e != nil {
			_, expected = match(e.Errors, line)
		}
		switch {
		case err != nil && expected:
			r.t.Logf("failed as expected: %v", err)
		case err != nil:
			r.t.Errorf("%v: line %q failed: %v", path, line, err)
			r.failed = true
		case expected:
			r.t.Errorf("%v: line %q was expected to fail but succeeded; remove its Errors rule in tests/live/examples/table.go", path, line)
			r.failed = true
		}
		if r.failed && e == nil {
			r.t.FailNow()
		}
	}
}

// verify checks what the example left behind.
func (r *runner) verify(e Example) {
	t := r.t
	if !r.flushed && (len(r.named) > 0 || r.anon > 0) {
		vms := r.vmInfo()
		if want := len(r.named) + r.anon; len(vms) != want {
			t.Errorf("expected %d VMs in vm info, got %d: %v", want, len(vms), harness.Keys(vms))
		}
		for name, nets := range r.named {
			row, ok := vms[name]
			if !ok {
				t.Errorf("VM %v was launched but is not in vm info", name)
				continue
			}
			if got := len(harness.List(row["vlan"])); got != nets {
				t.Errorf("VM %v has networks %v, want %d", name, row["vlan"], nets)
			}
		}
	}

	if r.life == "start" {
		r.waitRunning()
	}
	if e.CCResponses {
		harness.Eventually(t, 2*time.Minute, 3*time.Second, func() error {
			rows, err := r.rows("cc commands")
			if err != nil {
				return err
			}
			// "cc commands" does not say which are exec, so every command
			// of an opted-in example must have been answered
			for _, row := range rows {
				if c, _ := strconv.Atoi(row["responses"]); c < 1 {
					return fmt.Errorf("cc command %v (%v) has no responses", row["id"], row["command"])
				}
			}
			if len(rows) == 0 {
				return fmt.Errorf("no cc commands found")
			}
			return nil
		})
	}
	if e.Check != "" {
		extraChecks[e.Check](r)
	}
}

// waitRunning waits until every VM is RUNNING.
func (r *runner) waitRunning() {
	harness.Eventually(r.t, 3*time.Minute, 3*time.Second, func() error {
		vms := r.vmInfo()
		if len(vms) == 0 {
			return fmt.Errorf("no VMs")
		}
		for name, row := range vms {
			if row["state"] != "RUNNING" {
				return fmt.Errorf("VM %v is %v", name, row["state"])
			}
		}
		return nil
	})
}

// waitCC waits for a miniccc client per VM.
func (r *runner) waitCC() {
	want := len(r.named) + r.anon
	harness.Eventually(r.t, 3*time.Minute, 3*time.Second, func() error {
		rows, err := r.rows("cc clients")
		if err != nil {
			return err
		}
		if len(rows) < want {
			return fmt.Errorf("%d of %d cc clients", len(rows), want)
		}
		return nil
	})
}

// stage puts the files the cc examples send into the files directory.
func (r *runner) stage() {
	for _, f := range []string{"foo", "foo.bash", "bar.bash", "my_whole_directory/a", "somedirectory/a"} {
		p := filepath.Join(harness.MMFiles, f)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho "+f+"\n"), 0755); err != nil {
			r.t.Fatal(err)
		}
	}
	// cc09.mm mounts onto /mnt/foo in the minimega container
	if _, err := harness.InContainer(r.t, "mkdir", "-p", "/mnt/foo"); err != nil {
		r.t.Fatalf("mkdir /mnt/foo: %v", err)
	}
	// protonuke comes from the image, as in the guest
	if _, err := harness.InContainer(r.t, "sh", "-c", "cp $(command -v protonuke) "+filepath.Join(harness.MMFiles, "protonuke")); err != nil {
		r.t.Logf("staging protonuke: %v", err)
	}
}

// warmCC issues six cc commands so "cc responses 1..6" and "cc responses foo"
// have something to show, and waits for the clients to answer.
func (r *runner) warmCC() {
	cmds := []string{"cc prefix foo", "cc exec hostname", "clear cc prefix"}
	for i := 2; i <= 6; i++ {
		cmds = append(cmds, fmt.Sprintf("cc exec echo %d", i))
	}
	for _, c := range cmds {
		if err := r.raw(c); err != nil {
			r.t.Fatalf("warming cc: %v", err)
		}
	}
	harness.Eventually(r.t, 2*time.Minute, 3*time.Second, func() error {
		rows, err := r.rows("cc commands")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if c, _ := strconv.Atoi(row["responses"]); c < 1 {
				return fmt.Errorf("cc command %v has no responses", row["id"])
			}
		}
		return nil
	})
}

// cleanup removes everything the example created and fails the test if
// something survives, so entries cannot affect each other.
func (r *runner) cleanup() {
	t := r.t
	resps, _ := harness.Raw("namespace")
	for _, row := range harness.ToRows(resps) {
		if ns := row["namespace"]; ns != baseNS && ns != "minimega" && ns != "" {
			if err := r.raw("clear namespace " + ns); err != nil {
				t.Errorf("clear namespace %v: %v", ns, err)
			}
		}
	}
	r.raw("dnsmasq kill all")
	r.raw("clear tap")
	r.raw("namespace " + baseNS)
	harness.Namespace = baseNS

	if os.Getenv("LIVE_CONTAINER") == "" {
		return
	}
	err := func() error {
		var last error
		for i := 0; i < 20; i++ {
			last = leftovers()
			if last == nil {
				return nil
			}
			time.Sleep(3 * time.Second)
		}
		return last
	}()
	if err != nil {
		t.Errorf("example left state behind: %v", err)
	}
}

// leftovers reports namespaces, qemu, dnsmasq, taps and OVS ports that remain.
func leftovers() error {
	resps, _ := harness.Raw("namespace")
	for _, row := range harness.ToRows(resps) {
		if ns := row["namespace"]; ns != baseNS && ns != "minimega" && ns != "" {
			return fmt.Errorf("namespace %v still exists", ns)
		}
	}
	for _, p := range []string{"qemu", "dnsmasq"} {
		out, _ := harness.ContainerOutput("pgrep", "-c", p)
		if n := strings.TrimSpace(out); n != "0" && n != "" {
			return fmt.Errorf("%v %s processes still running", n, p)
		}
	}
	if resps, _ := harness.Raw("tap"); len(harness.ToRows(resps)) > 0 {
		return fmt.Errorf("taps remain: %v", harness.ToRows(resps))
	}
	out, err := harness.ContainerOutput("ovs-vsctl", "list-ports", "mega_bridge")
	if err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf("ports left on mega_bridge: %v", strings.Fields(out))
	}
	return nil
}
