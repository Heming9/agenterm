package i18n

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestResourcesComplete(t *testing.T) {
	placeholders := regexp.MustCompile(`%[+#.0-9\-]*[a-zA-Z%]`)
	for key, entry := range messages {
		if entry["en"] == "" || entry["zh-CN"] == "" {
			t.Errorf("%s: missing translation", key)
		}
		if strings.Join(placeholders.FindAllString(entry["en"], -1), ",") != strings.Join(placeholders.FindAllString(entry["zh-CN"], -1), ",") {
			t.Errorf("%s: format placeholders differ", key)
		}
	}
}

func TestApplicationResourceKeysExist(t *testing.T) {
	// Check references in application and library source without hardcoding a list
	// of keys, catching typos when adding or renaming messages.
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(tree, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				index := -1
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if f.Name == "tr" || f.Name == "serverText" {
						index = 0
					}
				case *ast.SelectorExpr:
					if pkg, ok := f.X.(*ast.Ident); ok && pkg.Name == "i18n" {
						if f.Sel.Name == "Text" {
							index = 1
						}
						if f.Sel.Name == "NewError" {
							index = 0
						}
					}
				}
				if index < 0 || len(call.Args) <= index {
					return true
				}
				if literal, ok := call.Args[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
					key, _ := strconv.Unquote(literal.Value)
					if _, ok := messages[key]; !ok {
						t.Errorf("%s: unknown resource key %q", path, key)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestFallbackAndLocalizedErrors(t *testing.T) {
	if got := Text("ja-JP", "connection.authorization"); got != "Connection authorization" {
		t.Fatal(got)
	}
	messages["test.fallback"] = map[string]string{"en": "Fallback"}
	defer delete(messages, "test.fallback")
	if Text("zh-CN", "test.fallback") != "Fallback" {
		t.Fatal("missing Chinese must fall back to English")
	}
	cause := errors.New("network")
	err := NewError("connect.w.http.d", cause, 403)
	if !errors.Is(err, cause) {
		t.Fatal("lost wrapped error")
	}
	if got := Localize(err, "en"); got != "connect: network (HTTP 403)" {
		t.Fatal(got)
	}
	if got := Localize(NewError("version.incompatible", "1.0.0", "2.0.0"), "en"); !strings.HasPrefix(got, "Incompatible versions:") {
		t.Fatal(got)
	}
}
