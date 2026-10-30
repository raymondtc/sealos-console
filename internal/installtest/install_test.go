package installtest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const version = "v0.5.0"

// The test executable doubles as a release fixture, without a compiler on the
// installer path. This branch is reached only by the fixture's --version call.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	os.Exit(m.Run())
}
func TestInstallerVerificationAndReplacement(t *testing.T) {
	repo, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	fixtures := filepath.Join(dir, "fixtures")
	target := filepath.Join(dir, "bin with spaces")
	os.Mkdir(fixtures, 0755)
	os.Mkdir(target, 0755)
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	binary, e := os.ReadFile(self)
	if e != nil {
		t.Fatal(e)
	}
	name := "sealos-console"
	ext := ".tar.gz"
	script := "install.sh"
	if runtime.GOOS == "windows" {
		name += ".exe"
		ext = ".zip"
		script = "install.ps1"
	}
	archive := fmt.Sprintf("sealos-console_%s_%s_%s%s", version, runtime.GOOS, runtime.GOARCH, ext)
	var packed bytes.Buffer
	if runtime.GOOS == "windows" {
		z := zip.NewWriter(&packed)
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		w.Write(binary)
		z.Close()
	} else {
		gz := gzip.NewWriter(&packed)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(binary))})
		tw.Write(binary)
		tw.Close()
		gz.Close()
	}
	if e = os.WriteFile(filepath.Join(fixtures, archive), packed.Bytes(), 0644); e != nil {
		t.Fatal(e)
	}
	checksums := filepath.Join(fixtures, "checksums.txt")
	correct := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(packed.Bytes()), archive))
	os.WriteFile(checksums, correct, 0644)
	config := filepath.Join(dir, "login-config")
	os.Mkdir(config, 0700)
	sentinel := filepath.Join(config, "default.json")
	os.WriteFile(sentinel, []byte("preserve-login"), 0600)
	t.Setenv("SEALOS_INSTALL_FIXTURES", fixtures)
	t.Setenv("SEALOS_INSTALL_SCRIPT", filepath.Join(repo, script))
	t.Setenv("SEALOS_INSTALL_TARGET", target)
	t.Setenv("SEALOS_CONSOLE_CONFIG_DIR", config)
	var command string
	var args []string
	if runtime.GOOS == "windows" {
		command = "powershell.exe"
		wrapper := filepath.Join(dir, "run.ps1")
		code := `$ErrorActionPreference = 'Stop'
function Invoke-WebRequest {
 param($Uri,$OutFile,$TimeoutSec,[switch]$UseBasicParsing)
 [System.IO.File]::Copy((Join-Path $env:SEALOS_INSTALL_FIXTURES ([System.IO.Path]::GetFileName($Uri))),$OutFile)
}
& $env:SEALOS_INSTALL_SCRIPT -Version 'v0.5.0' -InstallDir $env:SEALOS_INSTALL_TARGET
`
		os.WriteFile(wrapper, []byte(code), 0644)
		args = []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", wrapper}
	} else {
		command = "sh"
		shim := filepath.Join(dir, "shims")
		os.Mkdir(shim, 0755)
		code := `#!/bin/sh
set -eu
url=''
out=''
while [ "$#" -gt 0 ]; do
 case "$1" in https://*) url=$1; shift ;; -o) out=$2; shift 2 ;; *) shift ;; esac
done
cp "$SEALOS_INSTALL_FIXTURES/${url##*/}" "$out"
`
		os.WriteFile(filepath.Join(shim, "curl"), []byte(code), 0755)
		t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		args = []string{filepath.Join(repo, script), "--version", version, "--install-dir", target}
	}
	run := func(wantSuccess bool) {
		t.Helper()
		out, e := exec.Command(command, args...).CombinedOutput()
		if (e == nil) != wantSuccess {
			t.Fatalf("installer success=%v want=%v: %v\n%s", e == nil, wantSuccess, e, out)
		}
	}
	// A bad checksum cannot replace an existing installation.
	dest := filepath.Join(target, name)
	os.WriteFile(dest, []byte("old binary"), 0755)
	os.WriteFile(checksums, []byte(fmt.Sprintf("%064d  %s\n", 0, archive)), 0644)
	run(false)
	if b, _ := os.ReadFile(dest); string(b) != "old binary" {
		t.Fatal("failed verification changed installation")
	}
	os.WriteFile(checksums, correct, 0644)
	run(true)
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, binary) {
		t.Fatal("wrong executable installed")
	}
	run(true) // Reinstallation replaces the existing binary.
	if b, _ := os.ReadFile(sentinel); string(b) != "preserve-login" {
		t.Fatal("login profile changed")
	}
	if runtime.GOOS != "windows" {
		// An existing developer symlink is replaced; its source is not overwritten.
		original := filepath.Join(dir, "source-binary")
		os.WriteFile(original, []byte("source unchanged"), 0755)
		os.Remove(dest)
		if e = os.Symlink(original, dest); e != nil {
			t.Fatal(e)
		}
		run(true)
		if b, _ := os.ReadFile(original); string(b) != "source unchanged" {
			t.Fatal("symlink source overwritten")
		}
	}
}
