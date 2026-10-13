package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

type StoreData struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
	History []ChangeRecord    `json:"history"`
}

type Store struct {
	path          string
	bakPath       string
	envPath       string
	writeEnv      bool
	encryptionKey string
	registry      *Registry
}

func NewStore(dataDir, envPath string, writeEnv bool, encryptionKey string, reg *Registry) *Store {
	return &Store{
		path:          filepath.Join(dataDir, "settings.json"),
		bakPath:       filepath.Join(dataDir, "settings.json.bak"),
		envPath:       envPath,
		writeEnv:      writeEnv,
		encryptionKey: encryptionKey,
		registry:      reg,
	}
}

func (s *Store) Load() (*StoreData, error) {
	data := &StoreData{
		Version: 1,
		Values:  make(map[string]string),
		History: make([]ChangeRecord, 0),
	}

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return data, nil
		}
		return nil, fmt.Errorf("open settings file: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read settings file: %w", err)
	}

	if len(raw) == 0 {
		return data, nil
	}

	if err := json.Unmarshal(raw, data); err != nil {
		return nil, fmt.Errorf("unmarshal settings file: %w", err)
	}

	if data.Values == nil {
		data.Values = make(map[string]string)
	}

	for k, v := range data.Values {
		spec, ok := s.registry.Get(k)
		isSecret := ok && spec.Secret
		dec, err := s.decryptValue(v, isSecret)
		if err != nil {
			return nil, fmt.Errorf("decrypt setting %s: %w", k, err)
		}
		data.Values[k] = dec
	}

	return data, nil
}

func (s *Store) Save(data *StoreData) error {
	if data == nil {
		return errors.New("nil store data")
	}

	toSave := StoreData{
		Version: data.Version,
		Values:  make(map[string]string),
		History: data.History,
	}
	if len(toSave.History) > 50 {
		toSave.History = toSave.History[len(toSave.History)-50:]
	}

	for k, v := range data.Values {
		spec, ok := s.registry.Get(k)
		isSecret := ok && spec.Secret
		enc, err := s.encryptValue(v, isSecret)
		if err != nil {
			return fmt.Errorf("encrypt setting %s: %w", k, err)
		}
		toSave.Values[k] = enc
	}

	encoded, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}

	tmpPath := filepath.Join(dir, fmt.Sprintf(".settings.%d.%d.tmp", os.Getpid(), time.Now().UnixNano()))
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create temp settings file: %w", err)
	}

	if _, err := tmpFile.Write(encoded); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp settings: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp settings: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp settings: %w", err)
	}

	if _, err := os.Stat(s.path); err == nil {
		_ = s.backup()
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename settings file: %w", err)
	}

	return nil
}

func (s *Store) backup() error {
	in, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer in.Close()

	tmpBak := s.bakPath + fmt.Sprintf(".%d.tmp", os.Getpid())
	out, err := os.OpenFile(tmpBak, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	_ = out.Sync()
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpBak)
		return err
	}

	return os.Rename(tmpBak, s.bakPath)
}

func (s *Store) SyncEnv(key, val string, unset bool) {
	if !s.writeEnv || s.envPath == "" {
		return
	}

	f, err := os.OpenFile(s.envPath, os.O_RDWR, 0o600)
	if err != nil {
		slog.Warn("cannot write .env file: file unavailable or read-only", "path", s.envPath, "err", err)
		return
	}
	defer f.Close()

	content, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		slog.Warn("failed to read .env file for update", "err", err)
		return
	}

	lines := strings.Split(string(content), "\n")
	found := false
	targetPrefix := key + "="
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, targetPrefix) || strings.HasPrefix(trimmed, "# "+targetPrefix) {
			if unset {
				lines[i] = "# " + targetPrefix
			} else {
				lines[i] = targetPrefix + val
			}
			found = true
			break
		}
	}

	if !found && !unset {
		lines = append(lines, targetPrefix+val)
	}

	newContent := strings.Join(lines, "\n")
	dir := filepath.Dir(s.envPath)
	tmpPath := filepath.Join(dir, fmt.Sprintf(".env.%d.%d.tmp", os.Getpid(), time.Now().UnixNano()))
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Warn("cannot create temp .env file", "err", err)
		return
	}

	if _, err := tmpFile.WriteString(newContent); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		slog.Warn("failed to write temp .env", "err", err)
		return
	}

	_ = tmpFile.Sync()
	_ = tmpFile.Close()

	if err := os.Rename(tmpPath, s.envPath); err != nil {
		_ = os.Remove(tmpPath)
		slog.Warn("failed to rename updated .env", "err", err)
	}
}

func (s *Store) encryptValue(val string, isSecret bool) (string, error) {
	if !isSecret || val == "" || s.encryptionKey == "" {
		return val, nil
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key, err := scrypt.Key([]byte(s.encryptionKey), salt, 32768, 8, 1, 32)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nil, nonce, []byte(val), nil)
	return fmt.Sprintf("enc:v1:%s:%s:%s", hex.EncodeToString(salt), hex.EncodeToString(nonce), hex.EncodeToString(sealed)), nil
}

func (s *Store) decryptValue(val string, isSecret bool) (string, error) {
	if !strings.HasPrefix(val, "enc:v1:") {
		return val, nil
	}

	if s.encryptionKey == "" {
		return "", errors.New("encrypted value present but SETTINGS_ENCRYPTION_KEY is unset")
	}

	parts := strings.Split(val, ":")
	if len(parts) != 5 {
		return "", errors.New("malformed encrypted setting format")
	}

	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid salt hex: %w", err)
	}

	nonce, err := hex.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("invalid nonce hex: %w", err)
	}

	sealed, err := hex.DecodeString(parts[4])
	if err != nil {
		return "", fmt.Errorf("invalid ciphertext hex: %w", err)
	}

	key, err := scrypt.Key([]byte(s.encryptionKey), salt, 32768, 8, 1, 32)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	decrypted, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", errors.New("invalid encryption key or corrupted ciphertext")
	}

	return string(decrypted), nil
}
