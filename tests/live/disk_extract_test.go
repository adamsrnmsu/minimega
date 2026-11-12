// Copyright 2026 National Technology & Engineering Solutions of Sandia, LLC (NTESS).
// Under the terms of Contract DE-NA0003525 with NTESS, the U.S. Government retains certain
// rights in this software.

//go:build live

package live

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// testDiskExtract runs "disk extract" and "disk inject" on a real nbd device.
// The image is built on the host: one ext4 partition that holds a file, a
// directory, a FIFO, its own /etc/hostname, and symlinks that point outside
// the image.
func testDiskExtract(t *testing.T) {
	work := filepath.Join(mmFiles, namespace, "extract")
	if err := os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(work, "root")
	for path, data := range map[string]string{
		"hello.txt":    "hello from the image\n",
		"etc/hostname": "image-hostname\n",
		"dir/a.txt":    "a\n",
	} {
		writeFile(t, filepath.Join(root, path), data)
	}
	symlink(t, "/etc/hostname", filepath.Join(root, "abs-link"))
	symlink(t, "../../../../etc", filepath.Join(root, "up-link"))
	if err := os.MkdirAll(filepath.Join(root, "special"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "special", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, image := filepath.Join(work, "extract.img"), filepath.Join(work, "extract.qcow2")
	runHost(t, "truncate", "-s", "64M", raw)
	runHost(t, "sh", "-c", "echo 'start=2048, type=83' | sfdisk -q "+raw)
	runHost(t, "mkfs.ext4", "-q", "-F", "-E", "offset=1048576", "-d", root, raw, "62M")
	if out, err := inContainer(t, "qemu-img", "convert", "-O", "qcow2", raw, image); err != nil {
		t.Fatalf("qemu-img convert: %v: %s", err, out)
	}
	before := sha(t, image)

	out := filepath.Join(work, "out")
	mustRun(t, fmt.Sprintf("disk extract %v files /hello.txt:%v/hello.txt /dir/:%v/dir /abs-link:%v/abs-link /up-link/hostname:%v/up-hostname",
		image, out, out, out, out))

	for path, want := range map[string]string{
		"hello.txt":   "hello from the image\n",
		"dir/a.txt":   "a\n",
		"up-hostname": "image-hostname\n", // resolved in the image, not the host
	} {
		if got := readFile(t, filepath.Join(out, path)); got != want {
			t.Errorf("extracted %v = %q, want %q", path, got, want)
		}
	}
	if target, err := os.Readlink(filepath.Join(out, "abs-link")); err != nil || target != "/etc/hostname" {
		t.Errorf("extracted abs-link = %q, %v; want a symlink to /etc/hostname", target, err)
	}

	if _, err := client.run(fmt.Sprintf("disk extract %v files /special:%v/special", image, out)); err == nil || !strings.Contains(err.Error(), "special file") {
		t.Errorf("extract of a FIFO: err = %v, want a special file error", err)
	}
	if _, err := client.run(fmt.Sprintf("disk extract %v:2 files /hello.txt:%v/p2", image, out)); err == nil || !strings.Contains(err.Error(), "partition 2 not found") {
		t.Errorf("extract from partition 2: err = %v", err)
	}
	if after := sha(t, image); after != before {
		t.Errorf("extract changed the image")
	}

	// inject, then extract what was injected
	writeFile(t, filepath.Join(work, "inject.txt"), "injected\n")
	mustRun(t, fmt.Sprintf("disk inject %v files %v/inject.txt:/injected.txt", image, work))
	mustRun(t, fmt.Sprintf("disk extract %v files /injected.txt:%v/injected.txt", image, out))
	if got := readFile(t, filepath.Join(out, "injected.txt")); got != "injected\n" {
		t.Errorf("extracted injected.txt = %q", got)
	}

	// every command released its nbd device and mount
	if mounts, _ := inContainer(t, "grep", "-c", "nbd", "/proc/mounts"); strings.TrimSpace(mounts) != "0" {
		t.Errorf("nbd mounts left in the container: %s", mounts)
	}
	if pids, _ := inContainer(t, "sh", "-c", "cat /sys/block/nbd*/pid 2>/dev/null | wc -l"); strings.TrimSpace(pids) != "0" {
		t.Errorf("connected nbd devices left: %s", pids)
	}
}

func runHost(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%v %v: %v: %s", name, args, err, out)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%v", err)
	}
	return string(b)
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func sha(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
