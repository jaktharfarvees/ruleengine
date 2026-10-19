package ruleengine_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/arrowhr/ruleengine"

// subsystemPackages are the packages INV1 names. The root package imports them
// and adapts between them; none of them may import the root back.
var subsystemPackages = []string{
	"rule", "hierarchy", "resolve", "fact", "dataset", "registry",
	"engine", "store", "manifest", "authz", "telemetry", "audit",
	"contextbuilder", "schema", "internal",
}

// cmd/, examples/ and ruletest/ are deliberately absent from the list above:
// they wire the library together and so may import the root package.

// TestImportDirectionIsOneWay enforces INV1. A subsystem that imports the root
// package creates an import cycle the moment the root needs it back, and makes
// the subsystem untestable on its own, so this is checked rather than trusted.
func TestImportDirectionIsOneWay(t *testing.T) {
	root := "."
	fileSet := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative := filepath.ToSlash(path)
		owner := strings.SplitN(relative, "/", 2)[0]
		if !slices.Contains(subsystemPackages, owner) {
			return nil
		}
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if value == modulePath {
				t.Errorf("%s imports the root package %s; INV1 requires the dependency to run the other way", relative, modulePath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no subsystem files were inspected; the walk or the package list is wrong")
	}
}

// TestNoFixedPrecedenceVocabulary enforces the acceptance criterion that the
// superseded six-level precedence model left no trace. Precedence comes from
// the tenant's own hierarchy (D17), so these names must not reappear.
func TestNoFixedPrecedenceVocabulary(t *testing.T) {
	banned := []string{
		"LEGAL_ENTITY", "EMPLOYEE_GROUP", "PrecedenceLevel",
		"AnchorEmployee", "EmployeeExceptionOrder", "IsEmployeeException", "ForEmployee",
		`"kind":"employee"`, `"kind": "employee"`,
	}
	// GLOBAL and COUNTRY are banned as standalone level names; employmentCountry
	// is an unrelated request field and must not trip the check.
	bannedWords := []string{"GLOBAL", "COUNTRY"}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		extension := filepath.Ext(path)
		if extension != ".go" && extension != ".json" {
			return nil
		}
		if strings.HasSuffix(path, "architecture_test.go") {
			return nil
		}
		if filepath.ToSlash(path) == "testdata/invalid/employee-anchor.json" {
			// This is the negative contract fixture that proves the retired
			// wire shape is rejected by both schema and Go validation.
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		for _, name := range banned {
			if strings.Contains(text, name) {
				t.Errorf("%s contains %q; precedence is derived from the tenant hierarchy, not fixed levels", filepath.ToSlash(path), name)
			}
		}
		for _, word := range bannedWords {
			for _, field := range strings.FieldsFunc(text, func(r rune) bool {
				return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
			}) {
				if field == word {
					t.Errorf("%s uses %q as a standalone identifier; there is no fixed precedence level", filepath.ToSlash(path), word)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func shouldSkipDir(name string) bool {
	return name == ".git" || name == ".cache"
}

// TestEveryExportedSymbolIsDocumented enforces the acceptance criterion that
// go doc reads sensibly for every package.
//
// This library is consumed by fourteen teams who will read its documentation
// rather than its source, so an undocumented exported symbol is a gap in the
// product. The check is here rather than left to a linter because the linter
// is not runnable in every environment and this is a release gate.
func TestEveryExportedSymbolIsDocumented(t *testing.T) {
	fileSet := token.NewFileSet()
	var undocumented []string
	checked := 0

	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// internal/ is not public API, so its symbols need no doc comment for
		// a consumer's sake; they are commented anyway, but not gated here.
		if strings.HasPrefix(filepath.ToSlash(path), "internal/") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range parsed.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if !typed.Name.IsExported() {
					continue
				}
				// A method on an unexported type is not reachable by a
				// consumer, so it is not part of the documented surface.
				if typed.Recv != nil && !exportedReceiver(typed.Recv) {
					continue
				}
				checked++
				if typed.Doc == nil || len(typed.Doc.List) == 0 {
					undocumented = append(undocumented,
						fmt.Sprintf("%s: func %s", filepath.ToSlash(path), typed.Name.Name))
				}
			case *ast.GenDecl:
				checkGenDecl(typed, filepath.ToSlash(path), &checked, &undocumented)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no exported symbols were inspected; the walk is wrong")
	}
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Fatalf("%d exported symbols have no doc comment:\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "))
	}
	t.Logf("checked %d exported symbols", checked)
}

// checkGenDecl inspects a type, const or var declaration. A grouped
// declaration may carry one comment for the group, which documents every
// member, so a member is only undocumented when neither it nor its group has
// a comment.
func checkGenDecl(declaration *ast.GenDecl, path string, checked *int, undocumented *[]string) {
	grouped := declaration.Doc != nil && len(declaration.Doc.List) > 0
	for _, spec := range declaration.Specs {
		switch typed := spec.(type) {
		case *ast.TypeSpec:
			if !typed.Name.IsExported() {
				continue
			}
			*checked++
			if !grouped && (typed.Doc == nil || len(typed.Doc.List) == 0) {
				*undocumented = append(*undocumented, fmt.Sprintf("%s: type %s", path, typed.Name.Name))
			}
		case *ast.ValueSpec:
			for _, name := range typed.Names {
				if !name.IsExported() {
					continue
				}
				*checked++
				if !grouped && (typed.Doc == nil || len(typed.Doc.List) == 0) {
					*undocumented = append(*undocumented, fmt.Sprintf("%s: %s", path, name.Name))
				}
			}
		}
	}
}

func exportedReceiver(receiver *ast.FieldList) bool {
	if receiver == nil || len(receiver.List) == 0 {
		return false
	}
	switch typed := receiver.List[0].Type.(type) {
	case *ast.StarExpr:
		identifier, ok := typed.X.(*ast.Ident)
		return ok && identifier.IsExported()
	case *ast.Ident:
		return typed.IsExported()
	default:
		return false
	}
}
