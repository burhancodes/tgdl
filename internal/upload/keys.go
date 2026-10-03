// Package upload implements uploads to third-party file hosts and per-user
// API key storage.
package upload

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/burhanverse/tgdl/internal/config"
)

// Keys stores per-user host API keys at auth/<uid>/keys.json (mode 0600).
type Keys struct {
	cfg *config.Config
	mu  sync.Mutex
}

func NewKeys(cfg *config.Config) *Keys { return &Keys{cfg: cfg} }

func (k *Keys) file(uid int64) string {
	return filepath.Join(k.cfg.AuthDir, strconv.FormatInt(uid, 10), "keys.json")
}

func (k *Keys) read(uid int64) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(k.file(uid))
	if err != nil {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		slog.Warn("invalid user keys file", "user", uid, "err", err)
		return out
	}
	for name, v := range raw {
		if s := fmt.Sprint(v); s != "" {
			out[name] = s
		}
	}
	return out
}

func (k *Keys) write(uid int64, m map[string]string) error {
	path := k.file(uid)
	if len(m) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns the user's stored key for service, or "".
func (k *Keys) Get(uid int64, service string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.read(uid)[strings.ToLower(service)]
}

// All returns every stored key for the user.
func (k *Keys) All(uid int64) map[string]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.read(uid)
}

func (k *Keys) Save(uid int64, service, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m := k.read(uid)
	m[strings.ToLower(service)] = strings.TrimSpace(key)
	return k.write(uid, m)
}

func (k *Keys) Delete(uid int64, service string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m := k.read(uid)
	delete(m, strings.ToLower(service))
	return k.write(uid, m)
}

// Resolve applies: personal key -> owner key (if shared keys allowed) -> "".
func (k *Keys) Resolve(uid int64, service string) string {
	service = strings.ToLower(service)
	if uid > 0 {
		if v := k.Get(uid, service); v != "" {
			return v
		}
	}
	if k.cfg.AllowSharedKeys {
		switch service {
		case "gofile":
			return k.cfg.GofileAPIKey
		case "pixeldrain":
			return k.cfg.PixeldrainAPIKey
		}
	}
	return ""
}
