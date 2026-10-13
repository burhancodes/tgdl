package settings_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTouchedFilesHaveNoComments(t *testing.T) {
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
			t.Fatal("could not find repo root")
		}
		root = parent
	}

	touchedFiles := []string{
		"cmd/gensettings/main.go",
		"cmd/tgdl/main.go",
		"internal/auth/auth.go",
		"internal/auth/auth_test.go",
		"internal/bot/app.go",
		"internal/bot/bot_test.go",
		"internal/bot/help.go",
		"internal/bot/settings.go",
		"internal/bot/settings_test.go",
		"internal/config/config.go",
		"internal/config/config_test.go",
		"internal/jobs/manager.go",
		"internal/jobs/semaphore.go",
		"internal/jobs/semaphore_test.go",
		"internal/jobs/sources.go",
		"internal/jobs/tgupload.go",
		"internal/logging/logging.go",
		"internal/media/media.go",
		"internal/tg/botapi.go",
		"internal/tg/tg.go",
		"internal/torrent/aria2.go",
	}

	settingsDir := filepath.Join(root, "internal", "settings")
	entries, err := os.ReadDir(settingsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			touchedFiles = append(touchedFiles, filepath.Join("internal", "settings", e.Name()))
		}
	}

	allowedPrefixes := []string{
		"//go:build",
		"//go:embed",
		"//go:generate",
		"//go:linkname",
		"//nolint",
		"// Code generated",
	}

	fset := token.NewFileSet()
	var violations []string

	for _, rel := range touchedFiles {
		fullPath := filepath.Join(root, rel)
		node, err := parser.ParseFile(fset, fullPath, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}

		for _, cg := range node.Comments {
			for _, c := range cg.List {
				text := strings.TrimSpace(c.Text)
				isAllowed := false
				for _, prefix := range allowedPrefixes {
					if strings.HasPrefix(text, prefix) {
						isAllowed = true
						break
					}
				}
				if !isAllowed {
					pos := fset.Position(c.Pos())
					violations = append(violations, rel+":"+pos.String()+": "+text)
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("found %d comments in touched files violating zero-comment policy:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
