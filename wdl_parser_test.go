package maple

import (
	"io/ioutil"
	"os"
	"strings"
	"testing"
)

func assertWdlAst(t *testing.T, wdlPath string) {
	t.Parallel()
	parser := NewWdlParser()
	lexer := NewWdlLexer()
	handler := &DefaultSyntaxErrorHandler{}

	bytes, err := ioutil.ReadFile(wdlPath)
	if err != nil {
		panic(err)
	}

	tokens, err := lexer.Lex(string(bytes), wdlPath, handler)
	if err != nil {
		panic(err)
	}

	tree, err := parser.ParseTokens(tokens, handler)
	if err != nil {
		panic(err)
	}

	ast := tree.Ast()

	astPath := wdlPath + ".ast"
	if _, err := os.Stat(astPath); os.IsNotExist(err) {
		ioutil.WriteFile(astPath, []byte(ast.PrettyString()), 0644)
	}

	bytes, err = ioutil.ReadFile(astPath)
	if err != nil {
		panic(err)
	}

	expected := strings.TrimRight(string(bytes), "\r\n ")
	actual := ast.PrettyString()

	if actual != expected {
		t.Fatalf("%s: Expecting AST to look like contents of %s", wdlPath, astPath)
	}
}

func TestWdlParser(t *testing.T) {
	files, err := ioutil.ReadDir("test/parse")
	if err != nil {
		panic(err)
	}

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".wdl") {
			t.Run(file.Name(), func(t *testing.T) {
				assertWdlAst(t, "test/parse/"+file.Name())
			})
		}
	}
}
