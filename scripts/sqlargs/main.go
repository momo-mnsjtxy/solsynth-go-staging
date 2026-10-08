// SPDX-License-Identifier: AGPL-3.0-only
// Extract statically resolvable SQL expressions, never their argument values.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type source struct {
	file *ast.File
	key  string
}
type call struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	SQL  string `json:"sql"`
	Args int    `json:"args"`
}
type report struct {
	Calls      []call `json:"calls"`
	Candidates int    `json:"candidates"`
	Unresolved int    `json:"unresolved"`
	Variadic   int    `json:"variadic"`
	NonSQL     int    `json:"non_sql"`
}

var startsSQL = regexp.MustCompile(`(?is)^\s*(?:(?:--[^\n]*\n|/\*.*?\*/)\s*)*(SELECT|INSERT|UPDATE|DELETE|WITH|CREATE|ALTER|DROP|DO|SHOW|EXPLAIN|SET|SAVEPOINT|ROLLBACK|RELEASE|TRUNCATE)\b`)

func resolve(e ast.Expr, defs map[string]ast.Expr, depth int) (string, bool) {
	if depth > 20 || e == nil {
		return "", false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, err := strconv.Unquote(x.Value)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return resolve(x.X, defs, depth+1)
	case *ast.Ident:
		return resolve(defs[x.Name], defs, depth+1)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			a, ok := resolve(x.X, defs, depth+1)
			b, ok2 := resolve(x.Y, defs, depth+1)
			return a + b, ok && ok2
		}
	}
	return "", false
}
func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	root, err := filepath.Abs(root)
	if err != nil {
		panic(err)
	}
	fs := token.NewFileSet()
	var files []source
	constants := map[string]map[string]ast.Expr{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fs, path, nil, 0)
		if err != nil {
			return err
		}
		key := filepath.Dir(path) + ":" + f.Name.Name
		files = append(files, source{f, key})
		if constants[key] == nil {
			constants[key] = map[string]ast.Expr{}
		}
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, n := range v.Names {
					if _, dup := constants[key][n.Name]; dup {
						constants[key][n.Name] = nil
						continue
					}
					if i < len(v.Values) {
						constants[key][n.Name] = v.Values[i]
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := report{Calls: []call{}}
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			idx := 0
			switch fn.Sel.Name {
			case "ExecContext", "QueryContext", "QueryRowContext":
				idx = 1
			case "Exec", "Query", "QueryRow":
			default:
				return true
			}
			out.Candidates++
			if c.Ellipsis.IsValid() {
				out.Variadic++
				return true
			}
			if len(c.Args) <= idx {
				out.Unresolved++
				return true
			}
			sql, ok := resolve(c.Args[idx], constants[f.key], 0)
			// pgx uses context as the first argument without a Context suffix.
			if !ok && idx == 0 && len(c.Args) > 1 {
				idx = 1
				sql, ok = resolve(c.Args[idx], constants[f.key], 0)
			}
			if !ok {
				out.Unresolved++
				return true
			}
			if !startsSQL.MatchString(sql) {
				out.NonSQL++
				return true
			}
			pos := fs.Position(c.Pos())
			path, _ := filepath.Rel(root, pos.Filename)
			out.Calls = append(out.Calls, call{path, pos.Line, sql, len(c.Args) - idx - 1})
			return true
		})
	}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
