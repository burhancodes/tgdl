package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allowedReadFileFiles = map[string]bool{
	"keystore.go":      true, // keystore config
	"keys.go":          true, // upload keys
	"deviceprofile.go": true, // device profile
	"xenforo.go":       true, // cookie read
	"trackers.go":      true, // trackers cache
	"gdrive.go":        true, // creds / tokens
	"aria2.go":         true, // torrent-file read
	"patcher.go":       true, // tool bootstrap check
	"cmd_misc.go":      true, // gallery-dl cookie/config file (size-bounded)
	"cmd_download.go":  true, // gallery-dl cookie/config file (size-bounded)
	"main.go":          true, // cgroup memory limit read
}

func TestMemGuard(t *testing.T) {
	// Find root directory containing go.mod
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
			t.Fatal("could not find repository root containing go.mod")
		}
		root = parent
	}

	fset := token.NewFileSet()
	var violations []string

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "vendor" || name == "scraper" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Errorf("failed to parse %s: %v", path, err)
			return nil
		}

		base := filepath.Base(path)

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Check os.ReadFile and ioutil.ReadFile
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if (id.Name == "os" || id.Name == "ioutil") && sel.Sel.Name == "ReadFile" {
						if !allowedReadFileFiles[base] {
							pos := fset.Position(call.Pos())
							violations = append(violations, pos.String()+": forbidden "+id.Name+".ReadFile outside allowlist")
						}
					}

					// Check io.ReadAll
					if id.Name == "io" && sel.Sel.Name == "ReadAll" {
						if !allowedReadFileFiles[base] {
							isLimitReader := false
							if len(call.Args) > 0 {
								if innerCall, ok := call.Args[0].(*ast.CallExpr); ok {
									if innerSel, ok := innerCall.Fun.(*ast.SelectorExpr); ok {
										if innerID, ok := innerSel.X.(*ast.Ident); ok {
											if innerID.Name == "io" && innerSel.Sel.Name == "LimitReader" {
												isLimitReader = true
											}
										}
									}
								}
							}
							if !isLimitReader {
								pos := fset.Position(call.Pos())
								violations = append(violations, pos.String()+": io.ReadAll without io.LimitReader argument")
							}
						}
					}

					// Check http.NewRequest / http.NewRequestWithContext
					if id.Name == "http" && (sel.Sel.Name == "NewRequest" || sel.Sel.Name == "NewRequestWithContext") {
						bodyArg := call.Args[len(call.Args)-1]
						if isBytesBuffer(bodyArg) {
							pos := fset.Position(call.Pos())
							violations = append(violations, pos.String()+": bytes.Buffer used as http request body")
						}
					}
				}
			}

			return true
		})

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(violations) > 0 {
		t.Errorf("MemGuard found %d violations (streaming rule violated):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func isBytesBuffer(expr ast.Expr) bool {
	// Check &buf or buf where buf is bytes.Buffer
	// or bytes.NewBuffer(...)
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		return isBytesBuffer(e.X)
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if id.Name == "bytes" && (sel.Sel.Name == "NewBuffer" || sel.Sel.Name == "NewBufferString") {
					return true
				}
			}
		}
	}
	return false
}
