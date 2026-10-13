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

var allowedReadFileFiles = map[string]string{
	"cmd/tgdl/main.go":             "cgroup memory limit read",
	"internal/bot/cmd_download.go": "credentials file (size-bounded)",
	"internal/bot/cmd_misc.go":     "cookies file (size-bounded)",
	"internal/dl/deviceprofile.go": "device profile config read",
	"internal/torrent/aria2.go":    "torrent-file read",
	"internal/torrent/trackers.go": "trackers cache read",
	"internal/gdrive/gdrive.go":    "creds / tokens file read",
	"internal/upload/keys.go":      "upload keys config read",
}

func TestMemGuard(t *testing.T) {
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

		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if (id.Name == "os" || id.Name == "ioutil") && sel.Sel.Name == "ReadFile" {
						if _, allowed := allowedReadFileFiles[rel]; !allowed {
							pos := fset.Position(call.Pos())
							violations = append(violations, pos.String()+": forbidden "+id.Name+".ReadFile outside allowlist ("+rel+")")
						}
					}

					if id.Name == "io" && sel.Sel.Name == "ReadAll" {
						if _, allowed := allowedReadFileFiles[rel]; !allowed {
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
