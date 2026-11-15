#!/usr/bin/env bash
# Multi-host live test for "clear vlans blacklist <vlan>" (sandia-minimega/minimega#1688).
#
#   sudo OUT=dir SCENARIOS="S1 S2 ..." tests/live/multihost/run.sh <image>
#
# Starts three privileged minimega containers (mm1..mm3) from <image> on one
# docker network, meshes them with "mesh dial", and drives each scenario with
# "minimega -level=error -e" through "docker exec". Every command and its output goes to
# $OUT/transcript.txt; each check prints PASS or FAIL, and $OUT/results.md
# holds the per-scenario verdicts. VMs are KVM VMs without a disk or kernel:
# a real QEMU process with a tap on the VLAN, but no guest OS.
#
# Needs: Linux, root, docker, /dev/kvm, the openvswitch module (sch_netem for
# S6). Exit status is non-zero if any scenario fails.

set -uo pipefail

IMAGE="${1:?usage: run.sh <image>}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUT="$(mkdir -p "${OUT:-$PWD/multihost-artifacts}" && cd "${OUT:-$PWD/multihost-artifacts}" && pwd)"
SCENARIOS="${SCENARIOS:-S1 S2 S4 S5 S6 S3 S7}"
NET=mm1688
HOSTS=(mm1 mm2 mm3)
T="$OUT/transcript.txt"
: > "$T"
: > "$OUT/decisive.tsv"
mkdir -p "$OUT/logs"

say() { echo "$*" | tee -a "$T"; }
now() { date +%s.%N; }

# mm HOST COMMAND: run one minimega command, stdout+stderr, bounded
mm() {
	local h=$1; shift
	docker exec "$h" timeout "${MM_T:-60}" minimega -level=error -e "$*" 2>&1
	local rc=$?
	[[ $rc -eq 124 ]] && echo "<<TIMEOUT after ${MM_T:-60}s>>"
	return 0
}

# run HOST COMMAND: like mm, logs to the transcript, leaves output in $OUT_
run() {
	local h=$1; shift
	LAST_CMD="$h> $*"
	echo "[$(date +%H:%M:%S.%3N)] $h> $*" >> "$T"
	OUT_="$(mm "$h" "$@")"
	[[ -n "$OUT_" ]] && sed 's/^/    | /' <<< "$OUT_" >> "$T"
	return 0
}

# vminfo HOST NS COLUMNS: sorted CSV rows "host,col1,col2..."
vminfo() {
	run "$1" ".csv true .header false .columns $3 namespace $2 vm info"
	OUT_="$(sort <<< "$OUT_")"
}

declare -A VERDICT
SC=""
pass() { say "  PASS [$SC] $1"; }
fail() {
	say "  FAIL [$SC] $1"
	say "       want: $2"
	say "       got:  $(head -c 2000 <<< "$OUT_")"
	VERDICT[$SC]=FAIL
}
note() { say "  NOTE [$SC] $*"; }
# decisive: record the last command and output for the report table
decisive() { printf '%s\t%s\t%s\n' "$SC" "$LAST_CMD" "$(tr '\n' ' ' <<< "$OUT_")" >> "$OUT/decisive.tsv"; }
expect_eq() { if [[ "$OUT_" == "$2" ]]; then pass "$1"; else fail "$1" "exactly '$2'"; fi; }
expect_re() { if [[ "$OUT_" =~ $2 ]]; then pass "$1"; else fail "$1" "match /$2/"; fi; }
expect_no_re() { if [[ "$OUT_" =~ $2 ]]; then fail "$1" "no match /$2/"; else pass "$1"; fi; }

# wait_for HOST COMMAND REGEX [TRIES]: poll every second
wait_for() {
	local h=$1 cmd=$2 re=$3 tries=${4:-60}
	for _ in $(seq "$tries"); do
		OUT_="$(mm "$h" "$cmd")"
		[[ "$OUT_" =~ $re ]] && { echo "[$(date +%H:%M:%S.%3N)] (waited) $h> $cmd => /$re/ ok" >> "$T"; return 0; }
		sleep 1
	done
	echo "[$(date +%H:%M:%S.%3N)] (waited) $h> $cmd => /$re/ never matched" >> "$T"
	sed 's/^/    | /' <<< "$OUT_" >> "$T"
	return 1
}

# wait_count HOST NS REGEX N [TRIES]: poll until N vm info rows match
wait_count() {
	local c
	for _ in $(seq "${5:-60}"); do
		c=$(mm "$1" ".csv true .header false .columns name namespace $2 vm info" | grep -cE "$3")
		[[ $c -ge $4 ]] && { echo "[$(date +%H:%M:%S.%3N)] (waited) $2: $c VMs match /$3/" >> "$T"; return 0; }
		sleep 1
	done
	echo "[$(date +%H:%M:%S.%3N)] (waited) $2: only $c VMs match /$3/" >> "$T"
	return 1
}

blacklist_has() { run "$1" "vlans blacklist"; [[ "$OUT_" =~ (^|$'\n')"$1"\ +\|\ $2($'\n'|$) ]]; }

# new_ns NS: namespace on mm1 spanning all three hosts
new_ns() {
	run mm1 "namespace $1 ns add-hosts mm1,mm2,mm3"
	run mm1 "namespace $1 ns hosts"
	expect_eq "namespace $1 spans mm1-mm3" "mm1: mm[1-3]"
}

# kill_flush NS VM: kill, wait for QUIT, flush, wait until gone
kill_flush() {
	run mm1 "namespace $1 vm kill $2"
	wait_for mm1 ".csv true .header false .columns name,state namespace $1 vm info" "$2,QUIT" 30
	run mm1 "namespace $1 vm flush"
	wait_for mm1 ".csv true .header false .columns name namespace $1 vm info" "^$" 30
}

cleanup_ns() {
	run mm1 "namespace $1 vm kill all"
	run mm1 "namespace $1 vm flush"
	run mm1 "namespace $1 ns flush"
	run mm1 "clear namespace $1"
}

start_node() {
	local h=$1
	mkdir -p "$OUT/logs/$h"
	docker rm -f "$h" >/dev/null 2>&1
	docker run -d --name "$h" --hostname "$h" --network "$NET" \
		--privileged --cap-add ALL \
		-v /dev:/dev -v /lib/modules:/lib/modules:ro \
		-v "$OUT/logs/$h:/var/log/minimega" \
		-e MM_DEGREE=0 -e MM_LOGLEVEL=debug \
		-e MM_LOGFILE=/var/log/minimega/minimega.log \
		"${@:2}" "$IMAGE" >/dev/null
}

wait_node() {
	for _ in $(seq 60); do
		mm "$1" version | grep -q '^'"$1"': minimega' && return 0
		sleep 2
	done
	return 1
}

mesh_up() {
	for h in mm2 mm3; do run mm1 "mesh dial $h"; done
	for h in "${HOSTS[@]}"; do
		wait_for "$h" "mesh status" "$h +\| 3 " 60 || return 1
	done
	run mm1 "mesh list"
}

finish() {
	say ""
	say "== collecting logs"
	for h in "${HOSTS[@]}" mmt; do
		docker logs "$h" > "$OUT/logs/$h.container.log" 2>&1 || true
		docker exec "$h" ovs-vsctl show > "$OUT/logs/$h.ovs.txt" 2>&1 || true
		docker rm -f "$h" >/dev/null 2>&1 || true
	done
	docker network rm "$NET" >/dev/null 2>&1 || true
	chmod -R a+rX "$OUT" 2>/dev/null || true
}
trap finish EXIT

# ---------------------------------------------------------------------------

s1() {
	# PR's own minitest, in a fresh container with a writable copy of tests/
	rm -rf "$OUT/minitest" && mkdir -p "$OUT/minitest" && cp -r "$SRC/tests/." "$OUT/minitest/"
	start_node mmt -v "$OUT/minitest:/tests"
	if wait_node mmt; then
		echo "[$(date +%H:%M:%S.%3N)] mmt\$ minitest -dir /tests -run '^vlans\$'" >> "$T"
		OUT_="$(docker exec mmt timeout 300 /opt/minimega/bin/minitest -dir /tests -run '^vlans$' 2>&1)"
		LAST_CMD="mmt\$ minitest -dir /tests -run ^vlans\$"
		sed 's/^/    | /' <<< "$OUT_" >> "$T"
		expect_no_re "minitest reports no mismatch" "got != want"
		if [[ -f "$OUT/minitest/vlans.got" ]]; then
			OUT_="$(diff "$OUT/minitest/vlans.want" "$OUT/minitest/vlans.got" && echo IDENTICAL)"
			LAST_CMD="diff tests/vlans.want tests/vlans.got"
			sed 's/^/    | /' <<< "$OUT_" >> "$T"
			expect_eq "vlans.got == vlans.want" "IDENTICAL"
			decisive
		else
			OUT_="(no vlans.got)"; fail "minitest wrote vlans.got" "a vlans.got file"
		fi
	else
		OUT_="mmt did not start"; fail "minitest container starts" "minimega answering"
	fi
	docker rm -f mmt >/dev/null 2>&1

	# mm1 is not meshed yet: a true single node
	local ns=s1
	run mm1 "namespace $ns ns hosts"
	expect_eq "s1 is single-node" "mm1: mm1"
	run mm1 "namespace $ns vm config memory 128"
	run mm1 "namespace $ns vm config net 333"
	run mm1 "namespace $ns vm launch kvm s1vm"
	run mm1 "namespace $ns vm start s1vm"
	run mm1 "namespace $ns clear vm config net"
	vminfo mm1 $ns name,state,vlan,tap
	expect_re "s1vm runs on VLAN 333" "^mm1,s1vm,RUNNING,\[333\],\[mega_tap[0-9]+\]$"
	local tap; tap="$(sed -E 's/.*\[(mega_tap[0-9]+)\]$/\1/' <<< "$OUT_")"
	OUT_="$(docker exec mm1 ovs-vsctl --bare --columns=tag find port name="$tap" 2>&1)"
	LAST_CMD="mm1\$ ovs-vsctl --bare --columns=tag find port name=$tap"
	echo "[$(date +%H:%M:%S.%3N)] $LAST_CMD" >> "$T"; sed 's/^/    | /' <<< "$OUT_" >> "$T"
	expect_eq "OVS tags $tap with 333" "333"

	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "running VM blocks the clear" "Error (mm1): VLAN 333 is in use by VM s1vm on mm1 in namespace s1"
	decisive

	run mm1 "namespace $ns vm kill s1vm"
	wait_for mm1 ".csv true .header false .columns name,state namespace $ns vm info" "s1vm,QUIT" 30
	run mm1 "namespace $ns clear vlans blacklist 333"
	note "after kill, before flush (docs say kill and flush): $OUT_"
	if [[ "$OUT_" == "" ]]; then run mm1 "vlans blacklist 333"; fi

	run mm1 "namespace $ns vm flush"
	vminfo mm1 $ns name
	expect_eq "no VMs left after flush" ""
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "clear succeeds after kill+flush" ""
	decisive
	run mm1 "vlans blacklist"
	expect_no_re "333 no longer blacklisted" "\| 333"

	# a host tap on the VLAN also blocks it
	run mm1 "namespace $ns tap create 333 name s1tap"
	expect_eq "tap create 333" "mm1: s1tap"
	blacklist_has mm1 333 && pass "tap create re-blacklists 333" || fail "tap create re-blacklists 333" "333 listed"
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "host tap blocks the clear" "Error (mm1): VLAN 333 is in use by host tap s1tap"
	decisive
	run mm1 "namespace $ns tap delete s1tap"
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "clear succeeds after tap delete" ""

	# argument checks
	run mm1 "clear vlans blacklist 333"
	expect_eq "second clear: not blacklisted" "Error (mm1): VLAN 333 is not blacklisted"
	run mm1 "clear vlans blacklist 4096"
	expect_eq "out of range" "Error (mm1): invalid VLAN (0 <= vlan < 4096)"
	cleanup_ns $ns
}

s2() {
	local ns=s2
	new_ns $ns
	run mm1 "namespace $ns vm config memory 128"
	run mm1 "namespace $ns vm config net 333"
	run mm1 "namespace $ns vm config schedule mm3"
	run mm1 "namespace $ns vm launch kvm s2vm"
	run mm1 "namespace $ns vm start s2vm"
	run mm1 "namespace $ns clear vm config"
	vminfo mm1 $ns name,state,vlan,tap
	expect_re "s2vm runs only on mm3, VLAN 333" "^mm3,s2vm,RUNNING,\[333\],\[mega_tap[0-9]+\]$"
	local tap; tap="$(sed -E 's/.*\[(mega_tap[0-9]+)\]$/\1/' <<< "$OUT_")"
	OUT_="$(docker exec mm3 ovs-vsctl --bare --columns=tag find port name="$tap" 2>&1)"
	LAST_CMD="mm3\$ ovs-vsctl --bare --columns=tag find port name=$tap"
	echo "[$(date +%H:%M:%S.%3N)] $LAST_CMD" >> "$T"; sed 's/^/    | /' <<< "$OUT_" >> "$T"
	expect_eq "OVS on mm3 tags $tap with 333" "333"
	blacklist_has mm1 333 && pass "333 blacklisted on mm1" || fail "333 blacklisted on mm1" "333 listed"

	run mm2 "vlans blacklist"; local bl2="$OUT_"
	run mm3 "vlans blacklist"; local bl3="$OUT_"

	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "peer VM blocks the clear on mm1" "Error (mm1): VLAN 333 is in use by VM s2vm on mm3 in namespace s2"
	decisive
	run mm1 "namespace $ns vm config net 333"
	expect_eq "hold released after refusal (vm config net 333 works)" ""
	run mm1 "namespace $ns clear vm config net"

	kill_flush $ns s2vm
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "clear succeeds on mm1 after kill+flush" ""
	decisive
	run mm1 "vlans blacklist"
	expect_no_re "333 gone from mm1 blacklist" "\| 333"
	run mm2 "vlans blacklist"
	expect_eq "mm2 blacklist unchanged (per-node)" "$bl2"
	run mm3 "vlans blacklist"
	expect_eq "mm3 blacklist unchanged (per-node)" "$bl3"
	cleanup_ns $ns
}

s4() {
	local ns=s4
	new_ns $ns
	run mm1 "namespace $ns vm config memory 128"
	run mm1 "namespace $ns vm config net 333 333"
	run mm1 "namespace $ns vm config schedule mm3"
	run mm1 "namespace $ns vm launch kvm s4vm"
	run mm1 "namespace $ns clear vm config"
	run mm1 "namespace $ns vm net bond s4vm 0,1 active-backup off"
	expect_eq "vm net bond on mm3" ""
	vminfo mm1 $ns name,vlan,bond
	expect_re "bond created on mm3" "^mm3,s4vm,\"\[333, 333\]\",\[mega_bond[^]]*\]$"
	run mm3 "namespace $ns vm info"
	OUT_="$(docker exec mm3 ovs-vsctl --bare --columns=name,tag find port tag=333 2>&1)"
	LAST_CMD="mm3\$ ovs-vsctl find port tag=333"
	echo "[$(date +%H:%M:%S.%3N)] $LAST_CMD" >> "$T"; sed 's/^/    | /' <<< "$OUT_" >> "$T"
	expect_re "OVS on mm3 has a bond port on 333" "bond"

	run mm1 "namespace $ns vm net disconnect s4vm 0"
	run mm1 "namespace $ns vm net disconnect s4vm 1"
	vminfo mm1 $ns name,vlan,bond
	expect_re "no NIC on 333, bond kept" "^mm3,s4vm,\"\[disconnected, disconnected\]\",\[mega_bond[^]]*\]$"
	decisive

	blacklist_has mm1 333 || run mm1 "vlans blacklist 333"
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_eq "bond alone blocks the clear on mm1 (Created survived gob)" "Error (mm1): VLAN 333 is in use by VM s4vm on mm3 in namespace s4"
	decisive

	run mm1 "namespace $ns clear vm net bond s4vm"
	note "clear vm net bond after both members were disconnected: '${OUT_}'"
	vminfo mm1 $ns name,vlan,bond
	if [[ "$OUT_" =~ \[\]$ ]]; then
		run mm1 "namespace $ns clear vlans blacklist 333"
		expect_eq "clear succeeds once the bond is gone" ""
	else
		kill_flush $ns s4vm
		run mm1 "namespace $ns clear vlans blacklist 333"
		expect_eq "clear succeeds after kill+flush" ""
	fi
	decisive
	cleanup_ns $ns
}

s5() {
	local ns=s5 n=48
	new_ns $ns
	run mm1 "namespace $ns ns queueing true"
	run mm1 "namespace $ns vm config memory 64"
	run mm1 "namespace $ns vm config net 333"
	run mm1 "namespace $ns vm launch kvm s5a[1-16]"
	run mm1 "namespace $ns vm launch kvm s5b[1-16]"
	run mm1 "namespace $ns vm launch kvm s5c[1-16]"
	run mm1 "namespace $ns clear vm config"
	run mm1 "namespace $ns clear vlans blacklist 333"
	expect_re "queue blocks the clear" "^Error \(mm1\): VLAN 333 is in use by queued VMs s5a1,s5a2,[^ ]*,s5a16 in namespace s5, queued VMs s5b1,[^ ]*,s5b16 in namespace s5, queued VMs s5c1,[^ ]*,s5c16 in namespace s5$"

	# cross-host case the PR documents as unprotected: mm2 has 333 blacklisted
	# by hand, knows nothing of mm1's queue
	run mm2 "vlans blacklist 333"
	run mm2 "clear vlans blacklist 333"
	note "cross-host: clear on mm2 while the VMs are only queued on mm1 (documented: queue is checked on this node only): '${OUT_:-<success>}'"
	decisive
	run mm2 "vlans blacklist 333"

	docker exec mm1 rm -f /tmp/s5.stop /tmp/s5.log
	docker exec -d mm1 bash -c 'while [ ! -e /tmp/s5.stop ]; do t=$(date +%s.%N); o=$(timeout 30 minimega -level=error -e "clear vlans blacklist 333" 2>&1 | tr "\n" " "); echo "$t|$(date +%s.%N)|$o" >> /tmp/s5.log; sleep 0.05; done'
	sleep 1
	local t0 t1 pid
	t0=$(now)
	echo "[$(date +%H:%M:%S.%3N)] mm1> namespace $ns ns schedule   (background)" >> "$T"
	mm mm1 "namespace $ns ns schedule" > "$OUT/s5-schedule.out" &
	pid=$!
	sleep 0.3
	run mm2 "clear vlans blacklist 333"
	note "cross-host: clear on mm2 during mm1's schedule (unprotected per PR docs): '${OUT_:-<success>}'"
	decisive
	wait $pid
	t1=$(now)
	sed 's/^/    | /' "$OUT/s5-schedule.out" >> "$T"
	OUT_="$(cat "$OUT/s5-schedule.out")"
	expect_eq "ns schedule returned cleanly" ""
	wait_count mm1 $ns ',s5[abc][0-9]+$' $n 60
	sleep 2
	docker exec mm1 touch /tmp/s5.stop
	sleep 1
	docker exec mm1 cat /tmp/s5.log > "$OUT/s5-clear-loop.log"
	say "  schedule window: $t0 .. $t1 ($(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2fs", b-a}'))"

	local total refused inwin inwin_ref other
	total=$(wc -l < "$OUT/s5-clear-loop.log")
	refused=$(grep -c '|Error (mm1): VLAN 333 is in use by ' "$OUT/s5-clear-loop.log")
	inwin=$(awk -F'|' -v a="$t0" -v b="$t1" '$2>=a && $1<=b' "$OUT/s5-clear-loop.log" | wc -l)
	inwin_ref=$(awk -F'|' -v a="$t0" -v b="$t1" '$2>=a && $1<=b' "$OUT/s5-clear-loop.log" | grep -c 'is in use by ')
	other=$(grep -v '|Error (mm1): VLAN 333 is in use by ' "$OUT/s5-clear-loop.log" | head -5)
	say "  clear attempts on mm1: total=$total refused=$refused; overlapping the schedule: $inwin (refused $inwin_ref)"
	say "  users named during the window:"
	awk -F'|' -v a="$t0" -v b="$t1" '$2>=a && $1<=b {print $3}' "$OUT/s5-clear-loop.log" \
		| grep -oE '(launching VMs|queued VMs|VM s5[abc][0-9]+ on mm[0-9])' | sed -E 's/VM s5[abc][0-9]+ on/VM on/' \
		| sort | uniq -c | sed 's/^/      /' | tee -a "$T"
	LAST_CMD="mm1 loop: clear vlans blacklist 333 every 50ms during ns schedule"
	OUT_="total=$total refused=$refused overlapping=$inwin refused_overlapping=$inwin_ref"
	if [[ $total -eq $refused && $total -gt 0 ]]; then pass "every clear attempt on mm1 refused"; else fail "every clear attempt on mm1 refused" "all refused; others: $other"; fi
	if [[ $inwin -eq 0 ]]; then
		note "INCONCLUSIVE: no attempt overlapped the schedule window"; VERDICT[$SC]=${VERDICT[$SC]:-INCONCLUSIVE}
	fi
	decisive
	blacklist_has mm1 333 && pass "333 still blacklisted on mm1" || fail "333 still blacklisted on mm1" "333 listed"
	vminfo mm1 $ns name,state,vlan
	OUT_="$(grep -cE ',s5[abc][0-9]+,(BUILDING|PAUSED|RUNNING),\[333\]$' <<< "$OUT_")"
	expect_eq "all $n VMs up on 333" "$n"
	vminfo mm1 $ns name
	OUT_="$(cut -d, -f1 <<< "$OUT_" | sort | uniq -c | tr -s ' ' | tr '\n' ' ')"
	note "VMs per host: $OUT_"
	run mm2 "vlans blacklist"
	note "mm2 blacklist after its own clear (mm2 runs s5 VMs on 333): $(tr '\n' ' ' <<< "$OUT_")"
	cleanup_ns $ns
}

s6_once() {
	local ns=$1 late
	new_ns $ns
	run mm1 "namespace $ns ns queueing true"
	run mm1 "namespace $ns vm config memory 64"
	run mm1 "namespace $ns vm launch kvm ${ns}a[1-6]"
	local t0 t1 tl0 tl1 pid
	t0=$(now)
	echo "[$(date +%H:%M:%S.%3N)] mm1> namespace $ns ns schedule   (background, client 1)" >> "$T"
	mm mm1 "namespace $ns ns schedule" > "$OUT/$ns-schedule.out" &
	pid=$!
	sleep 0.5
	tl0=$(now)
	run mm1 "namespace $ns vm launch kvm ${ns}late[1-2]"
	tl1=$(now)
	expect_eq "late batch queued from client 2" ""
	local still_running=no
	kill -0 $pid 2>/dev/null && still_running=yes
	wait $pid
	t1=$(now)
	sed 's/^/    | /' "$OUT/$ns-schedule.out" >> "$T"
	say "  schedule $t0..$t1 ($(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2fs", b-a}')), late launch $tl0..$tl1 (+$(awk -v a="$t0" -v b="$tl1" 'BEGIN{printf "%.2fs", b-a}')), schedule still running after late queue: $still_running"
	if [[ $still_running != yes ]]; then
		note "INCONCLUSIVE: the late batch was not queued while the schedule ran"
		VERDICT[$SC]=${VERDICT[$SC]:-INCONCLUSIVE}
	fi
	vminfo mm1 $ns name
	OUT_="$(grep -c ",${ns}a[0-9]" <<< "$OUT_")"
	expect_eq "first batch launched (6 VMs)" "6"
	run mm1 "namespace $ns ns queue"
	local q="$OUT_"
	# kept: still queued (the PR's claim); joined: launched by the first
	# schedule (the old code read the live queue); dropped: neither
	vminfo mm1 $ns name
	local joined; joined=$(grep -c ",${ns}late[12]$" <<< "$OUT_")
	if [[ "$q" =~ Names:\ ${ns}late\[1-2\] ]]; then late=kept
	elif [[ $joined -eq 2 ]]; then late=joined
	else late=dropped; fi
	OUT_="late batch $late; late VMs launched by first schedule: $joined; ns queue: $(grep -E '^(mm1: )?(VMs|Names):' <<< "$q" | tr '\n' ' ')"
	LAST_CMD="mm1> namespace $ns ns queue"
	say "  RESULT $ns: late batch $late"
	if [[ $late == kept ]]; then pass "late batch still queued"; else fail "late batch still queued" "Names: ${ns}late[1-2]"; fi
	decisive
	S6_COUNTS+="$late "
	if [[ $late == kept ]]; then
		run mm1 "namespace $ns ns schedule"
		expect_eq "second schedule runs" ""
		vminfo mm1 $ns name
		OUT_="$(grep -c ",${ns}late[12]$" <<< "$OUT_")"
		expect_eq "second schedule launches the late batch (2 VMs)" "2"
	fi
	cleanup_ns $ns
}

s6() {
	S6_COUNTS=""
	# Delay mm3's packets by 2s so that the window between the queue snapshot
	# and the launch is wide: hostStats and globalVMs each wait for mm3. The
	# late "vm launch" (whose name check also waits for mm3) then lands in
	# that window, the case where the old code cleared the whole queue.
	if docker exec mm3 tc qdisc add dev eth0 root netem delay 2000ms 2>>"$T"; then
		note "netem: mm3 eth0 delayed 2000ms"
	else
		note "netem unavailable: timing not widened, late batch may land after the window"
	fi
	for i in 1 2 3; do s6_once "s6x$i"; done
	docker exec mm3 tc qdisc del dev eth0 root 2>>"$T"
	say "  S6 late batches over 3 runs: $S6_COUNTS"
}

s3() {
	local ns=s3
	new_ns $ns
	run mm1 "vlans blacklist 333"
	vminfo mm1 $ns name,vlan
	expect_eq "baseline: no VMs in s3" ""

	# (a) peer frozen, default mesh timeout (infinite): record only
	run mm1 "mesh timeout"
	note "mesh timeout: $OUT_"
	docker pause mm3 >/dev/null
	say "  docker pause mm3"
	echo "[$(date +%H:%M:%S.%3N)] mm1> clear vlans blacklist 333   (background, mm3 paused)" >> "$T"
	MM_T=300 mm mm1 "clear vlans blacklist 333" > "$OUT/s3a-clear.out" &
	local pid=$!
	sleep 8
	local hung=no; kill -0 $pid 2>/dev/null && hung=yes
	note "(a) clear still waiting after 8s with mm3 paused and no mesh timeout: $hung"
	run mm1 "namespace s3tmp vm config net 333"
	note "(a) vm config net 333 while the clear waits: '${OUT_}'"
	decisive
	run mm1 "version"
	note "(a) daemon answers other commands meanwhile: '${OUT_}'"
	docker unpause mm3 >/dev/null
	say "  docker unpause mm3"
	wait $pid
	sed 's/^/    | /' "$OUT/s3a-clear.out" >> "$T"
	note "(a) the waiting clear returned after unpause: '$(cat "$OUT/s3a-clear.out")'"
	run mm1 "clear namespace s3tmp"
	blacklist_has mm1 333 || run mm1 "vlans blacklist 333"
	# the pause can make mm1 drop mm3 from the mesh: rejoin before (b)
	run mm1 "mesh status"
	wait_for mm1 "mesh status" "mm1 +\| 3 " 3 || { run mm1 "mesh dial mm3"; wait_for mm1 "mesh status" "mm1 +\| 3 " 60; }
	run mm1 "mesh status"

	# (b) peer frozen with a mesh timeout: must fail and release the hold
	run mm1 "mesh timeout 5"
	docker pause mm3 >/dev/null
	say "  docker pause mm3"
	run mm1 "clear vlans blacklist 333"
	expect_re "frozen peer: clear fails" "^Error \(mm1\): unable to check if VLAN 333 is in use: namespace [^:]+: .*mm3"
	decisive
	blacklist_has mm1 333 && pass "333 stays blacklisted" || fail "333 stays blacklisted" "333 listed"
	run mm1 "namespace $ns vm config net 333"
	expect_eq "hold released (vm config net 333 works)" ""
	run mm1 "namespace $ns clear vm config net"
	docker unpause mm3 >/dev/null
	say "  docker unpause mm3"
	run mm1 "mesh timeout 0"
	sleep 6
	wait_for mm1 "mesh status" "mm1 +\| 3 " 3 || { run mm1 "mesh dial mm3"; wait_for mm1 "mesh status" "mm1 +\| 3 " 60; }
	run mm1 "mesh status"

	# (c) peer stopped while still in ns hosts
	docker stop mm3 >/dev/null
	say "  docker stop mm3"
	wait_for mm1 "mesh status" "mm1 +\| 2 " 30 || note "mesh still lists mm3"
	run mm1 "mesh status"
	run mm1 "namespace $ns ns hosts"
	expect_eq "mm3 still in s3 hosts" "mm1: mm[1-3]"
	run mm1 "clear vlans blacklist 333"
	expect_re "stopped peer: clear fails naming mm3" "^Error \(mm1\): unable to check if VLAN 333 is in use: namespace s3: .*mm3"
	decisive
	blacklist_has mm1 333 && pass "333 stays blacklisted" || fail "333 stays blacklisted" "333 listed"
	run mm1 "namespace $ns vm config net 333"
	expect_eq "hold released (vm config net 333 works)" ""
	run mm1 "namespace $ns clear vm config net"
	run mm1 "namespace $ns ns del-hosts mm3"
	run mm1 "namespace $ns ns hosts"
	run mm1 "clear vlans blacklist 333"
	expect_eq "clear succeeds after ns del-hosts mm3" ""
	decisive
	run mm1 "clear namespace $ns"

	# bring mm3 back for S7
	docker start mm3 >/dev/null
	wait_node mm3 && run mm1 "mesh dial mm3"
	wait_for mm1 "mesh status" "mm1 +\| 3 " 60 || note "mm3 did not rejoin"
}

s7() {
	local ns=s7 dur=${S7_SECONDS:-90}
	new_ns $ns
	run mm1 "namespace $ns vm config memory 64"
	run mm1 "namespace $ns vm config net 333"
	run mm1 "namespace $ns vm config schedule mm1"
	run mm1 "namespace $ns vm launch kvm s7base"
	run mm1 "namespace $ns clear vm config"
	run mm1 "namespace $ns ns queueing true"
	for h in "${HOSTS[@]}"; do docker exec "$h" rm -f /tmp/s7.stop; done
	local pids1; pids1="$(docker exec mm1 cat /tmp/minimega/minimega.pid 2>/dev/null)"

	# each loop: one command per line, logs "<rc> <cmd> => <output>"
	local loops=(
		"mm1|ns del-hosts mm2;ns add-hosts mm2;ns del-hosts mm3;ns add-hosts mm3"
		"mm1|vm config clone s7base;vm config save c\$i;vm config net 333 444;clear vm config;vm config restore c\$i;clear vm config"
		"mm1|vm launch kvm s7q\$i;ns schedule dry-run;ns schedule mv s7q\$i mm2;ns schedule dump;ns flush"
		"mm1|vm info;ns queue;vm info"
		"mm1|clear vlans blacklist 333"
		"mm1|vlans blacklist 444;clear vlans blacklist 444"
		"mm2|vm info;clear vlans blacklist 333"
		"mm1|vm launch kvm s7r\$i;ns schedule;vm kill s7r\$i;vm flush"
	)
	local k=0
	for l in "${loops[@]}"; do
		local h=${l%%|*} cmds=${l#*|}
		docker exec -d "$h" bash -c '
			set -o pipefail; i=0; IFS=";" read -ra C <<< "$1"
			while [ ! -e /tmp/s7.stop ]; do
				i=$((i+1))
				for c in "${C[@]}"; do
					c=${c//\$i/$i}
					o=$(timeout 30 minimega -level=error -e "namespace '"$ns"' $c" 2>&1 | tr "\n" " "); rc=$?
					echo "$rc|$c|$o" >> /tmp/s7-'"$k"'.log
				done
			done' _ "$cmds"
		k=$((k+1))
	done
	say "  soak: ${#loops[@]} loops for ${dur}s"
	sleep "$dur"
	for h in "${HOSTS[@]}"; do docker exec "$h" touch /tmp/s7.stop; done
	sleep 35
	k=0
	for l in "${loops[@]}"; do
		docker exec "${l%%|*}" cat /tmp/s7-$k.log > "$OUT/s7-loop$k.log" 2>/dev/null
		say "  loop $k (${l}): $(wc -l < "$OUT/s7-loop$k.log") commands, $(grep -c '^124|' "$OUT/s7-loop$k.log") timeouts"
		k=$((k+1))
	done
	OUT_="$(cat "$OUT"/s7-loop*.log | grep -c '^124|')"
	LAST_CMD="soak: count of commands that hit the 30s timeout"
	expect_eq "no command hung" "0"
	decisive
	OUT_="$(grep -c '|clear vlans blacklist 333|$' "$OUT/s7-loop4.log")"
	expect_eq "333 never freed while s7base runs on it" "0"
	OUT_="$(grep -hiE 'concurrent map|fatal error|panic|data race' "$OUT"/logs/mm*/minimega.log "$OUT"/s7-loop*.log | head -5)"
	LAST_CMD="grep 'concurrent map|fatal error|panic|data race' in logs"
	expect_eq "no crash markers in logs" ""
	decisive
	for h in "${HOSTS[@]}"; do
		MM_T=10 run "$h" "version"
		expect_re "$h daemon answers" "^$h: minimega"
	done
	OUT_="$(docker exec mm1 cat /tmp/minimega/minimega.pid 2>/dev/null)"
	expect_eq "mm1 minimega pid unchanged" "$pids1"
	run mm1 "namespace $ns .csv true .header false .columns name,state vm info"
	cleanup_ns $ns
}

# ---------------------------------------------------------------------------

say "== image: $IMAGE ($(docker image inspect "$IMAGE" --format '{{.Id}}'))"
say "== scenarios: $SCENARIOS"
modprobe openvswitch 2>/dev/null || true
docker network rm "$NET" >/dev/null 2>&1
docker network create "$NET" >/dev/null || { echo "network create failed"; exit 1; }
for h in "${HOSTS[@]}"; do start_node "$h"; done
for h in "${HOSTS[@]}"; do
	wait_node "$h" || { say "FATAL: $h did not start"; docker logs "$h" 2>&1 | tail -30 | tee -a "$T"; exit 1; }
	run "$h" "version"; say "  $OUT_"
done

declare -a ORDER
for s in $SCENARIOS; do
	if [[ $s != S1 && -z "${MESHED:-}" ]]; then
		SC=S0
		say ""; say "== S0: mesh mm1-mm3"
		mesh_up || { say "FATAL: mesh did not converge"; exit 1; }
		MESHED=1
	fi
	SC=$s
	ORDER+=("$s")
	VERDICT[$s]=""
	say ""; say "== $s ($(date -u +%H:%M:%SZ))"
	fn=${s,,}
	if declare -F "$fn" >/dev/null; then "$fn"; else say "  unknown scenario $s"; VERDICT[$s]=FAIL; fi
	[[ -z "${VERDICT[$s]}" ]] && VERDICT[$s]=PASS
	say "== $s: ${VERDICT[$s]}"
done

status=0
{
	echo "| scenario | verdict |"
	echo "|---|---|"
	for s in "${ORDER[@]}"; do
		echo "| $s | ${VERDICT[$s]} |"
		[[ ${VERDICT[$s]} == FAIL ]] && status=1
	done
} > "$OUT/results.md"
say ""
cat "$OUT/results.md" | tee -a "$T"
[[ -n "${GITHUB_STEP_SUMMARY:-}" ]] && { echo "### #1688 multi-host live test ($IMAGE)"; cat "$OUT/results.md"; } >> "$GITHUB_STEP_SUMMARY"
exit $status
