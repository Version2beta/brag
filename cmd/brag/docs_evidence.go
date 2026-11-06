package main

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

var documentationExtensions = map[string]bool{".md": true, ".markdown": true, ".rst": true, ".adoc": true, ".txt": true, ".html": true, ".htm": true}

type countedRatio struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value"`
	Units       string   `json:"units"`
}
type docFileCounts struct {
	Total                              int            `json:"tracked_document_files"`
	MarkedGenerated                    int            `json:"canonical_generated_marker_files"`
	UnknownGeneration                  int            `json:"generation_unknown_files"`
	GeneratedGo                        int            `json:"marked_generated_go_files"`
	GoGenerateFiles                    int            `json:"go_generate_source_files"`
	GoGenerateDirectives               int            `json:"go_generate_directives"`
	TemplateConfigFiles                int            `json:"recognized_template_config_files"`
	PackageCount                       int            `json:"parsed_go_packages"`
	PackagesWithComments               int            `json:"packages_with_comments"`
	PackageCommentCoverage             countedRatio   `json:"package_comment_coverage"`
	ExportedSymbols                    int            `json:"exported_symbols"`
	ExportedSymbolsDocumented          int            `json:"exported_symbols_with_comments"`
	ExportedSymbolCoverage             countedRatio   `json:"exported_symbol_comment_coverage"`
	ExportedInterfaceMethods           int            `json:"exported_interface_methods"`
	ExportedInterfaceMethodsDocumented int            `json:"exported_interface_methods_with_comments"`
	InterfaceMethodCoverage            countedRatio   `json:"exported_interface_method_comment_coverage"`
	UnsupportedLanguages               map[string]int `json:"unsupported_language_files,omitempty"`
	SkippedFiles                       int            `json:"skipped_files"`
	ParseFailures                      []string       `json:"parse_failures,omitempty"`
	ParseFailuresTruncated             bool           `json:"parse_failures_truncated"`
}
type docSnapshot struct {
	Status         string        `json:"status"`
	Snapshot       string        `json:"snapshot"`
	Method         string        `json:"method"`
	Counts         docFileCounts `json:"counts"`
	FilesTruncated bool          `json:"files_truncated"`
}
type docFileChange struct {
	Path           string `json:"path"`
	Change         string `json:"change"`
	GeneratedClass string `json:"generated_class"`
}
type docComparison struct {
	From             string          `json:"from"`
	To               string          `json:"to"`
	Added            int             `json:"added"`
	Removed          int             `json:"removed"`
	Modified         int             `json:"modified"`
	Unchanged        int             `json:"unchanged"`
	Details          []docFileChange `json:"details"`
	DetailsTruncated bool            `json:"details_truncated"`
}
type documentationEvidence struct {
	Status      string          `json:"status"`
	Method      string          `json:"method"`
	Limits      string          `json:"limits"`
	Snapshots   []docSnapshot   `json:"snapshots"`
	Comparisons []docComparison `json:"comparisons"`
	Limitations []string        `json:"limitations"`
}
type docFile struct {
	hash      string
	generated string
}
type docMap map[string]docFile

func buildDocumentationEvidence(root string, inputs []snapshotInput) documentationEvidence {
	e := documentationEvidence{Status: "measured", Method: "Go parser comments plus Git-tracked documentation path/content identity; canonical generated marker only; generators are never run", Limits: fmt.Sprintf("Per snapshot: %d Go files, %d bytes/file, %d total Go bytes; at most %d documentation files use the same per-file/total byte caps; changed-file details cap at %d; parse-failure paths cap at 50.", complexityFileLimit, complexityFileBytes, complexityTotalBytes, 500, 100), Limitations: []string{"Comment presence is not documentation quality", "Unmarked documentation generation is classified unknown, not hand-authored", "Generated outputs are not attributed to people", "Go build tags and package build selection are not evaluated", "Recognized generator/template patterns: go:generate, paths containing templates/ or codegen/, and extensions .tmpl/.template/.gotmpl"}}
	maps := []docMap{}
	for _, in := range inputs {
		s, m := loadDocSnapshot(root, in)
		e.Snapshots = append(e.Snapshots, s)
		maps = append(maps, m)
		if s.Status != "measured" {
			e.Status = "partial"
		}
	}
	if len(maps) == 3 {
		e.Comparisons = append(e.Comparisons, compareDocFiles("baseline", "HEAD", maps[0], maps[1]), compareDocFiles("HEAD", "current_tracked_worktree", maps[1], maps[2]))
	} else if len(maps) == 2 {
		e.Comparisons = append(e.Comparisons, compareDocFiles("HEAD", "current_tracked_worktree", maps[0], maps[1]))
	}
	return e
}
func loadDocSnapshot(root string, in snapshotInput) (docSnapshot, docMap) {
	s := docSnapshot{Status: "measured", Snapshot: in.label, Method: "tracked regular files; Go AST declarations; file identity is Git blob ID or SHA-256 overlay content", Counts: docFileCounts{UnsupportedLanguages: map[string]int{}}}
	files := docMap{}
	paths, hashes, modes, err := snapshotPaths(root, in)
	if err != nil {
		s.Status = "failed"
		return s, files
	}
	total, goFiles, docBytes := 0, 0, 0
	packages := map[string]bool{}
	packageDocs := map[string]bool{}
	exportedSymbols := map[string]bool{}
	exportedInterfaceMethods := map[string]bool{}
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		if source(p) && ext != ".go" {
			s.Counts.UnsupportedLanguages[ext]++
		}
		if docFormat(ext) {
			if len(files) >= 500 {
				s.Status = "partial"
				s.FilesTruncated = true
				s.Counts.SkippedFiles++
				continue
			}
			b, ok, skip := readSnapshotFile(root, in, p, hashes[p], modes[p], docBytes)
			if skip {
				s.Status = "partial"
				s.Counts.SkippedFiles++
				continue
			}
			if !ok {
				continue
			}
			docBytes += len(b)
			class := generatedClass(b)
			sum := sha256.Sum256(b)
			hash := fmt.Sprintf("sha256:%x", sum)
			files[p] = docFile{hash, class}
			s.Counts.Total++
			if class == "generated" {
				s.Counts.MarkedGenerated++
			} else {
				s.Counts.UnknownGeneration++
			}
			continue
		}
		if ext != ".go" {
			if generatorTemplatePath(p) {
				s.Counts.TemplateConfigFiles++
			}
			continue
		}
		if goFiles >= complexityFileLimit {
			s.Status = "partial"
			s.Counts.SkippedFiles++
			continue
		}
		goFiles++
		b, ok, skip := readSnapshotFile(root, in, p, hashes[p], modes[p], total)
		if skip {
			s.Status = "partial"
			s.Counts.SkippedFiles++
			continue
		}
		if !ok {
			continue
		}
		total += len(b)
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, p, b, parser.ParseComments|parser.AllErrors)
		if parseErr != nil {
			s.Status = "partial"
			if len(s.Counts.ParseFailures) >= 50 {
				s.Counts.ParseFailuresTruncated = true
			} else {
				s.Counts.ParseFailures = append(s.Counts.ParseFailures, p)
			}
			continue
		}
		if generatedGoClass(b, fset.Position(f.Package).Offset) == "generated" {
			s.Counts.GeneratedGo++
		}
		fileHasGenerate := false
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
				if strings.HasPrefix(line, "go:generate ") {
					s.Counts.GoGenerateDirectives++
					fileHasGenerate = true
				}
			}
		}
		if fileHasGenerate {
			s.Counts.GoGenerateFiles++
		}
		pkg := filepath.Dir(p) + "/" + f.Name.Name
		packages[pkg] = true
		if f.Doc != nil && len(f.Doc.List) > 0 {
			packageDocs[pkg] = true
		}
		collectExportedGoSymbols(f, pkg, exportedSymbols, exportedInterfaceMethods)
	}
	s.Counts.ExportedSymbols = len(exportedSymbols)
	for _, documented := range exportedSymbols {
		if documented {
			s.Counts.ExportedSymbolsDocumented++
		}
	}
	s.Counts.ExportedInterfaceMethods = len(exportedInterfaceMethods)
	for _, documented := range exportedInterfaceMethods {
		if documented {
			s.Counts.ExportedInterfaceMethodsDocumented++
		}
	}
	s.Counts.PackageCount = len(packages)
	s.Counts.PackagesWithComments = len(packageDocs)
	s.Counts.PackageCommentCoverage = ratioCount(s.Counts.PackagesWithComments, s.Counts.PackageCount, "documented packages / parsed packages")
	s.Counts.ExportedSymbolCoverage = ratioCount(s.Counts.ExportedSymbolsDocumented, s.Counts.ExportedSymbols, "exported declarations with attached doc / exported declaration symbols")
	s.Counts.InterfaceMethodCoverage = ratioCount(s.Counts.ExportedInterfaceMethodsDocumented, s.Counts.ExportedInterfaceMethods, "documented exported interface methods / exported interface methods")
	if s.Counts.SkippedFiles > 0 || len(s.Counts.ParseFailures) > 0 {
		s.Status = "partial"
	}
	return s, files
}
func collectExportedGoSymbols(f *ast.File, pkg string, symbols, interfaceMethods map[string]bool) {
	add := func(m map[string]bool, key string, documented bool) { m[key] = m[key] || documented }
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			if ast.IsExported(x.Name.Name) {
				key := pkg + "/decl/" + x.Name.Name
				if x.Recv != nil && len(x.Recv.List) > 0 {
					key = pkg + "/method/" + receiverName(x.Recv.List[0].Type) + "/" + x.Name.Name
				}
				add(symbols, key, x.Doc != nil && len(x.Doc.List) > 0)
			}
		case *ast.GenDecl:
			for _, sp := range x.Specs {
				switch y := sp.(type) {
				case *ast.TypeSpec:
					doc := y.Doc
					if doc == nil {
						doc = x.Doc
					}
					if ast.IsExported(y.Name.Name) {
						add(symbols, pkg+"/decl/"+y.Name.Name, doc != nil && len(doc.List) > 0)
					}
					if iface, ok := y.Type.(*ast.InterfaceType); ok {
						for _, field := range iface.Methods.List {
							for _, name := range field.Names {
								if ast.IsExported(name.Name) {
									add(interfaceMethods, pkg+"/interface/"+y.Name.Name+"/"+name.Name, field.Doc != nil && len(field.Doc.List) > 0)
								}
							}
						}
					}
				case *ast.ValueSpec:
					doc := y.Doc
					if doc == nil {
						doc = x.Doc
					}
					for _, name := range y.Names {
						if ast.IsExported(name.Name) {
							add(symbols, pkg+"/decl/"+name.Name, doc != nil && len(doc.List) > 0)
						}
					}
				}
			}
		}
	}
}
func ratioCount(n, d int, units string) countedRatio {
	r := countedRatio{Numerator: n, Denominator: d, Units: units}
	if d > 0 {
		v := float64(n) / float64(d)
		r.Value = &v
	}
	return r
}
func docFormat(ext string) bool { return documentationExtensions[ext] }
func generatedClass(b []byte) string {
	limit := len(b)
	if limit > 4096 {
		limit = 4096
	}
	for _, line := range strings.Split(string(b[:limit]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "// Code generated ") && strings.Contains(line, " DO NOT EDIT.") {
			return "generated"
		}
		if strings.HasPrefix(line, "<!-- Code generated ") && strings.Contains(line, " DO NOT EDIT. -->") {
			return "generated"
		}
	}
	return "unknown"
}
func generatedGoClass(b []byte, packageOffset int) string {
	if packageOffset < 0 || packageOffset > len(b) {
		return "unknown"
	}
	return generatedClass(b[:packageOffset])
}
func generatorTemplatePath(p string) bool {
	q := strings.ToLower(filepath.ToSlash(p))
	ext := filepath.Ext(q)
	return ext == ".tmpl" || ext == ".template" || ext == ".gotmpl" || strings.Contains("/"+q, "/templates/") || strings.Contains("/"+q, "/codegen/") || filepath.Base(q) == "buf.gen.yaml" || filepath.Base(q) == "stringer.conf"
}
func compareDocFiles(from, to string, a, b docMap) docComparison {
	c := docComparison{From: from, To: to, Details: []docFileChange{}}
	ks := map[string]bool{}
	for k := range a {
		ks[k] = true
	}
	for k := range b {
		ks[k] = true
	}
	ordered := []string{}
	for k := range ks {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		x, xok := a[k]
		y, yok := b[k]
		typ := ""
		class := "unknown"
		switch {
		case !xok:
			c.Added++
			typ = "added"
			class = y.generated
		case !yok:
			c.Removed++
			typ = "removed"
			class = x.generated
		case x.hash == y.hash:
			c.Unchanged++
			continue
		default:
			c.Modified++
			typ = "modified"
			class = y.generated
		}
		if len(c.Details) < 100 {
			c.Details = append(c.Details, docFileChange{k, typ, class})
		} else {
			c.DetailsTruncated = true
		}
	}
	return c
}

// The AST declaration denominator counts each exported function/method, type, const/var name once; grouped declaration comments attach to each contained symbol.
