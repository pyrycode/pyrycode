// Command daemon-test-inventory reads Go syntax without compiling or running tests.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type function struct {
	ContextSHA256   string   `json:"context_sha256"`
	SourceSHA256    string   `json:"source_sha256"`
	Name            string   `json:"name"`
	File            string   `json:"file"`
	Test            bool     `json:"test"`
	LiveTagged      bool     `json:"live_tagged"`
	References      []string `json:"references"`
	ProviderCommand bool     `json:"provider_command"`
	EnvGates        []string `json:"env_gates"`
}

func scan(dir string) ([]function, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fs := token.NewFileSet()
	type declaration struct {
		file *ast.File
		fn   *ast.FuncDecl
		path string
	}
	var declarations []declaration
	names := map[string]bool{}
	var context strings.Builder
	for _, path := range files {
		f, err := parser.ParseFile(fs, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Recv != nil {
				context.WriteString(filepath.Base(path))
				context.Write(raw[fs.Position(decl.Pos()).Offset:fs.Position(decl.End()).Offset])
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			if names[fn.Name.Name] {
				return nil, fmt.Errorf("duplicate function %s", fn.Name.Name)
			}
			names[fn.Name.Name] = true
			declarations = append(declarations, declaration{f, fn, path})
		}
	}
	// Assertion helpers are imported by live roots; their bodies are part of
	// the reviewed boundary even though this syntactic graph does not type-resolve
	// calls into another package.
	for _, pkg := range []string{"scenario", "claudetape"} {
		shared, err := filepath.Glob(filepath.Join(dir, "..", "internal", pkg, "*.go"))
		if err != nil {
			return nil, err
		}
		for _, path := range shared {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			context.WriteString(filepath.Join(pkg, filepath.Base(path)))
			context.Write(raw)
		}
	}
	// MCP replay invokes current command handlers through a test-only hook. Its
	// transport, hook/router and actual production MCP handlers are also oracle
	// dependencies: changing any of them must invalidate boundary fingerprints.
	extra := []struct{ label, pattern string }{
		{"internal/e2emcp", filepath.Join(dir, "..", "..", "e2emcp", "*.go")},
		{"internal/acp", filepath.Join(dir, "..", "..", "acp", "*.go")},
		{"cmd/pyry", filepath.Join(dir, "..", "..", "..", "cmd", "pyry", "mcp*.go")},
		{"cmd/pyry", filepath.Join(dir, "..", "..", "..", "cmd", "pyry", "main.go")},
		{"cmd/pyry", filepath.Join(dir, "..", "..", "..", "cmd", "pyry", "jsonrpc_stdio.go")},
	}
	for _, source := range extra {
		paths, err := filepath.Glob(source.pattern)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("missing boundary dependency %s", source.label)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			context.WriteString(filepath.Join(source.label, filepath.Base(path)))
			context.Write(raw)
		}
	}
	contextSum := sha256.Sum256([]byte(context.String()))
	contextDigest := hex.EncodeToString(contextSum[:])
	var out []function
	for _, d := range declarations {
		raw, err := os.ReadFile(d.path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw[fs.Position(d.fn.Pos()).Offset:fs.Position(d.fn.End()).Offset])
		item := function{ContextSHA256: contextDigest, SourceSHA256: hex.EncodeToString(sum[:]), Name: d.fn.Name.Name, File: filepath.Base(d.path), References: []string{}, EnvGates: []string{}}
		for _, group := range d.file.Comments {
			for _, c := range group.List {
				if c.Pos() < d.file.Package && strings.HasPrefix(c.Text, "//go:build ") {
					expr, err := constraint.Parse(c.Text)
					if err != nil {
						return nil, err
					}
					item.LiveTagged = expr.Eval(func(tag string) bool { return tag == "e2e_realclaude" }) && !expr.Eval(func(string) bool { return false })
				}
			}
		}
		if strings.HasPrefix(item.Name, "Test") && len(item.Name) > 4 && d.fn.Type.Params != nil && len(d.fn.Type.Params.List) == 1 && d.fn.Type.Results == nil {
			if pointer, ok := d.fn.Type.Params.List[0].Type.(*ast.StarExpr); ok {
				if sel, ok := pointer.X.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "testing" && sel.Sel.Name == "T" {
						item.Test = true
					}
				}
			}
		}
		refs, gates := map[string]bool{}, map[string]bool{}
		literal := func(expr ast.Expr) string {
			if v, ok := expr.(*ast.BasicLit); ok && v.Kind == token.STRING {
				s, _ := strconv.Unquote(v.Value)
				return s
			}
			return ""
		}
		ast.Inspect(d.fn.Body, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && names[id.Name] {
				refs[id.Name] = true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "exec" && (sel.Sel.Name == "LookPath" || sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				index := 0
				if sel.Sel.Name == "CommandContext" {
					index = 1
				}
				if len(call.Args) > index && literal(call.Args[index]) == "claude" {
					item.ProviderCommand = true
				}
			}
			if pkg.Name == "os" && sel.Sel.Name == "Getenv" && len(call.Args) == 1 {
				name := literal(call.Args[0])
				if strings.HasPrefix(name, "PYRY_") {
					gates[name] = true
				}
			}
			return true
		})
		for name := range refs {
			item.References = append(item.References, name)
		}
		sort.Strings(item.References)
		for name := range gates {
			item.EnvGates = append(item.EnvGates, name)
		}
		sort.Strings(item.EnvGates)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func main() {
	dir := flag.String("directory", "internal/e2e/realclaude", "Go package source directory")
	flag.Parse()
	rows, err := scan(*dir)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(rows)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
