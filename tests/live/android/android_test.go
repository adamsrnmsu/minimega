// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

//go:build live

// Package android is the live test group for Android Emulator VMs: one phone
// on a VLAN, launched from the configuration in topology.mm. Lines starting
// with TIMING and FINDING in the test log are the measurements of the spike.
package android

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sandia-minimega/minimega/v2/tests/live/harness"
)

const (
	vm = "phone0"
	// same paths as topology.mm; the first Android VM on a host gets 5554
	adbBin = "/usr/local/lib/android/sdk/platform-tools/adb"
)

// set by one phase, read by later ones
var (
	// adb serial; Launch and Start replace it with android_serial from vm info
	serial    = "emulator-5554"
	bootStart time.Time
	vmID      string
	vmTap     string
)

func TestMain(m *testing.M) {
	if err := harness.Connect(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestLive(t *testing.T) {
	harness.RunPhases(t, []harness.Phase{
		{Name: "Launch", Fn: testLaunch},
		{Name: "Start", Fn: testStart, Needs: []string{"Launch"}},
		{Name: "Boot", Fn: testBoot, Needs: []string{"Start"}},
		{Name: "Info", Fn: testInfo, Needs: []string{"Start"}},
		{Name: "Kill", Fn: testKill, Needs: []string{"Launch"}},
		{Name: "Flush", Fn: testFlush, Needs: []string{"Kill"}},
		// run.sh runs this phase by name, alone, as the teardown
		{Name: "ClearNamespace", Fn: testClearNamespace},
	})
}

// --- helpers -----------------------------------------------------------------

// adb runs adb in the container, where the emulator's ports are, and returns
// the last line of output: InContainer merges stderr, and adb prints daemon
// start-up messages there.
func adb(t testing.TB, args ...string) (string, error) {
	t.Helper()
	out, err := harness.InContainer(t, append([]string{"timeout", "30", adbBin}, args...)...)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), err
}

func shell(t testing.TB, args ...string) (string, error) {
	t.Helper()
	return adb(t, append([]string{"-s", serial, "shell"}, args...)...)
}

// logCmd logs the output of a command run in the container.
func logCmd(t testing.TB, label string, args ...string) {
	t.Helper()
	out, err := harness.InContainer(t, args...)
	t.Logf("%s (err: %v):\n%s", label, err, out)
}

func ps(t testing.TB, label string) {
	t.Helper()
	logCmd(t, "ps "+label, "ps", "-eo", "pid,ppid,rss,comm,args")
}

// qemuPids lists the emulator's QEMU processes. pgrep runs directly, not
// through a shell whose own command line would match.
func qemuPids(t testing.TB) []string {
	t.Helper()
	out, _ := harness.InContainer(t, "pgrep", "-f", "qemu-system")
	return strings.Fields(out)
}

// save writes an artifact for CI to upload.
func save(t testing.TB, name string, data []byte) {
	t.Helper()
	dir := os.Getenv("LIVE_ARTIFACTS")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Logf("save %v: %v", name, err)
	}
}

// instanceDir is the VM's directory under the minimega base, which is
// bind-mounted at the same path on the host.
func instanceDir() string {
	return filepath.Join(filepath.Dir(harness.MMFiles), vmID)
}

// keepLog saves the emulator's output. minimega truncates it on relaunch and
// deletes it on flush, so each copy gets its own name.
func keepLog(t testing.TB, suffix string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(instanceDir(), "android-emulator.log"))
	if err != nil {
		t.Logf("emulator log: %v", err)
		return
	}
	save(t, "android-emulator-"+suffix+".log", b)
}

func info(t testing.TB) harness.Row {
	t.Helper()
	r, ok := harness.VMInfo(t)[vm]
	if !ok {
		t.Fatalf("%v is not in vm info", vm)
	}
	return r
}

// --- phases ------------------------------------------------------------------

func testLaunch(t *testing.T) {
	// start the adb server before the emulator so that root's adb key exists
	// when the guest first asks for authorisation
	logCmd(t, "adb start-server", adbBin, "start-server")

	start := time.Now()
	_, err := harness.Run("vm launch android " + vm)
	t.Logf("TIMING launch_to_qmp=%.1fs (err: %v)", time.Since(start).Seconds(), err)

	r := info(t)
	vmID, serial = r["id"], r["android_serial"]

	// minimega gives the emulator a fixed 30s to open its QMP socket. A cold
	// AVD may miss that; "vm start" relaunches a VM in ERROR, so record it
	// and let Start decide.
	if r["state"] == "ERROR" {
		keepLog(t, "launch1")
		t.Logf("FINDING first_launch_error launch error: %v; tags: %v", err, r["tags"])
		return
	}
	if err != nil {
		t.Fatalf("vm launch: %v", err)
	}
	if r["state"] != "BUILDING" {
		t.Errorf("state after launch = %v, want BUILDING", r["state"])
	}
	if h := harness.MustTable(t, "host"); len(h) != 1 || h[0]["androidvms"] != "1" {
		t.Errorf("host androidvms: got %v, want 1", h)
	}
}

func testStart(t *testing.T) {
	start := time.Now()
	_, err := harness.Run("vm start " + vm)
	t.Logf("TIMING start=%.1fs", time.Since(start).Seconds())
	bootStart = time.Now()
	if err != nil {
		keepLog(t, "start")
		t.Fatalf("vm start: %v; tags: %v", err, info(t)["tags"])
	}
	r := info(t)
	// a relaunch reserves its ports again
	serial = r["android_serial"]
	if r["state"] != "RUNNING" {
		t.Fatalf("state after start = %v, want RUNNING", r["state"])
	}
}

func testBoot(t *testing.T) {
	defer func() {
		if t.Failed() {
			logCmd(t, "adb devices", adbBin, "devices", "-l")
			ps(t, "boot-failed")
			keepLog(t, "boot-failed")
		}
	}()

	// RUNNING only means QEMU accepted "cont"; Android is up when the
	// property is set. adb fails until the guest's adbd answers.
	harness.Eventually(t, 5*time.Minute, 2*time.Second, func() error {
		if s := info(t)["state"]; s != "RUNNING" {
			t.Fatalf("state = %v while booting", s)
		}
		out, err := shell(t, "getprop", "sys.boot_completed")
		if out != "1" {
			return fmt.Errorf("sys.boot_completed = %q (adb: %v)", out, err)
		}
		return nil
	})
	t.Logf("TIMING start_to_boot_completed=%.1fs", time.Since(bootStart).Seconds())

	// the rest is recorded, not asserted
	for _, c := range [][]string{
		{"nproc"},
		{"grep", "MemTotal", "/proc/meminfo"},
		{"getprop", "ro.build.version.sdk"},
	} {
		out, err := shell(t, c...)
		t.Logf("guest %v: %v (err: %v)", strings.Join(c, " "), out, err)
	}
	// does the guest see the tap NIC next to the emulator's own?
	logCmd(t, "guest ip -o link", adbBin, "-s", serial, "shell", "ip", "-o", "link")
	ps(t, "booted")
	logCmd(t, "free -m", "free", "-m")
	// is the pid minimega tracks (and will SIGKILL) QEMU or a launcher?
	t.Logf("FINDING pids vm_info=%v qemu-system=%v", info(t)["pid"], qemuPids(t))
}

func testInfo(t *testing.T) {
	r := info(t)
	for col, want := range map[string]string{
		"type":                 "android",
		"state":                "RUNNING",
		"android_avd":          "mmtest",
		"android_console_port": "5554",
		"android_adb_port":     "5555",
		"android_serial":       "emulator-5554",
		"android_grpc_port":    "8554",
		"bridge":               "[mega_bridge]",
	} {
		if r[col] != want {
			t.Errorf("%v = %q, want %q", col, r[col], want)
		}
	}
	if !strings.Contains(r["vlan"], "phones") {
		t.Errorf("vlan = %q, want phones", r["vlan"])
	}
	if r["pid"] == "" || r["pid"] == "0" {
		t.Errorf("pid = %q", r["pid"])
	}
	taps, macs := harness.List(r["tap"]), harness.List(r["mac"])
	if len(taps) != 1 || taps[0] == "" || len(macs) != 1 || macs[0] == "" {
		t.Fatalf("tap = %q, mac = %q, want one of each", r["tap"], r["mac"])
	}
	ports, err := harness.InContainer(t, "ovs-vsctl", "list-ports", "mega_bridge")
	if err != nil || !strings.Contains(ports, taps[0]) {
		t.Errorf("tap %v is not on mega_bridge: %v (err: %v)", taps[0], strings.Fields(ports), err)
	}
}

func testKill(t *testing.T) {
	if taps := harness.List(info(t)["tap"]); len(taps) > 0 {
		vmTap = taps[0]
	}

	// flush deletes the emulator log, and logcat needs a live guest
	keepLog(t, "final")
	if out, err := harness.InContainer(t, "timeout", "60", adbBin, "-s", serial, "logcat", "-d"); err != nil {
		t.Logf("logcat not reachable: %v", err)
	} else {
		save(t, "logcat.txt", []byte(out))
	}

	harness.MustRun(t, "vm kill "+vm)
	harness.Eventually(t, 30*time.Second, time.Second, func() error {
		if s := info(t)["state"]; s != "QUIT" {
			return fmt.Errorf("state = %v, want QUIT", s)
		}
		return nil
	})

	// minimega kills one pid. If the emulator forked QEMU, it is still here.
	time.Sleep(2 * time.Second)
	ps(t, "after-kill")
	if pids := qemuPids(t); len(pids) > 0 {
		t.Errorf("FINDING orphan_after_kill qemu-system pids %v survived vm kill", pids)
	}
}

func testFlush(t *testing.T) {
	harness.MustRun(t, "vm flush")

	if _, ok := harness.VMInfo(t)[vm]; ok {
		t.Errorf("%v is still in vm info after flush", vm)
	}
	// Flush marks the tap defunct; the tap reaper (TapReapRate, 1 s) removes
	// the OVS port, so poll like core does.
	harness.Eventually(t, 30*time.Second, time.Second, func() error {
		ports, err := harness.InContainer(t, "ovs-vsctl", "list-ports", "mega_bridge")
		if err != nil {
			return fmt.Errorf("ovs-vsctl: %v", err)
		}
		if vmTap != "" && strings.Contains(ports, vmTap) {
			return fmt.Errorf("tap %v is still on mega_bridge after flush", vmTap)
		}
		return nil
	})
	if _, err := os.Stat(instanceDir()); !os.IsNotExist(err) {
		t.Errorf("instance dir %v still exists after flush (err: %v)", instanceDir(), err)
	}
}

func testClearNamespace(t *testing.T) {
	// the adb server is ours, not minimega's
	logCmd(t, "adb kill-server", "timeout", "30", adbBin, "kill-server")

	if _, err := harness.Raw("clear namespace " + harness.Namespace); err != nil {
		t.Fatalf("clear namespace: %v", err)
	}
	resps, err := harness.Raw("namespace")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range harness.ToRows(resps) {
		if r["namespace"] == harness.Namespace {
			t.Errorf("namespace %v still exists", harness.Namespace)
		}
	}

	harness.Eventually(t, time.Minute, 3*time.Second, func() error {
		if pids := qemuPids(t); len(pids) > 0 {
			return fmt.Errorf("qemu-system processes still running: %v", pids)
		}
		out, err := harness.InContainer(t, "ovs-vsctl", "list-ports", "mega_bridge")
		if err == nil && strings.TrimSpace(out) != "" {
			return fmt.Errorf("ports left on mega_bridge: %v", strings.Fields(out))
		}
		return nil
	})
}
