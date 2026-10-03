// Package apk decompiles, patches, re-signs and aligns Android APKs by
// orchestrating APKEditor, tgpatcher and uber-apk-signer.
package apk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/config"
)

const (
	apkEditorRelease = "https://api.github.com/repos/REAndroid/APKEditor/releases/latest"
	uberSignerRelease = "https://api.github.com/repos/patrickfav/uber-apk-signer/releases/latest"
	tgPatcherURL     = "https://raw.githubusercontent.com/AbhiTheModder/termux-scripts/refs/heads/main/tgpatcher.py"
)

// Patcher implements jobs.Patcher.
type Patcher struct {
	toolsDir string
	http     *http.Client
	mu       sync.Mutex
}

// New returns a patcher that caches its tools under dataDir/tools.
func New(cfg *config.Config) *Patcher {
	return &Patcher{toolsDir: filepath.Join(cfg.DataDir, "tools"), http: &http.Client{Timeout: 2 * time.Minute}}
}

func (p *Patcher) apkEditorJar() string { return filepath.Join(p.toolsDir, "APKEditor.jar") }
func (p *Patcher) signerJar() string    { return filepath.Join(p.toolsDir, "uber-apk-signer.jar") }
func (p *Patcher) tgPatcher() string    { return filepath.Join(p.toolsDir, "tgpatcher.py") }

func nonEmpty(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

func (p *Patcher) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func (p *Patcher) fetchFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func (p *Patcher) fetchJarAsset(ctx context.Context, api, dest string) error {
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := p.getJSON(ctx, api, &rel); err != nil {
		return err
	}
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, ".jar") && a.URL != "" {
			return p.fetchFile(ctx, a.URL, dest)
		}
	}
	return errors.New("no .jar asset in release")
}

func (p *Patcher) ensureTools(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(p.toolsDir, 0o755); err != nil {
		return err
	}
	if !nonEmpty(p.apkEditorJar()) {
		slog.Info("downloading APKEditor.jar")
		if err := p.fetchJarAsset(ctx, apkEditorRelease, p.apkEditorJar()); err != nil {
			return fmt.Errorf("download APKEditor: %w", err)
		}
	}
	if !nonEmpty(p.signerJar()) {
		if err := p.fetchJarAsset(ctx, uberSignerRelease, p.signerJar()); err != nil {
			slog.Warn("uber-apk-signer unavailable; will fall back to zipalign/apksigner", "err", err)
		}
	}
	if !nonEmpty(p.tgPatcher()) {
		if err := p.fetchFile(ctx, tgPatcherURL, p.tgPatcher()); err != nil {
			return fmt.Errorf("download tgpatcher: %w", err)
		}
	}
	if b, err := os.ReadFile(p.tgPatcher()); err == nil && bytes.Contains(b, []byte("Mod by Abhi")) {
		_ = os.WriteFile(p.tgPatcher(), bytes.ReplaceAll(b, []byte("Mod by Abhi"), []byte("by AquaLabs")), 0o644)
	}
	return nil
}

func run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return buf.String(), ctx.Err()
	}
	return buf.String(), err
}

func findExec(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func sdkTool(name string) string {
	if p := findExec(name); p != "" {
		return p
	}
	var bases []string
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT", "ANDROID_SDK"} {
		if v := os.Getenv(env); v != "" {
			bases = append(bases, filepath.Join(v, "build-tools"))
		}
	}
	home, _ := os.UserHomeDir()
	bases = append(bases, filepath.Join(home, "Android/Sdk/build-tools"), "/usr/lib/android-sdk/build-tools", "/opt/android-sdk/build-tools")
	for _, b := range bases {
		entries, err := os.ReadDir(b)
		if err != nil {
			continue
		}
		for _, e := range entries {
			c := filepath.Join(b, e.Name(), name)
			if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return c
			}
		}
	}
	return ""
}

// Patch runs the full pipeline and returns the path of <orig>_patched.apk in outDir.
func (p *Patcher) Patch(ctx context.Context, input, outDir, originalName string, ks *config.KeystoreInfo, stage func(string)) (string, error) {
	say := func(s string) {
		if stage != nil {
			stage(s)
		}
	}
	say("Ensuring APKEditor, tgpatcher & uber-apk-signer tools...")
	if err := p.ensureTools(ctx); err != nil {
		return "", err
	}
	java := findExec("java")
	if java == "" {
		return "", errors.New("java is not installed (required for APKEditor)")
	}
	base := strings.TrimSpace(originalName)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		base = "app"
	}
	work := filepath.Join(outDir, "patcher_tmp")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	decompiled := filepath.Join(work, "plus")
	unaligned := filepath.Join(work, "unaligned_patched.apk")

	say("Decompiling APK with APKEditor...")
	if out, err := run(ctx, work, java, "-jar", p.apkEditorJar(), "d", "-i", input, "-o", decompiled, "-dex-lib", "jf"); err != nil {
		return "", fmt.Errorf("decompilation failed: %w: %s", err, tail(out))
	}
	say("Applying tgpatcher --anti patch...")
	if out, err := run(ctx, work, "python3", p.tgPatcher(), "--anti", "--dir", "plus"); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		slog.Warn("tgpatcher returned an error", "err", err, "output", tail(out))
	}
	say("Recompiling APK with APKEditor...")
	if out, err := run(ctx, work, java, "-jar", p.apkEditorJar(), "b", "-i", decompiled, "-o", unaligned, "-dex-lib", "jf"); err != nil {
		return "", fmt.Errorf("recompilation failed: %w: %s", err, tail(out))
	}
	say("Zip-aligning & signing APK (v1+v2+v3+v4)...")
	final := filepath.Join(outDir, base+"_patched.apk")
	if err := p.alignAndSign(ctx, java, unaligned, final, ks); err != nil {
		return "", err
	}
	return final, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 800 {
		return s[len(s)-800:]
	}
	return s
}

func (p *Patcher) alignAndSign(ctx context.Context, java, unaligned, final string, ks *config.KeystoreInfo) error {
	if nonEmpty(p.signerJar()) {
		outDir := filepath.Dir(final)
		args := []string{"-jar", p.signerJar(), "--apks", unaligned, "--out", outDir}
		if ks != nil && ks.Path != "" {
			args = append(args, "--ks", ks.Path, "--ksAlias", ks.KeyAlias, "--ksPass", ks.StorePass, "--ksKeyPass", ks.KeyPass)
		}
		out, err := run(ctx, outDir, java, args...)
		stem := strings.TrimSuffix(filepath.Base(unaligned), ".apk")
		for _, suffix := range []string{"-aligned-signed.apk", "-aligned-debugSigned.apk", "-aligned-unsigned.apk"} {
			if c := filepath.Join(outDir, stem+suffix); nonEmpty(c) {
				_ = os.Remove(final)
				return os.Rename(c, final)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("uber-apk-signer produced no output; falling back", "err", err, "output", tail(out))
	}
	if za := sdkTool("zipalign"); za != "" {
		if out, err := run(ctx, "", za, "-p", "-f", "4", unaligned, final); err != nil {
			slog.Warn("zipalign failed", "err", err, "output", tail(out))
		}
	}
	if !nonEmpty(final) {
		data, err := os.ReadFile(unaligned)
		if err != nil {
			return err
		}
		if err := os.WriteFile(final, data, 0o644); err != nil {
			return err
		}
	}
	if ks == nil || ks.Path == "" {
		return nil
	}
	if as := sdkTool("apksigner"); as != "" {
		if out, err := run(ctx, "", as, "sign", "--ks", ks.Path, "--ks-pass", "pass:"+ks.StorePass,
			"--ks-key-alias", ks.KeyAlias, "--key-pass", "pass:"+ks.KeyPass, final); err == nil {
			return nil
		} else {
			slog.Warn("apksigner failed", "err", err, "output", tail(out))
		}
	}
	if js := findExec("jarsigner"); js != "" {
		if out, err := run(ctx, "", js, "-keystore", ks.Path, "-storepass", ks.StorePass, "-keypass", ks.KeyPass,
			"-sigalg", "SHA256withRSA", "-digestalg", "SHA-256", final, ks.KeyAlias); err != nil {
			return fmt.Errorf("jarsigner failed: %w: %s", err, tail(out))
		}
		return nil
	}
	return errors.New("no APK signer available (need uber-apk-signer, apksigner or jarsigner)")
}
