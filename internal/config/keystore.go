package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

type keystoreFile struct {
	StorePass string `json:"store_pass"`
	KeyAlias  string `json:"key_alias"`
	KeyPass   string `json:"key_pass"`
}

func readUserKeystore(dir string) *KeystoreInfo {
	raw, err := os.ReadFile(filepath.Join(dir, "keystore_config.json"))
	if err != nil {
		return nil
	}
	var kf keystoreFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		slog.Warn("invalid user keystore config", "dir", dir, "err", err)
		return nil
	}
	if kf.StorePass == "" || kf.KeyAlias == "" {
		return nil
	}
	var files []string
	for _, pat := range []string{"*.jks", "*.keystore"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		files = append(files, m...)
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	abs, err := filepath.Abs(files[0])
	if err != nil {
		return nil
	}
	kp := kf.KeyPass
	if kp == "" {
		kp = kf.StorePass
	}
	return &KeystoreInfo{Path: abs, StorePass: kf.StorePass, KeyAlias: kf.KeyAlias, KeyPass: kp}
}
