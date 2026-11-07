package zone

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadIdentityCreatesAndReloadsOwnerOnlyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "linksocks", "zone.key")
	first, err := loadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != ed25519.PrivateKeySize {
		t.Fatalf("private key length = %d, want %d", len(first), ed25519.PrivateKeySize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != hex.EncodeToString(first.Seed())+"\n" {
		t.Fatal("identity file does not contain the generated seed")
	}
	if runtime.GOOS != "windows" {
		for _, tc := range []struct {
			path string
			mode os.FileMode
		}{{path, 0600}, {filepath.Dir(path), 0700}} {
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.mode {
				t.Errorf("%s permissions = %o, want %o", tc.path, info.Mode().Perm(), tc.mode)
			}
		}
	}
	second, err := loadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || !first.Public().(ed25519.PublicKey).Equal(second.Public()) {
		t.Fatal("reloading changed the persistent identity")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("loading an existing identity modified the file: %v", err)
	}
}

func TestLoadIdentityUsesUserConfigDirectory(t *testing.T) {
	config := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", config)
	case "darwin", "ios":
		t.Setenv("HOME", config)
	default:
		t.Setenv("XDG_CONFIG_HOME", config)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadIdentity(filepath.Join(dir, "linksocks", "zone.key"))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("default identity path does not use the user config directory: %v", err)
	}
}

func TestLoadIdentityReadsExistingSeed(t *testing.T) {
	seed := bytes.Repeat([]byte{0xa5}, ed25519.SeedSize)
	path := filepath.Join(t.TempDir(), "zone.key")
	data := []byte(" \t" + hex.EncodeToString(seed) + "\r\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := loadIdentity(path)
	if err != nil || !bytes.Equal(key, ed25519.NewKeyFromSeed(seed)) {
		t.Fatalf("did not reconstruct the expected Ed25519 identity: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("existing identity was rewritten: %v", err)
	}
}

func TestLoadIdentityRejectsBadFilesWithoutOverwriting(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"empty", ""},
		{"whitespace", " \n\t"},
		{"short_seed", strings.Repeat("ab", 31) + "\n"},
		{"long_seed", strings.Repeat("ab", 33) + "\n"},
		{"odd_hex", strings.Repeat("a", 63)},
		{"invalid_hex", strings.Repeat("zz", 32)},
		{"private_key_instead_of_seed", strings.Repeat("ab", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zone.key")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadIdentity(path); err == nil {
				t.Fatal("accepted a malformed identity file")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.data {
				t.Fatalf("malformed identity file was overwritten: %v", err)
			}
		})
	}
}

func TestLoadIdentityRejectsSharedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	data := []byte(strings.Repeat("ab", ed25519.SeedSize) + "\n")
	for _, mode := range []os.FileMode{0644, 0640, 0604, 0660} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zone.key")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := loadIdentity(path); err == nil {
				t.Fatal("accepted a group- or world-accessible identity file")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatalf("permission rejection rewrote the identity: %v", err)
			}
		})
	}
}

func TestLoadIdentityReportsFilesystemErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadIdentity(dir); err == nil {
		t.Fatal("accepted a directory as an identity file")
	}
	path := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(path, []byte("existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIdentity(filepath.Join(path, "zone.key")); err == nil {
		t.Fatal("accepted an identity path under a regular file")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "existing file" {
		t.Fatalf("filesystem error altered an existing file: %v", err)
	}
}
