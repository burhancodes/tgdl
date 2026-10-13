package settings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAllConfigEnvVarsRegistered(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find repository root")
		}
		root = parent
	}

	configPath := filepath.Join(root, "internal", "config", "config.go")
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, configPath, nil, 0)
	if err != nil {
		t.Fatalf("parse config.go: %v", err)
	}

	extractedEnvVars := make(map[string]bool)
	helperNames := map[string]bool{
		"envStr":      true,
		"envInt":      true,
		"envInt64":    true,
		"envFloat":    true,
		"envSeconds":  true,
		"envDuration": true,
		"envBool":     true,
	}

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		if id, ok := call.Fun.(*ast.Ident); ok && helperNames[id.Name] {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				val, _ := strconv.Unquote(lit.Value)
				extractedEnvVars[val] = true
			}
		}

		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" {
				if sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv" {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						val, _ := strconv.Unquote(lit.Value)
						extractedEnvVars[val] = true
					}
				}
			}
		}

		return true
	})

	if len(extractedEnvVars) == 0 {
		t.Fatal("no env vars extracted from config.go")
	}

	reg := NewRegistry()
	var missing []string
	for k := range extractedEnvVars {
		if _, ok := reg.Get(k); !ok {
			missing = append(missing, k)
		}
	}

	if len(missing) > 0 {
		t.Errorf("the following env vars from config.go are missing in settings registry: %s", strings.Join(missing, ", "))
	}
}

func TestMaskValue(t *testing.T) {
	if got := MaskValue("notsecret", false); got != "notsecret" {
		t.Errorf("got %q, want %q", got, "notsecret")
	}
	if got := MaskValue("short", true); got != "••••••••" {
		t.Errorf("got %q, want %q", got, "••••••••")
	}
	if got := MaskValue("1234567890ab", true); got != "••••90ab" {
		t.Errorf("got %q, want %q", got, "••••90ab")
	}
	if got := MaskValue("123456:ABC-token-sample-1234", true); got != "••••1234" {
		t.Errorf("got %q, want %q", got, "••••1234")
	}
	if got := MaskValue("", true); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"512M", 512 * 1024 * 1024},
		{"2G", 2 * 1024 * 1024 * 1024},
		{"1024K", 1024 * 1024},
		{"100B", 100},
		{"2147483648", 2147483648},
		{"0", 0},
		{"", 0},
	}
	for _, tc := range cases {
		got, err := ParseBytes(tc.in)
		if err != nil {
			t.Errorf("ParseBytes(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseBytes(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}

	if _, err := ParseBytes("-5M"); err == nil {
		t.Errorf("expected error for negative bytes")
	}
}

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Validate("TG_MAX_CONCURRENT_DOWNLOADS", "0"); err == nil {
		t.Errorf("expected error for concurrency 0")
	}
	if err := reg.Validate("TG_MAX_CONCURRENT_DOWNLOADS", "5"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := reg.Validate("LOG_LEVEL", "INVALID"); err == nil {
		t.Errorf("expected error for invalid log level")
	}
	if err := reg.Validate("LOG_LEVEL", "DEBUG"); err != nil {
		t.Errorf("unexpected error for DEBUG log level: %v", err)
	}
	if err := reg.Validate("MIN_FREE_DISK_BYTES", "2G"); err != nil {
		t.Errorf("unexpected error for 2G: %v", err)
	}
	if err := reg.Validate("TG_ALBUM_MAX_BYTES", "3G"); err == nil {
		t.Errorf("expected error for album size exceeding 2G limit")
	}
}

func TestEnvExampleKeys(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find repository root")
		}
		root = parent
	}

	data, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(data), "\n")
	envKeys := make(map[string]bool)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
		if eq := strings.Index(line, "="); eq > 0 {
			k := strings.TrimSpace(line[:eq])
			if !strings.Contains(k, " ") {
				envKeys[k] = true
			}
		}
	}

	reg := NewRegistry()
	var missing []string
	for _, spec := range reg.All() {
		if !envKeys[spec.Key] {
			missing = append(missing, spec.Key)
		}
	}

	if len(missing) > 0 {
		t.Errorf("the following registry keys are missing in .env.example: %v", missing)
	}
}

func TestSettingsDocsMatchesGenerator(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find repository root")
		}
		root = parent
	}

	docPath := filepath.Join(root, "docs", "settings.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("could not read %s: %v", docPath, err)
	}

	reg := NewRegistry()
	expected := GenerateDocs(reg)
	if string(data) != expected {
		t.Errorf("docs/settings.md is out of date; please run 'go run ./cmd/gensettings'")
	}
}
