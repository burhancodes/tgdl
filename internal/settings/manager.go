package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

type EditRateLimiter struct {
	mu         sync.Mutex
	timestamps []time.Time
	limit      int
	window     time.Duration
}

func NewEditRateLimiter(limit int, window time.Duration) *EditRateLimiter {
	return &EditRateLimiter{
		limit:  limit,
		window: window,
	}
}

func (l *EditRateLimiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)
	filtered := l.timestamps[:0]
	for _, t := range l.timestamps {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	l.timestamps = filtered
	if len(l.timestamps) >= l.limit {
		return false
	}
	l.timestamps = append(l.timestamps, now)
	return true
}

type Manager struct {
	mu       sync.RWMutex
	cfg      *config.Config
	registry *Registry
	store    *Store
	data     *StoreData
	hooks    map[string]Hook
	limiter  *EditRateLimiter
}

func NewManager(cfg *config.Config, reg *Registry, store *Store) (*Manager, error) {
	data, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings store: %w", err)
	}

	m := &Manager{
		cfg:      cfg,
		registry: reg,
		store:    store,
		data:     data,
		hooks:    make(map[string]Hook),
		limiter:  NewEditRateLimiter(30, time.Minute),
	}

	m.applyStartupOverrides()
	return m, nil
}

func (m *Manager) RegisterHook(key string, h Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks[CleanKey(key)] = h
}

func (m *Manager) Registry() *Registry {
	return m.registry
}

func (m *Manager) applyStartupOverrides() {
	if m.data == nil || len(m.data.Values) == 0 {
		return
	}

	validOverrides := make(map[string]string)
	for k, v := range m.data.Values {
		cleanK := CleanKey(k)
		spec, ok := m.registry.Get(cleanK)
		if !ok {
			slog.Warn("ignoring unknown setting override from settings.json", "key", k)
			continue
		}
		if spec.Apply == ApplyReadOnly {
			slog.Warn("ignoring override for read-only setting from settings.json", "key", k)
			continue
		}
		if spec.Validate != nil {
			if err := spec.Validate(v); err != nil {
				slog.Warn("ignoring invalid setting override from settings.json", "key", k, "err", err)
				continue
			}
		}
		validOverrides[cleanK] = v
		if spec.OnChange != nil {
			if err := spec.OnChange(m.cfg, v); err != nil {
				slog.Warn("failed to apply setting override to config", "key", k, "err", err)
			}
		}
	}
	m.data.Values = validOverrides
}

func (m *Manager) Get(key string) (val string, source ValueSource, spec *Spec, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanK := CleanKey(key)
	sp, ok := m.registry.Get(cleanK)
	if !ok {
		return "", "", nil, fmt.Errorf("unknown configuration key %q", key)
	}

	if ov, found := m.data.Values[cleanK]; found {
		return ov, SourceOverride, sp, nil
	}

	if envVal, found := os.LookupEnv(cleanK); found {
		return strings.TrimSpace(envVal), SourceEnv, sp, nil
	}

	return sp.Default, SourceDefault, sp, nil
}

func (m *Manager) Set(ctx context.Context, userID int64, key, val, source string) (*ChangeRecord, error) {
	cleanK := CleanKey(key)
	val = strings.TrimSpace(val)

	if !m.limiter.Allow() {
		return nil, errors.New("rate limit exceeded (max 30 edits per minute)")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	spec, ok := m.registry.Get(cleanK)
	if !ok {
		return nil, fmt.Errorf("unknown configuration key %q", key)
	}

	if spec.Apply == ApplyReadOnly {
		return nil, fmt.Errorf("setting %s is read-only and can only be set via the environment", cleanK)
	}

	if cleanK == "AUTHORIZED_USER_IDS" {
		proposed, err := ParseIDList(val)
		if err != nil {
			return nil, err
		}
		if m.cfg.HasOwner && !m.cfg.HasExplicitOwner && m.cfg.OwnerID != 0 {
			if len(proposed) > 0 && !slices.Contains(proposed, m.cfg.OwnerID) {
				return nil, fmt.Errorf("cannot remove owner (ID %d) from AUTHORIZED_USER_IDS: lock-out prevented", m.cfg.OwnerID)
			}
			if len(proposed) == 0 {
				return nil, errors.New("cannot clear AUTHORIZED_USER_IDS: OWNER_ID is not configured separately")
			}
		}
	}

	if spec.Validate != nil {
		if err := spec.Validate(val); err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", cleanK, err)
		}
	}

	oldVal, _, _, _ := m.getLocked(cleanK)

	if spec.Apply == ApplyLive {
		if hook, ok := m.hooks[cleanK]; ok && hook != nil {
			if err := hook(ctx, m.cfg, cleanK, val); err != nil {
				return nil, fmt.Errorf("apply setting live: %w", err)
			}
		}
		if spec.OnChange != nil {
			if err := spec.OnChange(m.cfg, val); err != nil {
				return nil, fmt.Errorf("apply setting to config: %w", err)
			}
		}
	}

	m.data.Values[cleanK] = val

	rec := ChangeRecord{
		Timestamp: time.Now().UTC(),
		UserID:    userID,
		Key:       cleanK,
		OldValue:  oldVal,
		NewValue:  val,
		Source:    source,
	}
	m.data.History = append(m.data.History, rec)

	if err := m.store.Save(m.data); err != nil {
		return nil, fmt.Errorf("save settings: %w", err)
	}

	m.store.SyncEnv(cleanK, val, false)

	slog.Info("settings changed",
		"user_id", userID,
		"key", cleanK,
		"old", MaskValue(oldVal, spec.Secret),
		"new", MaskValue(val, spec.Secret),
		"source", source,
	)

	return &rec, nil
}

func (m *Manager) Reset(ctx context.Context, userID int64, key, source string) (*ChangeRecord, error) {
	cleanK := CleanKey(key)

	if !m.limiter.Allow() {
		return nil, errors.New("rate limit exceeded (max 30 edits per minute)")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	spec, ok := m.registry.Get(cleanK)
	if !ok {
		return nil, fmt.Errorf("unknown configuration key %q", key)
	}

	if spec.Apply == ApplyReadOnly {
		return nil, fmt.Errorf("setting %s is read-only", cleanK)
	}

	oldVal, hadOverride := m.data.Values[cleanK]
	if !hadOverride {
		return nil, nil
	}

	delete(m.data.Values, cleanK)

	newVal, _, _, _ := m.getLocked(cleanK)

	if cleanK == "AUTHORIZED_USER_IDS" {
		proposed, _ := ParseIDList(newVal)
		if m.cfg.HasOwner && !m.cfg.HasExplicitOwner && m.cfg.OwnerID != 0 && len(proposed) > 0 {
			if !slices.Contains(proposed, m.cfg.OwnerID) {
				m.data.Values[cleanK] = oldVal
				return nil, fmt.Errorf("resetting AUTHORIZED_USER_IDS would lock out owner (ID %d)", m.cfg.OwnerID)
			}
		}
	}

	if spec.Apply == ApplyLive {
		if hook, ok := m.hooks[cleanK]; ok && hook != nil {
			if err := hook(ctx, m.cfg, cleanK, newVal); err != nil {
				m.data.Values[cleanK] = oldVal
				return nil, fmt.Errorf("revert setting live: %w", err)
			}
		}
		if spec.OnChange != nil {
			if err := spec.OnChange(m.cfg, newVal); err != nil {
				m.data.Values[cleanK] = oldVal
				return nil, fmt.Errorf("revert setting on config: %w", err)
			}
		}
	}

	rec := ChangeRecord{
		Timestamp: time.Now().UTC(),
		UserID:    userID,
		Key:       cleanK,
		OldValue:  oldVal,
		NewValue:  newVal,
		Source:    source,
	}
	m.data.History = append(m.data.History, rec)

	if err := m.store.Save(m.data); err != nil {
		return nil, fmt.Errorf("save settings: %w", err)
	}

	m.store.SyncEnv(cleanK, "", true)

	slog.Info("settings reset",
		"user_id", userID,
		"key", cleanK,
		"old", MaskValue(oldVal, spec.Secret),
		"new", MaskValue(newVal, spec.Secret),
		"source", source,
	)

	return &rec, nil
}

func (m *Manager) ResetAll(ctx context.Context, userID int64, source string) error {
	if !m.limiter.Allow() {
		return errors.New("rate limit exceeded (max 30 edits per minute)")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for k := range m.data.Values {
		spec, ok := m.registry.Get(k)
		if !ok || spec.Apply != ApplyLive {
			continue
		}
		fallback := spec.Default
		if envVal, found := os.LookupEnv(k); found {
			fallback = strings.TrimSpace(envVal)
		}
		if hook, ok := m.hooks[k]; ok && hook != nil {
			_ = hook(ctx, m.cfg, k, fallback)
		}
		if spec.OnChange != nil {
			_ = spec.OnChange(m.cfg, fallback)
		}
	}

	m.data.Values = make(map[string]string)
	rec := ChangeRecord{
		Timestamp: time.Now().UTC(),
		UserID:    userID,
		Key:       "ALL",
		OldValue:  "overrides",
		NewValue:  "reset",
		Source:    source,
	}
	m.data.History = append(m.data.History, rec)

	if err := m.store.Save(m.data); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	slog.Info("all settings overrides reset", "user_id", userID, "source", source)
	return nil
}

func (m *Manager) Undo(ctx context.Context, userID int64) (*ChangeRecord, error) {
	if !m.limiter.Allow() {
		return nil, errors.New("rate limit exceeded (max 30 edits per minute)")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.data.History) == 0 {
		return nil, errors.New("no history to undo")
	}

	last := m.data.History[len(m.data.History)-1]
	m.data.History = m.data.History[:len(m.data.History)-1]

	if last.Key == "ALL" {
		return nil, errors.New("cannot undo reset-all operation")
	}

	spec, ok := m.registry.Get(last.Key)
	if !ok {
		return nil, fmt.Errorf("unknown key in history: %s", last.Key)
	}

	if last.OldValue == "" {
		delete(m.data.Values, last.Key)
	} else {
		m.data.Values[last.Key] = last.OldValue
	}

	activeVal, _, _, _ := m.getLocked(last.Key)
	if spec.Apply == ApplyLive {
		if hook, ok := m.hooks[last.Key]; ok && hook != nil {
			_ = hook(ctx, m.cfg, last.Key, activeVal)
		}
		if spec.OnChange != nil {
			_ = spec.OnChange(m.cfg, activeVal)
		}
	}

	if err := m.store.Save(m.data); err != nil {
		return nil, fmt.Errorf("save settings on undo: %w", err)
	}

	slog.Info("setting change undone",
		"user_id", userID,
		"key", last.Key,
		"restored", MaskValue(last.OldValue, spec.Secret),
	)

	return &last, nil
}

func (m *Manager) History() []ChangeRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ChangeRecord, len(m.data.History))
	copy(out, m.data.History)
	return out
}

func (m *Manager) Changed() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.data.Values))
	for k, v := range m.data.Values {
		out[k] = v
	}
	return out
}

func (m *Manager) Export() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	exported := make(map[string]string)
	for k, v := range m.data.Values {
		spec, ok := m.registry.Get(k)
		if ok && spec.Secret {
			exported[k] = "[REDACTED]"
		} else {
			exported[k] = v
		}
	}

	return json.MarshalIndent(exported, "", "  ")
}

func (m *Manager) Import(raw []byte) ([]Diff, func(ctx context.Context, userID int64) error, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var incoming map[string]string
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return nil, nil, fmt.Errorf("invalid json format: %w", err)
	}

	var diffs []Diff
	toApply := make(map[string]string)

	for k, v := range incoming {
		cleanK := CleanKey(k)
		if v == "[REDACTED]" {
			continue
		}
		spec, ok := m.registry.Get(cleanK)
		if !ok {
			return nil, nil, fmt.Errorf("unknown setting %q in import", k)
		}
		if spec.Apply == ApplyReadOnly {
			return nil, nil, fmt.Errorf("setting %s is read-only and cannot be imported", cleanK)
		}
		if cleanK == "AUTHORIZED_USER_IDS" {
			proposed, err := ParseIDList(v)
			if err != nil {
				return nil, nil, err
			}
			if m.cfg.HasOwner && !m.cfg.HasExplicitOwner && m.cfg.OwnerID != 0 && len(proposed) > 0 {
				if !slices.Contains(proposed, m.cfg.OwnerID) {
					return nil, nil, fmt.Errorf("imported AUTHORIZED_USER_IDS locks out owner (%d)", m.cfg.OwnerID)
				}
			}
		}
		if spec.Validate != nil {
			if err := spec.Validate(v); err != nil {
				return nil, nil, fmt.Errorf("invalid value for %s: %w", cleanK, err)
			}
		}

		oldVal, _, _, _ := m.getLocked(cleanK)
		if !ValuesEqual(spec, oldVal, v) {
			diffs = append(diffs, Diff{
				Key:      cleanK,
				OldVal:   oldVal,
				NewVal:   v,
				IsSecret: spec.Secret,
			})
			toApply[cleanK] = v
		}
	}

	applyFn := func(ctx context.Context, userID int64) error {
		for k, v := range toApply {
			if _, err := m.Set(ctx, userID, k, v, "import"); err != nil {
				return err
			}
		}
		return nil
	}

	return diffs, applyFn, nil
}

func (m *Manager) getLocked(key string) (string, ValueSource, *Spec, error) {
	sp, ok := m.registry.Get(key)
	if !ok {
		return "", "", nil, fmt.Errorf("unknown configuration key %q", key)
	}
	if ov, found := m.data.Values[key]; found {
		return ov, SourceOverride, sp, nil
	}
	if envVal, found := os.LookupEnv(key); found {
		return strings.TrimSpace(envVal), SourceEnv, sp, nil
	}
	return sp.Default, SourceDefault, sp, nil
}
