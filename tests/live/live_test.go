// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

//go:build live

package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	var err error
	if client, err = dial(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestLive runs every check against the topology loaded from topology.mm.
// Subtests run in order: early phases establish that the experiment is up,
// later phases mutate it, and the final phase tears it down.
func TestLive(t *testing.T) {
	// What each phase needs to have passed first. Phases that need nothing
	// beyond the inventory still run when the guests do not come up.
	version := []string{"Version"}
	inv := []string{"Inventory"}
	cc := []string{"CCClients"}
	network := []string{"CCClients", "DHCP"}

	runPhases(t, []phase{
		// control plane
		{"Version", testVersion, nil},
		{"Check", testCheck, version},
		{"Host", testHost, version},
		{"Namespace", testNamespace, version},
		{"Inventory", testInventory, []string{"Namespace"}},
		{"VMConfig", testVMConfig, inv},
		{"VMInfoSummary", testVMInfoSummary, inv},
		{"VLANs", testVLANs, inv},
		{"Bridge", testBridge, inv},
		{"FileList", testFileList, inv},
		{"Disk", testDisk, inv},
		{"Miniweb", testMiniweb, inv},

		// guests come up
		{"CCClients", testCCClients, inv},
		{"DHCP", testDHCP, inv},

		// command and control
		{"CCExec", testCCExec, cc},
		{"CCFilterTag", testCCFilterTag, cc},
		{"CCExitCode", testCCExitCode, cc},
		{"CCSendRecv", testCCSendRecv, cc},
		{"CCBackgroundProcess", testCCBackgroundProcess, cc},
		{"CCResponses", testCCResponses, cc},

		// what the guest sees of its VM configuration
		{"GuestQemuAppend", testGuestQemuAppend, cc},
		{"GuestDisks", testGuestDisks, cc},
		{"GuestMachineType", testGuestMachineType, cc},
		{"GuestSMP", testGuestSMP, cc},
		{"GuestNICDrivers", testGuestNICDrivers, cc},
		{"GuestDualHomed", testGuestDualHomed, cc},

		// networking
		{"PingSameLAN", testPingSameLAN, network},
		{"OSPF", testOSPF, network},
		{"PingAcrossRouters", testPingAcrossRouters, network},
		{"NoBackchannelHostReachable", testNoBackchannelHost, network},
		{"DNS", testDNS, network},
		{"HTTPProtonuke", testHTTPProtonuke, network},
		{"Firewall", testFirewall, network},
		{"HostTap", testHostTap, network},
		{"CCRTunnel", testCCRTunnel, network},
		{"QoS", testQoS, network},
		{"CapturePcap", testCapturePcap, network},

		// VM operations
		{"Screenshot", testScreenshot, inv},
		{"Serial", testSerial, inv},
		{"QMP", testQMP, inv},
		{"VNCRecord", testVNCRecord, inv},
		{"Tags", testTags, cc},
		{"Top", testTop, inv},
		{"StopStart", testStopStart, cc},
		{"NetDisconnectConnect", testNetDisconnectConnect, network},
		{"Hotplug", testHotplug, cc},
		{"Save", testSave, cc},
		{"KillFlushRelaunch", testKillFlushRelaunch, cc},

		// teardown
		{"ClearNamespace", testClearNamespace, version},
	})
}

// --- control plane -----------------------------------------------------------

func testVersion(t *testing.T) {
	v := mustText(t, "version")
	if !strings.Contains(v, "minimega") {
		t.Fatalf("unexpected version: %q", v)
	}
	t.Log(strings.TrimSpace(v))
}

func testCheck(t *testing.T) {
	// "check" reports missing external dependencies as errors.
	if _, err := client.run("check"); err != nil {
		t.Errorf("dependency check failed: %v", err)
	}
}

func testHost(t *testing.T) {
	rows := mustTable(t, "host")
	if len(rows) != 1 {
		t.Fatalf("expected 1 host, got %d", len(rows))
	}
	h := rows[0]
	if n, _ := strconv.Atoi(h["cpus"]); n < 1 {
		t.Errorf("host cpus = %q", h["cpus"])
	}
	if vms, _ := strconv.Atoi(h["vms"]); vms < len(topology) {
		t.Errorf("host vms = %v, want >= %d", h["vms"], len(topology))
	}
	if mem, _ := strconv.Atoi(h["memused"]); mem <= 0 {
		t.Errorf("host memused = %q", h["memused"])
	}
}

func testNamespace(t *testing.T) {
	found := false
	for _, r := range mustTable(t, "namespace") {
		if r["namespace"] == namespace {
			found = true
		}
	}
	if !found {
		t.Fatalf("namespace %q not listed", namespace)
	}
	if hosts := mustText(t, "ns hosts"); strings.TrimSpace(hosts) == "" {
		t.Errorf("namespace has no hosts")
	}
	if q := strings.TrimSpace(mustText(t, "ns queueing")); q != "true" {
		t.Errorf("ns queueing = %q, want true (set by topology.mm)", q)
	}
	// "vm launch" flushed the queue
	if q := strings.TrimSpace(mustText(t, "ns queue")); q != "" {
		t.Errorf("queue not empty after flush: %q", q)
	}
}

func testInventory(t *testing.T) {
	eventually(t, 3*time.Minute, 5*time.Second, func() error {
		vms := vmInfo(t)
		if len(vms) != len(topology) {
			return fmt.Errorf("got %d VMs, want %d (%v)", len(vms), len(topology), keys(vms))
		}
		for _, s := range topology {
			r, ok := vms[s.Name]
			if !ok {
				return fmt.Errorf("missing VM %v", s.Name)
			}
			if r["type"] != "kvm" {
				return fmt.Errorf("%v: type %v", s.Name, r["type"])
			}
			if r["state"] != "RUNNING" {
				return fmt.Errorf("%v: state %v (error tag: %v)", s.Name, r["state"], r["tags"])
			}
		}
		return nil
	})
}

func testVMConfig(t *testing.T) {
	vms := vmInfo(t)

	for _, s := range topology {
		t.Run(s.Name, func(t *testing.T) {
			r := vms[s.Name]
			if r["memory"] != s.Memory {
				t.Errorf("memory = %v, want %v", r["memory"], s.Memory)
			}
			if r["vcpus"] != s.VCPUs {
				t.Errorf("vcpus = %v, want %v", r["vcpus"], s.VCPUs)
			}
			if r["snapshot"] != "true" {
				t.Errorf("snapshot = %v", r["snapshot"])
			}
			if !strings.HasSuffix(r["kernel"], "live/live.kernel") || !strings.HasSuffix(r["initrd"], "live/live.initrd") {
				t.Errorf("kernel/initrd = %v / %v", r["kernel"], r["initrd"])
			}

			vlans := list(r["vlan"])
			if len(vlans) != len(s.VLANs) {
				t.Fatalf("vlan = %v, want aliases %v", r["vlan"], s.VLANs)
			}
			for i, alias := range s.VLANs {
				if !strings.HasPrefix(vlans[i], alias+" (") {
					t.Errorf("nic %d vlan = %v, want alias %v", i, vlans[i], alias)
				}
			}
			for _, b := range list(r["bridge"]) {
				if b != "mega_bridge" {
					t.Errorf("bridge = %v", b)
				}
			}

			var tags map[string]string
			if err := json.Unmarshal([]byte(r["tags"]), &tags); err != nil {
				t.Fatalf("tags %q: %v", r["tags"], err)
			}
			for k, v := range s.Tags {
				if tags[k] != v {
					t.Errorf("tag %v = %q, want %q", k, tags[k], v)
				}
			}
			if tags["topology"] != "live" {
				t.Errorf("tag topology = %q (from saved config live-base)", tags["topology"])
			}

			wantCC := "live.role=host"
			if s.Router {
				wantCC = "live.role=router"
			}
			if !strings.Contains(r["append"], wantCC) {
				t.Errorf("append = %q", r["append"])
			}
		})
	}

	// one-off options
	check := func(vm, col, want string) {
		t.Helper()
		if got := vms[vm][col]; !strings.Contains(got, want) {
			t.Errorf("%v %v = %q, want it to contain %q", vm, col, got, want)
		}
	}
	check("host-a1", "serial-ports", "1")
	check("host-a1", "virtio-ports", "live.port0")
	check("host-c1", "disks", "data.qcow2")
	check("host-c1", "disks", "virtio")
	check("host-c2", "disks", "ide")
	check("host-c2", "disks", "writeback")
	check("host-c5", "usb-use-xhci", "true")
	check("host-d5", "uuid", d5UUID)
	for i := 1; i <= 5; i++ {
		check("host-b"+itoa(i), "mac", "00:1f:53:00:02:1"+itoa(i))
	}
	if got := vms["host-a2"]["serial-ports"]; got != "0" && got != "" {
		t.Errorf("host-a2 serial-ports = %q, want none", got)
	}
}

func testVMInfoSummary(t *testing.T) {
	rows := mustTable(t, "vm info summary")
	if len(rows) != len(topology) {
		t.Fatalf("summary rows = %d", len(rows))
	}
	if _, ok := rows[0]["memory"]; ok {
		t.Errorf("summary should not include memory column")
	}
	for _, col := range []string{"name", "state", "uuid", "cc_active", "vlan"} {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("summary missing column %v", col)
		}
	}
}

func testVLANs(t *testing.T) {
	got := map[string]string{}
	for _, r := range mustTable(t, "vlans") {
		got[r["alias"]] = r["vlan"]
	}
	if got["lan-a"] != "1001" {
		t.Errorf("lan-a = %q, want 1001 (vlans add)", got["lan-a"])
	}
	seen := map[string]string{}
	for _, alias := range []string{"lan-a", "lan-b", "lan-c", "lan-d"} {
		v, ok := got[alias]
		if !ok {
			t.Errorf("alias %v missing: %v", alias, got)
			continue
		}
		if other, dup := seen[v]; dup {
			t.Errorf("%v and %v share VLAN %v", alias, other, v)
		}
		seen[v] = alias
	}
}

func testBridge(t *testing.T) {
	rows := mustTable(t, "bridge")
	found := false
	for _, r := range rows {
		if r["bridge"] == "mega_bridge" {
			found = true
		}
	}
	if !found {
		t.Errorf("mega_bridge not listed: %v", rows)
	}

	out, err := inContainer(t, "ovs-vsctl", "list-ports", "mega_bridge")
	if err != nil {
		t.Fatalf("ovs-vsctl: %v: %s", err, out)
	}
	ports := strings.Fields(out)
	// every VM NIC plus the live-mgmt host tap
	if len(ports) < totalNICs+1 {
		t.Errorf("mega_bridge has %d ports, want >= %d: %v", len(ports), totalNICs+1, ports)
	}

	// every VM tap is tagged with its VLAN in OVS
	vms := vmInfo(t)
	vlanOf := map[string]string{}
	for _, r := range mustTable(t, "vlans") {
		vlanOf[r["alias"]] = r["vlan"]
	}
	for _, s := range []string{"host-a1", "rtr-cd"} {
		checkOVSTags(t, s, list(vms[s]["tap"]), vlanOf)
	}
}

// checkOVSTags checks that each of a VM's taps is tagged in OVS with the VLAN
// of the matching NIC in its spec.
func checkOVSTags(t *testing.T, vm string, taps []string, vlanOf map[string]string) {
	t.Helper()
	vlans := specFor(vm).VLANs
	if len(taps) != len(vlans) {
		t.Errorf("%v has taps %v, want one per VLAN in %v", vm, taps, vlans)
		return
	}
	for i, tap := range taps {
		out, err := inContainer(t, "ovs-vsctl", "get", "port", tap, "tag")
		if err != nil {
			t.Errorf("ovs tag %v: %v %s", tap, err, out)
			continue
		}
		if want := vlanOf[vlans[i]]; strings.TrimSpace(out) != want {
			t.Errorf("%v nic %d tap %v tag = %q, want %v", vm, i, tap, strings.TrimSpace(out), want)
		}
	}
}

func testFileList(t *testing.T) {
	rows := mustTable(t, "file list live")
	names := map[string]bool{}
	for _, r := range rows {
		names[strings.TrimPrefix(r["name"], "/")] = true
	}
	for _, f := range []string{"live/live.kernel", "live/live.initrd", "live/topology.mm", "live/data.qcow2"} {
		if !names[f] {
			t.Errorf("file list missing %v (have %v)", f, keys(names))
		}
	}
}

func testDisk(t *testing.T) {
	rows := mustTable(t, "disk info live/data.qcow2")
	if len(rows) != 1 {
		t.Fatalf("disk info rows: %v", rows)
	}
	r := rows[0]
	if r["format"] != "qcow2" {
		t.Errorf("format = %q", r["format"])
	}
	if !strings.Contains(r["virtualsize"], "64") {
		t.Errorf("virtual size = %q", r["virtualsize"])
	}
	if r["inuse"] != "true" {
		t.Errorf("inuse = %q, want true (attached to host-c1 and host-c2)", r["inuse"])
	}

	// snapshot then inspect the backing chain
	mustRun(t, "disk snapshot live/data.qcow2 live/data-snap.qcow2")
	snap := mustTable(t, "disk info live/data-snap.qcow2")
	if len(snap) == 0 || !strings.Contains(snap[0]["backingfile"], "data.qcow2") {
		t.Errorf("snapshot backing file: %v", snap)
	}
}

func testMiniweb(t *testing.T) {
	get := func(path string) []byte {
		t.Helper()
		resp, err := http.Get(miniwebURL + path)
		if err != nil {
			t.Fatalf("GET %v: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %v: %v", path, resp.Status)
		}
		return body
	}

	if body := get("/"); !bytes.Contains(bytes.ToLower(body), []byte("minimega")) {
		t.Errorf("index page does not mention minimega")
	}

	// miniweb talks to minimega over the same socket and lists our VMs
	body := get("/vms/info.json?namespace=" + namespace)
	for _, s := range topology {
		if !bytes.Contains(body, []byte(`"`+s.Name+`"`)) {
			t.Errorf("/vms/info.json missing %v", s.Name)
		}
	}
}

// --- guests come up ----------------------------------------------------------

func testCCClients(t *testing.T) {
	want := ccVMs()
	eventually(t, settleTimeout, 5*time.Second, func() error {
		vms := vmInfo(t)
		var missing []string
		for _, s := range want {
			if vms[s.Name]["cc_active"] != "true" {
				missing = append(missing, s.Name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("cc not active on %v", missing)
		}
		return nil
	})

	clients := mustTable(t, "cc clients")
	if len(clients) != len(want) {
		t.Errorf("cc clients = %d, want %d", len(clients), len(want))
	}
	for _, c := range clients {
		if c["os"] != "linux" || c["arch"] != "amd64" {
			t.Errorf("client %v reports %v/%v", c["hostname"], c["os"], c["arch"])
		}
	}

	if got := vmInfo(t)["host-d4"]["cc_active"]; got != "false" {
		t.Errorf("host-d4 (backchannel false) cc_active = %v", got)
	}
}

func testDHCP(t *testing.T) {
	// minimega learns guest addresses by snooping traffic on each tap, so
	// this also covers the no-backchannel host.
	eventually(t, settleTimeout, 5*time.Second, func() error {
		vms := vmInfo(t)
		var bad []string
		for _, s := range topology {
			ips := list(vms[s.Name]["ip"])
			for i, want := range s.IPs {
				got := ""
				if i < len(ips) {
					got = ips[i]
				}
				ok := got == want
				if want == "" {
					ok = strings.HasPrefix(got, s.Subnet)
				}
				if !ok {
					bad = append(bad, fmt.Sprintf("%v[%d]=%q", s.Name, i, got))
				}
			}
		}
		if len(bad) > 0 {
			return fmt.Errorf("unexpected addresses: %v", bad)
		}
		return nil
	})

	// dynamic leases fall inside the configured range
	for i := 1; i <= 5; i++ {
		name := "host-a" + itoa(i)
		ip := list(vmInfo(t)[name]["ip"])[0]
		last, _ := strconv.Atoi(ip[strings.LastIndex(ip, ".")+1:])
		if last < 100 || last > 199 {
			t.Errorf("%v leased %v outside 10.10.1.100-199", name, ip)
		}
	}
}

// --- command and control -----------------------------------------------------

func testCCExec(t *testing.T) {
	out := ccOK(t, "host-b1", "hostname; uname -s")
	if !strings.Contains(out, "host-b1") || !strings.Contains(out, "Linux") {
		t.Errorf("unexpected output: %q", out)
	}

	// broadcast to every client
	res, _ := ccRun(t, "", "exec hostname", len(ccVMs()), 2*time.Minute)
	for _, s := range ccVMs() {
		r, ok := res[s.Name]
		if !ok {
			t.Errorf("no response from %v", s.Name)
			continue
		}
		if got := strings.TrimSpace(r.Stdout); got != s.hostname() {
			t.Errorf("%v reports hostname %q, want %q", s.Name, got, s.hostname())
		}
	}
}

func testCCFilterTag(t *testing.T) {
	var servers []string
	for _, s := range ccVMs() {
		if s.Tags["role"] == "server" {
			servers = append(servers, s.Name)
		}
	}
	res, _ := ccRun(t, "role=server", "exec true", len(servers), 2*time.Minute)
	if len(res) != len(servers) {
		t.Errorf("role=server reached %v, want %v", keys(res), servers)
	}
	for name := range res {
		if specFor(name).Tags["role"] != "server" {
			t.Errorf("filter leaked to %v", name)
		}
	}

	// filter on a "vm info" column
	res, _ = ccRun(t, "name=host-c2", "exec true", 1, time.Minute)
	if _, ok := res["host-c2"]; !ok || len(res) != 1 {
		t.Errorf("name filter reached %v", keys(res))
	}
}

func testCCExitCode(t *testing.T) {
	res, id := ccRun(t, "name=host-b2", `exec sh -c "exit 3"`, 1, time.Minute)
	if res["host-b2"].ExitCode != 3 {
		t.Errorf("exit code from responses = %d, want 3", res["host-b2"].ExitCode)
	}
	code := strings.TrimSpace(mustText(t, fmt.Sprintf("cc exitcode %d host-b2", id)))
	if code != "3" {
		t.Errorf("cc exitcode = %q, want 3", code)
	}
}

func testCCSendRecv(t *testing.T) {
	payload := fmt.Sprintf("minimega live test %d\n", time.Now().UnixNano())
	src, _ := artifact(t, "send.txt")
	if err := os.WriteFile(src, []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}

	// files are named relative to the namespace's files directory
	ccRun(t, "name=host-c3", "send "+namespace+"/artifacts/send.txt", 1, time.Minute)
	out := ccOK(t, "host-c3", "cat /tmp/miniccc/files/"+namespace+"/artifacts/send.txt")
	if out != payload {
		t.Errorf("guest read %q, want %q", out, payload)
	}

	ccOK(t, "host-c3", "echo from-guest-$(hostname) > /tmp/recv.txt")
	_, id := ccRun(t, "name=host-c3", "recv /tmp/recv.txt", 1, time.Minute)
	uuid := vmInfo(t)["host-c3"]["uuid"]
	path := fmt.Sprintf("%v/%v/miniccc_responses/%d/%v/tmp/recv.txt", mmFiles, namespace, id, uuid)
	eventually(t, 30*time.Second, 2*time.Second, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != "from-guest-host-c3" {
			return fmt.Errorf("received %q", b)
		}
		return nil
	})
}

func testCCBackgroundProcess(t *testing.T) {
	ccRun(t, "name=host-a3", "background sleep 7777", 1, time.Minute)

	var pid string
	eventually(t, time.Minute, 3*time.Second, func() error {
		for _, r := range mustTable(t, "cc process list host-a3") {
			if strings.Contains(r["command"], "sleep 7777") {
				pid = r["pid"]
				return nil
			}
		}
		return fmt.Errorf("sleep 7777 not listed")
	})
	ccOK(t, "host-a3", "ps | grep -q '[s]leep 7777'")

	ccRun(t, "name=host-a3", "process kill "+pid, 0, time.Minute)
	eventually(t, time.Minute, 3*time.Second, func() error {
		for _, r := range mustTable(t, "cc process list host-a3") {
			if strings.Contains(r["command"], "sleep 7777") {
				return fmt.Errorf("process %v still listed", pid)
			}
		}
		return nil
	})
}

func testCCResponses(t *testing.T) {
	_, id := ccRun(t, "name=host-b4", "exec echo cc-responses-check", 1, time.Minute)
	out := mustText(t, fmt.Sprintf("cc responses %d", id))
	if !strings.Contains(out, "cc-responses-check") {
		t.Errorf("cc responses %d = %q", id, out)
	}
	mustRun(t, fmt.Sprintf("cc delete response %d", id))
}

// --- guest view of VM configuration ----------------------------------------

func testGuestQemuAppend(t *testing.T) {
	out := ccOK(t, "host-c1", "cat /sys/class/dmi/id/product_serial")
	if strings.TrimSpace(out) != "LIVE-C1" {
		t.Errorf("SMBIOS serial = %q, want LIVE-C1 (vm config qemu-append)", out)
	}
	// minimega passes the VM UUID through SMBIOS as well
	uuid := ccOK(t, "host-d5", "cat /sys/class/dmi/id/product_uuid")
	if !strings.EqualFold(strings.TrimSpace(uuid), d5UUID) {
		t.Errorf("host-d5 SMBIOS uuid = %q, want %v", uuid, d5UUID)
	}
}

func testGuestDisks(t *testing.T) {
	// virtio disk shows up as vda, IDE as sda; both 64M (131072 sectors)
	if out := ccOK(t, "host-c1", "cat /sys/block/vda/size"); strings.TrimSpace(out) != "131072" {
		t.Errorf("host-c1 vda size = %q", out)
	}
	if out := ccOK(t, "host-c2", "cat /sys/block/sda/size"); strings.TrimSpace(out) != "131072" {
		t.Errorf("host-c2 sda size = %q", out)
	}
	// snapshot mode: the guest sees its own write, the shared base image on
	// the host does not change
	marker := fmt.Sprintf("live-snapshot-%d", time.Now().UnixNano())
	ccOK(t, "host-c1", "echo "+marker+" | dd of=/dev/vda bs=512 count=1 2>/dev/null; sync")
	// drop the page cache so the read comes from the disk, not the write
	if out := ccOK(t, "host-c1", "echo 3 > /proc/sys/vm/drop_caches; dd if=/dev/vda bs=512 count=1 2>/dev/null"); !strings.Contains(out, marker) {
		t.Errorf("host-c1 cannot read back its own write")
	}
	if out := ccOK(t, "host-c2", "dd if=/dev/sda bs=512 count=1 2>/dev/null"); strings.Contains(out, marker) {
		t.Errorf("write on host-c1 is visible to host-c2")
	}
	base, err := os.ReadFile(mmFiles + "/live/data.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(base, []byte(marker)) {
		t.Errorf("snapshot=true VM wrote to the base image")
	}
}

func testGuestMachineType(t *testing.T) {
	if out := ccOK(t, "host-c4", "cat /sys/class/dmi/id/product_name"); !strings.Contains(out, "Q35") {
		t.Errorf("host-c4 machine = %q, want Q35", out)
	}
	if out := ccOK(t, "host-c3", "cat /sys/class/dmi/id/product_name"); !strings.Contains(out, "i440FX") {
		t.Errorf("host-c3 machine = %q, want i440FX default", out)
	}
	if out := ccOK(t, "host-c5", "ls /sys/bus/pci/drivers/xhci_hcd/ | grep -c : || true"); strings.TrimSpace(out) == "0" {
		t.Errorf("host-c5 has no xHCI controller bound (usb-use-xhci)")
	}
}

func testGuestSMP(t *testing.T) {
	if out := ccOK(t, "host-b3", "nproc"); strings.TrimSpace(out) != "2" {
		t.Errorf("host-b3 nproc = %q, want 2", out)
	}
	if out := ccOK(t, "host-b4", "nproc"); strings.TrimSpace(out) != "1" {
		t.Errorf("host-b4 nproc = %q, want 1", out)
	}
	// Kernel overhead is the same on two single-vCPU guests, so their MemTotal
	// differs by the difference in "vm config memory", give or take some page
	// metadata (exactly 32768 kB on the arm64/TCG range). host-b3 is not used:
	// its second vCPU costs ~256 kB more.
	a, b := "host-a1", "host-b4"
	ma, _ := strconv.Atoi(specFor(a).Memory)
	mb, _ := strconv.Atoi(specFor(b).Memory)
	want := (ma - mb) * 1024
	if got := memTotalKB(t, a) - memTotalKB(t, b); got < want-2048 || got > want+1024 {
		t.Errorf("MemTotal %v - %v = %d kB, want about %d kB (memory %v vs %v MB)", a, b, got, want, ma, mb)
	}
}

// memTotalKB returns a guest's MemTotal from /proc/meminfo.
func memTotalKB(t *testing.T, vm string) int {
	t.Helper()
	out := ccOK(t, vm, "grep MemTotal /proc/meminfo")
	f := strings.Fields(out)
	if len(f) < 2 {
		t.Fatalf("%v meminfo: %q", vm, out)
	}
	kb, err := strconv.Atoi(f[1])
	if err != nil {
		t.Fatalf("%v meminfo: %q: %v", vm, out, err)
	}
	return kb
}

func testGuestNICDrivers(t *testing.T) {
	driver := func(vm string) string {
		return strings.TrimSpace(ccOK(t, vm, "basename $(readlink /sys/class/net/eth0/device/driver)"))
	}
	if d := driver("host-b1"); d != "e1000" {
		t.Errorf("host-b1 driver = %q, want e1000", d)
	}
	if d := driver("host-b3"); d != "virtio_net" {
		t.Errorf("host-b3 driver = %q, want virtio_net", d)
	}
	mac := strings.TrimSpace(ccOK(t, "host-b2", "cat /sys/class/net/eth0/address"))
	if mac != "00:1f:53:00:02:12" {
		t.Errorf("host-b2 MAC = %q", mac)
	}
}

func testGuestDualHomed(t *testing.T) {
	out := ccOK(t, "host-d5", "ip -4 -o addr")
	for _, ip := range []string{"10.10.4.15/24", "10.10.3.16/24"} {
		if !strings.Contains(out, ip) {
			t.Errorf("host-d5 missing %v:\n%s", ip, out)
		}
	}
	// the guest reaches both LANs directly
	ccOK(t, "host-d5", "ping -c 2 -W 2 10.10.3.13 && ping -c 2 -W 2 10.10.4.12")
}

// --- networking --------------------------------------------------------------

func ipOf(t *testing.T, vm string) string {
	t.Helper()
	ips := list(vmInfo(t)[vm]["ip"])
	if len(ips) == 0 || ips[0] == "" {
		t.Fatalf("%v has no IP", vm)
	}
	return ips[0]
}

func testPingSameLAN(t *testing.T) {
	ccOK(t, "host-a2", "ping -c 3 -W 2 "+ipOf(t, "host-a1"))
	ccOK(t, "host-b1", "ping -c 3 -W 2 10.10.2.15")
	ccOK(t, "host-c2", "ping -c 3 -W 2 10.10.3.1")
}

func testOSPF(t *testing.T) {
	// each router learns the other's LANs over lan-b
	ccEventually(t, "rtr-ab", "ip route | grep -q '10.10.4.0/24 via 10.10.2.2'", settleTimeout)
	ccEventually(t, "rtr-cd", "ip route | grep -q '10.10.1.0/24 via 10.10.2.1'", settleTimeout)
	out := ccOK(t, "rtr-ab", "ip route")
	t.Logf("rtr-ab routes:\n%s", out)
}

func testPingAcrossRouters(t *testing.T) {
	// lan-a -> lan-d traverses rtr-ab, lan-b, and rtr-cd
	ccEventually(t, "host-a1", "ping -c 3 -W 2 10.10.4.12", 2*time.Minute)
	ccEventually(t, "host-c3", "ping -c 3 -W 2 "+ipOf(t, "host-a4"), 2*time.Minute)
	out := ccOK(t, "host-a1", "traceroute -n -w 2 -q 1 10.10.4.12")
	for _, hop := range []string{"10.10.1.1", "10.10.2.2"} {
		if !strings.Contains(out, hop) {
			t.Errorf("traceroute missing hop %v:\n%s", hop, out)
		}
	}
}

func testNoBackchannelHost(t *testing.T) {
	ccEventually(t, "host-d1", "ping -c 3 -W 2 10.10.4.14", time.Minute)
	ccEventually(t, "host-b2", "ping -c 3 -W 2 10.10.4.14", time.Minute)
}

func testDNS(t *testing.T) {
	// busybox nslookup also asks for AAAA records, which minirouter's
	// dnsmasq refuses, so match the A record instead of the exit status
	ccEventually(t, "host-a1", "nslookup web.live 2>&1 | grep -q 10.10.3.11", time.Minute)
	ccEventually(t, "host-c2", "nslookup fw.live 2>&1 | grep -q 10.10.4.11", time.Minute)
	if out := ccOK(t, "host-a1", "cat /etc/resolv.conf"); !strings.Contains(out, "10.10.1.1") {
		t.Errorf("DHCP did not hand out the router as DNS server:\n%s", out)
	}
}

func testHTTPProtonuke(t *testing.T) {
	// protonuke was started on host-c1 by topology.mm
	ccEventually(t, "host-a2", "timeout 10 wget -q -O /tmp/index.html http://web.live/ && test -s /tmp/index.html", 2*time.Minute)
}

func testFirewall(t *testing.T) {
	// control: both ports answer from inside lan-d
	ccEventually(t, "host-d2", "timeout 10 wget -q -O /dev/null http://10.10.4.11:8080/passwd", time.Minute)
	ccEventually(t, "host-d2", "timeout 10 wget -q -O /dev/null http://10.10.4.11/", time.Minute)

	// through rtr-cd: 80 is open, 8080 is dropped by "router fw"
	ccEventually(t, "host-a1", "timeout 10 wget -q -O /dev/null http://10.10.4.11/", time.Minute)
	r := ccExec(t, "host-a1", "timeout 10 wget -q -O /dev/null http://10.10.4.11:8080/passwd")
	if r.ExitCode == 0 {
		t.Errorf("TCP/8080 to host-d1 should be blocked by rtr-cd")
	}
	out := ccOK(t, "rtr-cd", "iptables -S")
	if !strings.Contains(out, "10.10.4.11") || !strings.Contains(out, "8080") {
		t.Errorf("rtr-cd iptables missing rule:\n%s", out)
	}
}

func testHostTap(t *testing.T) {
	found := false
	for _, r := range mustTable(t, "tap") {
		if r["tap"] == "live-mgmt" {
			found = true
		}
	}
	if !found {
		t.Errorf("tap live-mgmt not listed")
	}
	// a guest reaches the minimega host's tap on lan-a ...
	ccEventually(t, "host-a3", "ping -c 2 -W 2 10.10.1.250", time.Minute)

	// ... and the host reaches a service on lan-c through the tap and both
	// routers (the image has no ping, so use bash's /dev/tcp)
	if out, err := inContainer(t, "ip", "route", "replace", "10.10.0.0/16", "via", "10.10.1.1", "dev", "live-mgmt"); err != nil {
		t.Fatalf("add route: %v: %s", err, out)
	}
	defer inContainer(t, "ip", "route", "del", "10.10.0.0/16")
	eventually(t, time.Minute, 3*time.Second, func() error {
		out, err := inContainer(t, "timeout", "10", "bash", "-c",
			`exec 3<>/dev/tcp/10.10.3.11/80 && printf 'GET / HTTP/1.0\r\n\r\n' >&3 && head -1 <&3`)
		if err != nil || !strings.HasPrefix(out, "HTTP/1.") {
			return fmt.Errorf("http via tap: %v: %q", err, out)
		}
		return nil
	})
}

// testCCRTunnel opens reverse tunnels: a guest listens on 4444 and the
// minimega host connects the traffic on to protonuke on host-c1.
func testCCRTunnel(t *testing.T) {
	if out, err := inContainer(t, "ip", "route", "replace", "10.10.0.0/16", "via", "10.10.1.1", "dev", "live-mgmt"); err != nil {
		t.Fatalf("add route: %v: %s", err, out)
	}
	defer inContainer(t, "ip", "route", "del", "10.10.0.0/16")

	const fetch = "timeout 10 wget -q -O /tmp/rtunnel.html http://127.0.0.1:4444/ && test -s /tmp/rtunnel.html"
	const tunnel = " 4444->10.10.3.11:80"

	if err := rtunnel("name=host-a3", "4444 10.10.3.11 80"); err != nil {
		t.Fatalf("cc rtunnel: %v", err)
	}
	ccEventually(t, "host-a3", fetch, time.Minute)
	if got := rtunnels(t, "cc rtunnel list host-a3"); !maps.Equal(got, map[string]bool{"host-a3" + tunnel: true}) {
		t.Errorf("cc rtunnel list host-a3 = %v", got)
	}

	// no client matches: the command fails
	if err := rtunnel("os=plan9", "4445 10.10.3.11 80"); err == nil || !strings.Contains(err.Error(), "no clients match filter: os=plan9") {
		t.Errorf("cc rtunnel with no matching client: err = %v", err)
	}

	// A runtime vm tag selects two clients. host-a3 already listens on 4444,
	// so only host-a3 fails, and host-a2 still opens its tunnel.
	for _, vm := range []string{"host-a2", "host-a3"} {
		mustRun(t, "vm tag "+vm+" rtunnel yes")
		defer client.run("clear vm tag " + vm + " rtunnel")
	}
	vms := vmInfo(t)
	err := rtunnel("rtunnel=yes", "4444 10.10.3.11 80")
	if err == nil || !strings.Contains(err.Error(), vms["host-a3"]["uuid"]) || strings.Contains(err.Error(), vms["host-a2"]["uuid"]) {
		t.Errorf("cc rtunnel on host-a2 and host-a3: err = %v, want an error naming host-a3 only", err)
	}
	ccEventually(t, "host-a2", fetch, time.Minute)

	want := map[string]bool{"host-a2" + tunnel: true, "host-a3" + tunnel: true}
	if got := rtunnels(t, "cc rtunnel list"); !maps.Equal(got, want) {
		t.Errorf("cc rtunnel list = %v, want %v", got, want)
	}
}

// rtunnel runs "cc rtunnel args" with the cc filter set to filter.
func rtunnel(filter, args string) error {
	if _, err := client.run("cc filter " + filter); err != nil {
		return err
	}
	defer client.run("clear cc filter")

	_, err := client.run("cc rtunnel " + args)
	return err
}

// rtunnels returns the "vm src->dst:port" entries that cmd lists.
func rtunnels(t *testing.T, cmd string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, r := range mustTable(t, cmd) {
		got[fmt.Sprintf("%v %v->%v:%v", r["vm"], r["src port"], r["dst"], r["dst port"])] = true
	}
	return got
}

var rttRe = regexp.MustCompile(`= [\d.]+/([\d.]+)/`)

func avgRTT(t *testing.T, from, to string) float64 {
	t.Helper()
	out := ccOK(t, from, "ping -c 4 -W 3 "+to+" | tail -1")
	m := rttRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("cannot parse ping output: %q", out)
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f
}

func testQoS(t *testing.T) {
	target := ipOf(t, "host-a2")
	mustRun(t, "qos add host-a2 0 delay 250ms")
	defer client.run("clear qos host-a2 all")

	if q := vmInfo(t)["host-a2"]["qos"]; !strings.Contains(q, "250ms") {
		t.Errorf("vm info qos = %q", q)
	}
	if rtt := avgRTT(t, "host-a4", target); rtt < 240 {
		t.Errorf("rtt with 250ms delay = %.1fms", rtt)
	}

	mustRun(t, "clear qos host-a2 0")
	if rtt := avgRTT(t, "host-a4", target); rtt > 150 {
		t.Errorf("rtt after clear qos = %.1fms", rtt)
	}

	// packet loss (minimega accepts 0 <= loss < 100)
	mustRun(t, "qos add host-a2 0 loss 99")
	out := ccExec(t, "host-a4", "ping -c 10 -W 1 "+target+" | tail -2").Stdout
	m := regexp.MustCompile(`(\d+) packets received`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("cannot parse ping output: %q", out)
	}
	if n, _ := strconv.Atoi(m[1]); n > 2 {
		t.Errorf("%d of 10 pings answered with 99%% loss", n)
	}
	mustRun(t, "clear qos host-a2 all")
	if q := vmInfo(t)["host-a2"]["qos"]; strings.Contains(q, "loss") {
		t.Errorf("qos still set after clear: %q", q)
	}
}

func testCapturePcap(t *testing.T) {
	host, path := artifact(t, "host-a5.pcap")
	mustRun(t, "capture pcap vm host-a5 0 "+path)
	ccOK(t, "host-a5", "ping -c 5 -W 2 10.10.1.1")
	if rows := mustTable(t, "capture"); len(rows) == 0 {
		t.Errorf("capture list empty while capturing")
	}
	mustRun(t, "capture pcap delete vm host-a5")

	// the capture must hold the pings, not just background ARP and OSPF
	if n, err := countICMPEcho(host, "10.10.1.1"); err != nil || n < 5 {
		t.Errorf("pcap has %d ICMP echo requests to 10.10.1.1, want >= 5: %v", n, err)
	}
	keep(t, host)
}

// --- VM operations -----------------------------------------------------------

func testScreenshot(t *testing.T) {
	host, path := artifact(t, "host-a1.png")
	mustRun(t, "vm screenshot host-a1 file "+path+" 800")
	b, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	// the console shows text, so a blank (one-color) frame is a failure
	if n, err := distinctColors(b); err != nil || n < 2 {
		t.Errorf("screenshot has %d colors, want a console with text: %v", n, err)
	}
	keep(t, host)

	// cirrus VGA
	host, path = artifact(t, "host-c5.png")
	mustRun(t, "vm screenshot host-c5 file "+path)
	if b, err := os.ReadFile(host); err != nil {
		t.Errorf("host-c5 screenshot failed: %v", err)
	} else if _, err := distinctColors(b); err != nil {
		t.Errorf("host-c5 screenshot is not a PNG: %v", err)
	}
}

func testSerial(t *testing.T) {
	eventually(t, 30*time.Second, 2*time.Second, func() error {
		out, err := client.text("vm serial host-a1 0 3000")
		if err != nil {
			return err
		}
		if !strings.Contains(out, "LIVE-SERIAL host-a1") {
			return fmt.Errorf("serial output: %q", out)
		}
		return nil
	})
}

func testQMP(t *testing.T) {
	out := mustText(t, `vm qmp host-a1 '{ "execute": "query-status" }'`)
	if !strings.Contains(out, "running") {
		t.Errorf("query-status: %s", out)
	}
	out = mustText(t, `vm qmp host-c3 '{ "execute": "query-name" }'`)
	if !strings.Contains(out, "return") {
		t.Errorf("query-name: %s", out)
	}
}

func testVNCRecord(t *testing.T) {
	host, path := artifact(t, "host-a1.fb")
	mustRun(t, "vnc record fb host-a1 "+path)
	time.Sleep(5 * time.Second)
	listed := false
	for _, r := range mustTable(t, "vnc") {
		if r["name"] == "host-a1" && strings.Contains(r["filename"], "host-a1.fb") {
			listed = true
		}
	}
	if !listed {
		t.Errorf("vnc does not list the recording: %v", mustTable(t, "vnc"))
	}
	mustRun(t, "vnc stop fb host-a1")
	// the recorder flushes its gzip stream as it shuts down
	eventually(t, 10*time.Second, time.Second, func() error {
		if n, err := fbChunks(host); err != nil || n == 0 {
			return fmt.Errorf("framebuffer recording has %d chunks: %v", n, err)
		}
		return nil
	})
}

func testTags(t *testing.T) {
	mustRun(t, "vm tag host-b5 stage tested")
	rows := mustTable(t, "vm tag host-b5 stage")
	if len(rows) != 1 || rows[0]["value"] != "tested" {
		t.Errorf("vm tag get = %v", rows)
	}
	res, _ := ccRun(t, "stage=tested", "exec hostname", 1, time.Minute)
	if len(res) != 1 || strings.TrimSpace(res["host-b5"].Stdout) != "host-b5" {
		t.Errorf("tag filter reached %v", keys(res))
	}
}

func testTop(t *testing.T) {
	rows := mustTable(t, "vm top 2")
	if len(rows) < len(topology) {
		t.Errorf("vm top rows = %d", len(rows))
	}
}

func testStopStart(t *testing.T) {
	mustRun(t, "vm stop host-a4")
	if s := vmInfo(t)["host-a4"]["state"]; s != "PAUSED" {
		t.Errorf("state after stop = %v", s)
	}
	mustRun(t, "vm start host-a4")
	eventually(t, 30*time.Second, 2*time.Second, func() error {
		if s := vmInfo(t)["host-a4"]["state"]; s != "RUNNING" {
			return fmt.Errorf("state = %v", s)
		}
		return nil
	})
	ccEventually(t, "host-a4", "true", time.Minute)
}

func testNetDisconnectConnect(t *testing.T) {
	ccOK(t, "host-b4", "ping -c 2 -W 2 10.10.2.15")

	mustRun(t, "vm net disconnect host-b5 0")
	if v := vmInfo(t)["host-b5"]["vlan"]; !strings.Contains(v, "disconnected") {
		t.Errorf("vlan after disconnect = %v", v)
	}
	if r := ccExec(t, "host-b4", "ping -c 2 -W 2 10.10.2.15"); r.ExitCode == 0 {
		t.Errorf("host-b5 still reachable after disconnect")
	}

	mustRun(t, "vm net connect host-b5 0 lan-b")
	ccEventually(t, "host-b4", "ping -c 2 -W 2 10.10.2.15", time.Minute)
}

func testHotplug(t *testing.T) {
	// unlike "disk create", "vm hotplug add" does not resolve paths against
	// the files directory, so pass an absolute path
	mustRun(t, "disk create qcow2 live/hotplug.qcow2 32M")
	mustRun(t, "vm hotplug add host-c3 "+mmFiles+"/live/hotplug.qcow2")

	rows := mustTable(t, "vm hotplug")
	if len(rows) == 0 || !strings.Contains(fmt.Sprint(rows), "hotplug.qcow2") {
		t.Errorf("vm hotplug list: %v", rows)
	}
	// USB mass storage appears as a SCSI disk of 32M (65536 sectors)
	ccEventually(t, "host-c3", "grep -l 65536 /sys/block/sd*/size", time.Minute)

	mustRun(t, "vm hotplug remove host-c3 all")
	ccEventually(t, "host-c3", "! grep -l 65536 /sys/block/sd*/size 2>/dev/null", time.Minute)
}

func testSave(t *testing.T) {
	mustRun(t, "vm save host-c2 live/saved-c2")
	eventually(t, 3*time.Minute, 3*time.Second, func() error {
		for _, r := range mustTable(t, "vm save") {
			if r["name"] == "host-c2" && r["status"] != "completed" {
				return fmt.Errorf("save status: %v", r)
			}
		}
		return nil
	})
	for _, ext := range []string{".state", ".hdd"} {
		eventually(t, time.Minute, 2*time.Second, func() error {
			fi, err := os.Stat(fmt.Sprintf("%v/live/saved-c2%v", mmFiles, ext))
			if err != nil {
				return err
			}
			if fi.Size() == 0 {
				return fmt.Errorf("%v is empty", ext)
			}
			return nil
		})
	}
	// the VM keeps running (or can be resumed) after the save
	if s := vmInfo(t)["host-c2"]["state"]; s != "RUNNING" {
		mustRun(t, "vm start host-c2")
	}
	ccEventually(t, "host-c2", "true", time.Minute)
}

func testKillFlushRelaunch(t *testing.T) {
	mustRun(t, "vm kill host-a5")
	eventually(t, 30*time.Second, 2*time.Second, func() error {
		if s := vmInfo(t)["host-a5"]["state"]; s != "QUIT" {
			return fmt.Errorf("state = %v", s)
		}
		return nil
	})
	mustRun(t, "vm flush")
	if _, ok := vmInfo(t)["host-a5"]; ok {
		t.Fatalf("host-a5 still present after flush")
	}

	// relaunch through the queue from a clone of a running VM's config
	// (clone needs a launched VM, so it can't be used for queued ones)
	mustRun(t, "vm config clone host-a4")
	mustRun(t, "vm config append live.role=host live.hostname=host-a6")
	mustRun(t, "vm launch kvm host-a6")
	if q := mustText(t, "ns queue"); !strings.Contains(q, "host-a6") {
		t.Errorf("host-a6 not queued: %q", q)
	}
	mustRun(t, "vm launch")
	mustRun(t, "vm start host-a6")
	mustRun(t, "clear vm config")

	eventually(t, 3*time.Minute, 5*time.Second, func() error {
		r := vmInfo(t)["host-a6"]
		if r["cc_active"] != "true" {
			return fmt.Errorf("host-a6 state=%v cc=%v", r["state"], r["cc_active"])
		}
		if ips := list(r["ip"]); len(ips) == 0 || !strings.HasPrefix(ips[0], "10.10.1.") {
			return fmt.Errorf("host-a6 ip=%v", r["ip"])
		}
		return nil
	})
	ccOK(t, "host-a6", "ping -c 2 -W 2 10.10.1.1")

	r := vmInfo(t)["host-a6"]
	if r["memory"] != "192" || !strings.Contains(r["tags"], `"lan":"a"`) || !strings.HasPrefix(list(r["vlan"])[0], "lan-a") {
		t.Errorf("host-a6 did not inherit host-a4's config: memory=%v tags=%v vlan=%v", r["memory"], r["tags"], r["vlan"])
	}
	if r["uuid"] == vmInfo(t)["host-a4"]["uuid"] {
		t.Errorf("clone kept the source UUID")
	}
}

// --- teardown ----------------------------------------------------------------

func testClearNamespace(t *testing.T) {
	if _, err := client.raw("clear namespace " + namespace); err != nil {
		t.Fatalf("clear namespace: %v", err)
	}

	for _, r := range toRowsMust(t, "namespace") {
		if r["namespace"] == namespace {
			t.Errorf("namespace %v still exists", namespace)
		}
	}

	eventually(t, time.Minute, 3*time.Second, func() error {
		out, _ := inContainer(t, "pgrep", "-c", "qemu")
		if n := strings.TrimSpace(out); n != "0" && n != "" {
			return fmt.Errorf("%v qemu processes still running", n)
		}
		out, err := inContainer(t, "ovs-vsctl", "list-ports", "mega_bridge")
		if err == nil && strings.TrimSpace(out) != "" {
			return fmt.Errorf("ports left on mega_bridge: %v", strings.Fields(out))
		}
		return nil
	})
}

func toRowsMust(t *testing.T, cmd string) []row {
	t.Helper()
	resps, err := client.raw(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return toRows(resps)
}
