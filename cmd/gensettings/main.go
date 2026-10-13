package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/burhanverse/tgdl/internal/settings"
)

func main() {
	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error finding repo root: %v\n", err)
		os.Exit(1)
	}

	reg := settings.NewRegistry()
	docContent := settings.GenerateDocs(reg)

	docPath := filepath.Join(root, "docs", "settings.md")
	if err := os.WriteFile(docPath, []byte(docContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", docPath, err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s successfully.\n", docPath)

	envPath := filepath.Join(root, ".env.example")
	envFile, err := os.Open(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", envPath, err)
		os.Exit(1)
	}
	defer envFile.Close()

	envKeys := make(map[string]bool)
	scanner := bufio.NewScanner(envFile)
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		}
		if eq := strings.Index(trimmed, "="); eq > 0 {
			k := strings.TrimSpace(trimmed[:eq])
			if !strings.Contains(k, " ") {
				envKeys[k] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error scanning %s: %v\n", envPath, err)
		os.Exit(1)
	}

	var missing []string
	for _, spec := range reg.All() {
		if !envKeys[spec.Key] {
			missing = append(missing, spec.Key)
		}
	}

	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "warning: the following keys are missing in %s: %v\n", envPath, missing)
		os.Exit(1)
	}
	fmt.Printf("Verified %s: all %d registry keys present.\n", envPath, len(reg.All()))
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	curr := wd
	for {
		if _, err := os.Stat(filepath.Join(curr, "go.mod")); err == nil {
			return curr, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return "", fmt.Errorf("go.mod not found")
		}
		curr = parent
	}
}
