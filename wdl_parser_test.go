package maple

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestWdlParser(t *testing.T) {
	bytes, err := ioutil.ReadFile("test.wdl")
	if err != nil {
		panic(err)
	}

	parser := NewWdlParser()
	lexer := NewWdlLexer()
	handler := &DefaultSyntaxErrorHandler{}

	tokens, err := lexer.Lex(string(bytes), "test.wdl", handler)
	if err != nil {
		panic(err)
	}

	tree, err := parser.ParseTokens(tokens, handler)
	if err != nil {
		panic(err)
	}

	ast := tree.Ast()

	if _, err := os.Stat("test.wdl.ast"); os.IsNotExist(err) {
		ioutil.WriteFile("test.wdl.ast", []byte(ast.PrettyString()), 0644)
	}

	bytes, err = ioutil.ReadFile("test.wdl.ast")
	if err != nil {
		panic(err)
	}

	expected := string(bytes)
	actual := ast.PrettyString()

	if actual != expected {
		t.Fatalf("Expecting ASTs to be equal")
	}
}
