package zone

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultIdentityPath is where the machine key lives when --identity is not
// given.
func defaultIdentityPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "zone.key"
	}
	return filepath.Join(dir, "linksocks", "zone.key")
}

func loadIdentity(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		path = defaultIdentityPath()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			return loadIdentity(path)
		}
		if err != nil {
			return nil, err
		}
		_, writeErr := file.WriteString(hex.EncodeToString(key.Seed()) + "\n")
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("identity file %s must have owner-only permissions (chmod 600)", path)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid zone identity file: %s", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
