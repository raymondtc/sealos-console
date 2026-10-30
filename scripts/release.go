//go:build ignore

// Build release archives with Go's standard library. Run from the repository root.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func pack(path string, files map[string]string, zipFormat bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	names := []string{}
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if zipFormat {
		w := zip.NewWriter(f)
		for _, n := range names {
			data, e := os.ReadFile(files[n])
			if e != nil {
				return e
			}
			h := &zip.FileHeader{Name: n, Method: zip.Deflate}
			h.SetModTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			h.SetMode(0644)
			if strings.HasSuffix(n, ".exe") {
				h.SetMode(0755)
			}
			entry, e := w.CreateHeader(h)
			if e != nil {
				return e
			}
			if _, e = entry.Write(data); e != nil {
				return e
			}
		}
		return w.Close()
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		data, e := os.ReadFile(files[n])
		if e != nil {
			return e
		}
		mode := int64(0644)
		if n == "sealos-console" {
			mode = 0755
		}
		if e = tw.WriteHeader(&tar.Header{Name: n, Size: int64(len(data)), Mode: mode, ModTime: time.Unix(0, 0)}); e != nil {
			return e
		}
		if _, e = tw.Write(data); e != nil {
			return e
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
func main() {
	version := flag.String("version", "", "Release version, for example v0.3.0")
	out := flag.String("out", "dist", "Output directory")
	flag.Parse()
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([-.][A-Za-z0-9.]+)?$`).MatchString(*version) {
		must(fmt.Errorf("provide --version vMAJOR.MINOR.PATCH"))
	}
	must(os.MkdirAll(*out, 0755))
	tmp, err := os.MkdirTemp("", "sealos-build-")
	must(err)
	defer os.RemoveAll(tmp)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			binary := "sealos-console"
			ext := ".tar.gz"
			if goos == "windows" {
				binary += ".exe"
				ext = ".zip"
			}
			target := filepath.Join(tmp, binary)
			cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X main.version="+*version, "-o", target, "./cmd/sealos-console")
			env := []string{}
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "GOOS=") && !strings.HasPrefix(v, "GOARCH=") && !strings.HasPrefix(v, "CGO_ENABLED=") {
					env = append(env, v)
				}
			}
			cmd.Env = append(env, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			must(cmd.Run())
			archive := fmt.Sprintf("sealos-console_%s_%s_%s%s", *version, goos, arch, ext)
			must(pack(filepath.Join(*out, archive), map[string]string{binary: target, "LICENSE": "LICENSE", "README.md": "README.md", "examples/app.json": "examples/app.json", "examples/database.json": "examples/database.json", "examples/app-public.json": "examples/app-public.json"}, goos == "windows"))
			fmt.Println(archive)
		}
	}
	skill := map[string]string{}
	must(filepath.WalkDir("skills/sealos-console", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, e := filepath.Rel("skills", path)
			if e != nil {
				return e
			}
			skill[filepath.ToSlash(rel)] = path
		}
		return nil
	}))
	must(pack(filepath.Join(*out, "sealos-console-skill.zip"), skill, true))
	entries, err := os.ReadDir(*out)
	must(err)
	checks := []string{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "checksums.txt" {
			continue
		}
		f, e := os.Open(filepath.Join(*out, entry.Name()))
		must(e)
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		must(e)
		checks = append(checks, fmt.Sprintf("%x  %s", h.Sum(nil), entry.Name()))
	}
	sort.Strings(checks)
	must(os.WriteFile(filepath.Join(*out, "checksums.txt"), []byte(strings.Join(checks, "\n")+"\n"), 0644))
}
