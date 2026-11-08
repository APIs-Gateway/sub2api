// This CI-only helper scans source and reads the pinned Go instrumenter's output.
// It does not implement its own approximation of Go coverage block boundaries.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"strconv"
)

type block struct {
	StartLine  int `json:"startLine"`
	StartCol   int `json:"startCol"`
	EndLine    int `json:"endLine"`
	EndCol     int `json:"endCol"`
	Statements int `json:"statements"`
}

type span struct {
	Line  int `json:"line"`
	Start int `json:"start"`
	End   int `json:"end"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func ints(expr ast.Expr) []int {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		panic("instrumenter array is not a composite literal")
	}
	var result []int
	for _, elt := range lit.Elts {
		value, ok := elt.(*ast.BasicLit)
		if !ok || value.Kind != token.INT {
			panic("instrumenter array contains a non-integer")
		}
		n, err := strconv.ParseUint(value.Value, 0, 32)
		must(err)
		result = append(result, int(n))
	}
	return result
}

func main() {
	if len(os.Args) != 3 {
		panic("usage: inventory SOURCE INSTRUMENTED_SOURCE")
	}
	source, err := os.ReadFile(os.Args[1])
	must(err)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, os.Args[2], nil, 0)
	must(err)
	var positions, statements []int
	found := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "CoverageIntegrity" {
				continue
			}
			found++
			if len(value.Values) != 1 {
				panic("instrumenter initializer missing")
			}
			init, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				panic("instrumenter initializer has unexpected type")
			}
			fields := make(map[string]bool)
			for _, elt := range init.Elts {
				pair, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					panic("instrumenter initializer missing field names")
				}
				key, ok := pair.Key.(*ast.Ident)
				if !ok || fields[key.Name] {
					panic("instrumenter initializer duplicate field")
				}
				fields[key.Name] = true
				switch key.Name {
				case "Pos":
					positions = ints(pair.Value)
				case "NumStmt":
					statements = ints(pair.Value)
				default:
					panic("instrumenter initializer unknown field")
				}
			}
			if len(fields) != 2 {
				panic("instrumenter initializer incomplete")
			}
		}
	}
	if found != 1 || len(positions) != 3*len(statements) {
		panic("instrumenter inventory incomplete")
	}
	blocks := make([]block, 0, len(statements))
	for i, n := range statements {
		blocks = append(blocks, block{positions[3*i], positions[3*i+2] & 65535, positions[3*i+1], positions[3*i+2] >> 16, n})
	}
	// Token spans exclude comments, whitespace and inserted semicolons. Retain
	// complete multiline token spans, including newlines inside raw strings.
	scanSet := token.NewFileSet()
	scanFile := scanSet.AddFile(os.Args[1], -1, len(source))
	var scan scanner.Scanner
	scan.Init(scanFile, source, func(pos token.Position, message string) {
		panic(fmt.Sprintf("%s: %s", pos, message))
	}, 0)
	tokens := make([]span, 0)
	for {
		pos, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		if kind == token.SEMICOLON && literal == "\n" {
			continue
		}
		offset := scanFile.Offset(pos)
		length := len(literal)
		if length == 0 {
			length = len(kind.String())
		}
		if kind == token.STRING && source[offset] == '`' {
			// go/scanner removes CR bytes from a raw string's returned literal;
			// positions must still describe the exact physical source bytes.
			end := bytes.IndexByte(source[offset+1:], '`')
			if end < 0 {
				panic("unterminated raw string")
			}
			length = end + 2
		}
		if offset+length > len(source) {
			panic("token outside source")
		}
		for start := offset; start < offset+length; {
			end := start
			for end < offset+length && source[end] != '\n' {
				end++
			}
			if end > start {
				p := scanFile.PositionFor(scanFile.Pos(start), false)
				tokens = append(tokens, span{p.Line, p.Column, p.Column + end - start})
			}
			start = end + 1
		}
	}
	must(json.NewEncoder(os.Stdout).Encode(struct {
		Blocks []block `json:"blocks"`
		Tokens []span  `json:"tokens"`
	}{blocks, tokens}))
}
