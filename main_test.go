package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestChooseFoldersWithFlag(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "photos")
	data, state, err := chooseFolders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if data != dir || state != filepath.Join(dir, ".photobridge") {
		t.Fatalf("got %s %s", data, state)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatal("folder not created")
	}
}

func TestUsableBase(t *testing.T) {
	// Folders under the system temp folder are refused (go run, apps opened
	// straight from a zip), so the home fallback is used.
	if usableBase(t.TempDir()) {
		t.Fatal("temp folder accepted")
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		os.Mkdir(ro, 0o555)
		if usableBase(ro) {
			t.Fatal("read-only folder accepted")
		}
	}
}

func TestLoadTokenIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := loadToken(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := loadToken(dir, false)
	c, _ := loadToken(dir, true)
	if a != b || a == c || len(a) < 16 {
		t.Fatalf("tokens %q %q %q", a, b, c)
	}
}
