package maple

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

func p(format string, a ...interface{}) (n int, err error) {
	return fmt.Printf(format+"\n", a...)
}

type terminal struct {
	id    int
	idStr string
}
type nonTerminal struct {
	id        int
	idStr     string
	firstSet  []int
	followSet []int
	rules     []int
}

func (nt *nonTerminal) CanStartWith(terminalId int) bool {
	for _, i := range nt.firstSet {
		if i == terminalId {
			return true
		}
	}
	return false
}
func (nt *nonTerminal) CanBeFollowedBy(terminalId int) bool {
	for _, i := range nt.followSet {
		if i == terminalId {
			return true
		}
	}
	return false
}

type rule struct {
	id       int
	str      string
	firstSet []int
}

func (rule *rule) CanStartWith(terminalId int) bool {
	for _, i := range rule.firstSet {
		if i == terminalId {
			return true
		}
	}
	return false
}

type Token struct {
	terminal     *terminal
	sourceString string
	resource     string
	line         int
	col          int
}

func (t *Token) String() string {
	return fmt.Sprintf(`<%s:%d:%d %s "%s">`,
		t.resource,
		t.line,
		t.col,
		t.terminal.idStr,
		base64.StdEncoding.EncodeToString([]byte(t.sourceString)))
}
func (t *Token) PrettyString() string {
	return t.String()
}
func (t *Token) Ast() AstNode {
	return t
}

type TokenStream struct {
	tokens []*Token
	index  int
}

func (ts *TokenStream) current() *Token {
	if ts.index < len(ts.tokens) {
		return ts.tokens[ts.index]
	}
	return nil
}
func (ts *TokenStream) advance() *Token {
	ts.index = ts.index + 1
	return ts.current()
}
func (ts *TokenStream) last() *Token {
	if len(ts.tokens) > 0 {
		return ts.tokens[len(ts.tokens)-1]
	}
	return nil
}

type parseTree struct {
	nonterminal       *nonTerminal
	children          []treeNode
	astTransform      interface{}
	isExpr            bool
	isNud             bool
	isPrefix          bool
	isInfix           bool
	nudMorphemeCount  int
	isExprNud         bool // true for rules like _expr := {_expr} + {...}
	list_separator_id int
	list              bool
}
type treeNode interface {
	String() string
	PrettyString() string
	Ast() AstNode
}

func (tree *parseTree) Add(node interface{}) error {
	switch t := node.(type) {
	case *parseTree:
		tree.children = append(tree.children, t)
	case *Token:
		tree.children = append(tree.children, t)
	default:
		return errors.New("only *parseTree and *Token allowed to be added")
	}
	return nil
}
func (tree *parseTree) isCompoundNud() bool {
	if len(tree.children) > 0 {
		switch firstChild := tree.children[0].(type) {
		case *parseTree:
			return firstChild.isNud && !firstChild.isPrefix && !tree.isExprNud && !tree.isInfix
		}
	}
	return false
}
func (tree *parseTree) Ast() AstNode {
	if tree.list == true {
		r := AstList{}
		if len(tree.children) == 0 {
			return &r
		}
		for _, child := range tree.children {
			switch t := child.(type) {
			case *Token:
				if tree.list_separator_id == t.terminal.id {
					continue
				}
				r = append(r, t.Ast())
			default:
				r = append(r, t.Ast())
			}
		}
		return &r
	} else if tree.isExpr {
		switch transform := tree.astTransform.(type) {
		case *AstTransformSubstitution:
			return tree.children[transform.index].Ast()
		case *AstTransformNodeCreator:
			attributes := make(map[string]AstNode)
			var child treeNode
			var firstChild interface{}
			if len(tree.children) > 0 {
				firstChild = tree.children[0]
			}
			_, is_tree := firstChild.(*parseTree)
			for s, i := range transform.parameters {
				// 36 is a dollar sign: '$'
				if i == 36 {
					child = tree.children[0]
				} else if tree.isCompoundNud() {
					firstChild := tree.children[0].(*parseTree)
					if i < firstChild.nudMorphemeCount {
						child = firstChild.children[i]
					} else {
						i = i - firstChild.nudMorphemeCount + 1
						child = tree.children[i]
					}
				} else if len(tree.children) == 1 && !is_tree {
					// TODO: I don't think this should ever be called
					fmt.Println("!!!!! THIS CODE ACTUALLY IS CALLED")
					child = tree.children[0]
					return child
				} else {
					child = tree.children[i]
				}
				attributes[s] = child.Ast()
			}
			return &Ast{transform.name, attributes, transform.keys}
		}
	} else {
		switch transform := tree.astTransform.(type) {
		case *AstTransformSubstitution:
			return tree.children[transform.index].Ast()
		case *AstTransformNodeCreator:
			attributes := make(map[string]AstNode)
			for s, i := range transform.parameters {
				attributes[s] = tree.children[i].Ast()
			}
			return &Ast{transform.name, attributes, transform.keys}
		}
		if len(tree.children) > 0 {
			return tree.children[0].Ast()
		}
	}
	return &EmptyAst{}
}
func (tree *parseTree) String() string {
	return parseTreeToString(tree, 0, 1)
}
func (tree *parseTree) PrettyString() string {
	return parseTreeToString(tree, 2, 0)
}
func parseTreeToString(treenode interface{}, indent int, indentLevel int) string {
	indentStr := ""
	if indent > 0 {
		indentStr = strings.Repeat(" ", indent*indentLevel)
	}
	switch node := treenode.(type) {
	case *parseTree:
		childStrings := make([]string, len(node.children))
		for index, child := range node.children {
			childStrings[index] = parseTreeToString(child, indent, indentLevel+1)
		}
		if indent == 0 || len(node.children) == 0 {
			return fmt.Sprintf("%s(%s: %s)", indentStr, node.nonterminal.idStr, strings.Join(childStrings, ", "))
		} else {
			return fmt.Sprintf("%s(%s:\n%s\n%s)", indentStr, node.nonterminal.idStr, strings.Join(childStrings, ",\n"), indentStr)
		}
	case *Token:
		return fmt.Sprintf("%s%s", indentStr, node.String())
	default:
		panic(fmt.Sprintf("parseTreeToString() called on %t", node))
	}
}

type AstNode interface {
	String() string
	PrettyString() string
}
type Ast struct {
	name       string
	attributes map[string]AstNode
	keys       []string // sorted keys into 'attributes'
}
type EmptyAst struct{}

func (ast *EmptyAst) String() string {
	return "None"
}
func (ast *EmptyAst) PrettyString() string {
	return "None"
}
func (ast *Ast) String() string {
	return astToString(ast, 0, 0)
}
func (ast *Ast) PrettyString() string {
	return astToString(ast, 2, 0)
}
func astToString(ast interface{}, indent int, indentLevel int) string {
	indentStr := ""
	nextIndentStr := ""
	attrPrefix := ""
	i := 0
	if indent > 0 {
		indentStr = strings.Repeat(" ", indent*indentLevel)
		nextIndentStr = strings.Repeat(" ", indent*(indentLevel+1))
		attrPrefix = nextIndentStr
	}
	switch node := ast.(type) {
	case *Ast:
		i = 0
		childStrings := make([]string, len(node.attributes))
		for _, key := range node.keys {
			childStrings[i] = fmt.Sprintf("%s%s=%s", attrPrefix, key, astToString(node.attributes[key], indent, indentLevel+1))
			i++
		}
		if indent > 0 {
			return fmt.Sprintf("(%s:\n%s\n%s)", node.name, strings.Join(childStrings, ",\n"), indentStr)
		} else {
			return fmt.Sprintf("(%s: %s)", node.name, strings.Join(childStrings, ", "))
		}
	case *AstList:
		childStrings := make([]string, len(*node))
		i = 0
		for _, subnode := range *node {
			childStrings[i] = fmt.Sprintf("%s%s", attrPrefix, astToString(subnode, indent, indentLevel+1))
			i++
		}
		if indent == 0 || len(*node) == 0 {
			return fmt.Sprintf("[%s]", strings.Join(childStrings, ", "))
		} else {
			return fmt.Sprintf("[\n%s\n%s]", strings.Join(childStrings, ",\n"), indentStr)
		}
	case *Token:
		return node.String()
	case *EmptyAst:
		return "None"
	default:
		panic(fmt.Sprintf("Wrong type to astToString(): %v (%t)", ast, ast))
	}
	return ""
}

type AstTransformSubstitution struct {
	index int
}

func (t *AstTransformSubstitution) String() string {
	return fmt.Sprintf("$%d", t.index)
}

type AstTransformNodeCreator struct {
	name       string
	parameters map[string]int // TODO: I think this is the right type?
	keys       []string
}

func (t *AstTransformNodeCreator) String() string {
	strs := make([]string, len(t.parameters))
	i := 0
	for _, k := range t.keys {
		strs[i] = fmt.Sprintf("%s=$%d", k, t.parameters[k])
		i++
	}
	return fmt.Sprintf("%s(%s)", t.name, strings.Join(strs, ", "))
}

type AstList []AstNode

func (ast *AstList) String() string {
	return astToString(ast, 0, 0)
}
func (ast *AstList) PrettyString() string {
	return astToString(ast, 2, 0)
}

type SyntaxError struct {
	message string
}

func (err *SyntaxError) Error() string {
	return err.message
}

type SyntaxErrors []*SyntaxError

func (errs SyntaxErrors) Error() string {
	strs := make([]string, len(errs))
	for i, e := range errs {
		strs[i] = e.Error()
	}
	if len(strs) > 0 {
		return strs[0]
	}
	return ""
	//return strings.Join(strs, strings.Repeat("=", 50))
}

type SyntaxErrorHandler interface {
	unexpected_eof() *SyntaxError
	excess_tokens() *SyntaxError
	unexpected_symbol(nt string, actual_token *Token, expected_terminals []*terminal, rule string) *SyntaxError
	no_more_tokens(nt string, expected_terminal *terminal, last_token *Token) *SyntaxError
	invalid_terminal(nt string, invalid_token *Token) *SyntaxError
	unrecognized_token(s string, line, col int) *SyntaxError
	missing_list_items(method string, required, found int, last string) *SyntaxError
	missing_terminator(method string, required *terminal, terminator *terminal, last *terminal) *SyntaxError
	Error() string
}
type DefaultSyntaxErrorHandler struct {
	syntaxErrors SyntaxErrors
}

func (h *DefaultSyntaxErrorHandler) Error() string {
	return h.syntaxErrors.Error()
}
func (h *DefaultSyntaxErrorHandler) _error(str string) *SyntaxError {
	e := &SyntaxError{str}
	h.syntaxErrors = append(h.syntaxErrors, e)
	return e
}
func (h *DefaultSyntaxErrorHandler) unexpected_eof() *SyntaxError {
	return h._error("Error: unexpected end of file")
}
func (h *DefaultSyntaxErrorHandler) excess_tokens() *SyntaxError {
	return h._error("Finished parsing without consuming all tokens.")
}
func (h *DefaultSyntaxErrorHandler) unexpected_symbol(nt string, actual_token *Token, expected_terminals []*terminal, rule string) *SyntaxError {
	strs := make([]string, len(expected_terminals))
	for i, t := range expected_terminals {
		strs[i] = t.idStr
	}
	return h._error(fmt.Sprintf("Unexpected symbol (line %d, col %d) when parsing parse_%s.  Expected %s, got %s.",
		actual_token.line,
		actual_token.col,
		nt,
		strings.Join(strs, ", "),
		actual_token.String()))
}
func (h *DefaultSyntaxErrorHandler) no_more_tokens(nt string, expected_terminal *terminal, last_token *Token) *SyntaxError {
	return h._error(fmt.Sprintf("No more tokens.  Expecting %s", expected_terminal.idStr))
}
func (h *DefaultSyntaxErrorHandler) invalid_terminal(nt string, invalid_token *Token) *SyntaxError {
	return h._error(fmt.Sprintf("Invalid symbol ID: %d (%s)", invalid_token.terminal.id, invalid_token.terminal.idStr))
}
func (h *DefaultSyntaxErrorHandler) unrecognized_token(s string, line, col int) *SyntaxError {
	lines := strings.Split(s, "\n")
	bad_line := lines[line-1]
	return h._error(fmt.Sprintf("Unrecognized token on line %d, column %d:\n\n%s\n%s",
		line, col, bad_line, strings.Repeat(" ", col-1)+"^"))
}
func (h *DefaultSyntaxErrorHandler) missing_list_items(method string, required, found int, last string) *SyntaxError {
	return h._error(fmt.Sprintf("List for %s requires %d items but only %d were found.", method, required, found))
}
func (h *DefaultSyntaxErrorHandler) missing_terminator(method string, required *terminal, terminator *terminal, last *terminal) *SyntaxError {
	return h._error(fmt.Sprintf("List for %s is missing a terminator", method))
}

/*
 * Parser Code
 */
var table [][]int
var terminals []*terminal
var nonterminals []*nonTerminal
var rules []*rule
var initMutex sync.Mutex

func initTable() [][]int {
	initMutex.Lock()
	defer initMutex.Unlock()
	if table == nil {
		table = make([][]int, 59)
		table[0] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 68, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[1] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 44, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[2] = []int{27, 27, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[3] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 57, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[4] = []int{46, 46, 49, -1, -1, -1, -1, 48, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 45, -1, -1, -1, -1, -1, -1, -1, 50, -1, -1, -1, -1, -1, 47, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[5] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 61, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[6] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 26, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[7] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[8] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, 20, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 21, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[9] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 23, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[10] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[11] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[12] = []int{-1, -1, -1, -1, -1, -1, -1, 69, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[13] = []int{-1, -1, -1, -1, -1, 12, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[14] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[15] = []int{-1, -1, -1, -1, -1, -1, 35, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 34, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 34}
		table[16] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[17] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[18] = []int{-1, -1, -1, 30, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[19] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 29, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[20] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[21] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[22] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 67, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[23] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 9, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[24] = []int{-1, -1, 70, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[25] = []int{-1, -1, -1, -1, -1, -1, 60, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[26] = []int{7, 7, -1, -1, -1, 7, -1, -1, -1, -1, -1, -1, -1, -1, -1, 7, -1, -1, -1, 6, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 7, -1, -1, -1, -1, -1}
		table[27] = []int{-1, -1, -1, -1, -1, -1, 71, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[28] = []int{52, 52, 52, -1, -1, -1, -1, 52, -1, -1, -1, 52, -1, -1, 52, -1, -1, -1, -1, 51, -1, -1, 52, -1, -1, -1, -1, -1, -1, -1, 52, -1, -1, -1, -1, -1, 52, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[29] = []int{-1, -1, -1, 17, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 13, -1, -1, 14, -1, -1, -1, -1, -1, -1, -1, -1, 16, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 15, -1, -1, -1, -1}
		table[30] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[31] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 39, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[32] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 63, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[33] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[34] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[35] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[36] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 8, -1, -1, -1, -1, -1}
		table[37] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 28, -1, -1, -1, -1}
		table[38] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 19, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[39] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 24, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[40] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 66, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[41] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[42] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 65, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 65, -1, -1, -1, -1, -1, -1, -1, -1, -1, 64, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[43] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[44] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 55, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[45] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 59, -1, -1, -1, -1, -1, -1, -1, -1}
		table[46] = []int{54, 54, 54, -1, -1, -1, -1, 54, -1, -1, -1, 54, -1, -1, 53, -1, -1, -1, -1, -1, -1, -1, 54, -1, -1, -1, -1, -1, -1, -1, 54, -1, -1, -1, -1, -1, 54, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[47] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[48] = []int{-1, -1, -1, -1, -1, -1, 33, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[49] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 40, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 41}
		table[50] = []int{-1, -1, -1, -1, -1, -1, 42, -1, 42, -1, 42, -1, -1, 42, 42, -1, -1, 42, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 42, -1, 42, -1, -1, -1, 42, -1, -1, -1, -1, -1, 42, 42, -1, -1, -1, -1, -1, -1, -1, -1, 42, 42}
		table[51] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 32, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[52] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[53] = []int{38, 38, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[54] = []int{2, 2, -1, -1, -1, 2, -1, -1, -1, -1, -1, -1, -1, -1, -1, 2, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, 2, -1, -1, -1, -1, -1}
		table[55] = []int{37, 37, 37, 37, -1, 37, -1, 37, -1, -1, -1, 37, -1, -1, -1, 37, -1, -1, -1, -1, -1, -1, 37, -1, -1, -1, -1, 37, 36, -1, 37, -1, -1, -1, -1, -1, 37, -1, -1, 37, -1, -1, -1, -1, -1, -1, -1, 37, -1, -1, -1, 37, -1, -1, -1, -1}
		table[56] = []int{5, 5, -1, -1, -1, 4, -1, -1, -1, -1, -1, -1, -1, -1, -1, 3, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[57] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		table[58] = []int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	}
	return table
}
func initTerminals() []*terminal {
	initMutex.Lock()
	defer initMutex.Unlock()
	if terminals == nil {
		terminals = make([]*terminal, 56)
		terminals[0] = &terminal{0, "type"}
		terminals[1] = &terminal{1, "type_e"}
		terminals[2] = &terminal{2, "scatter"}
		terminals[3] = &terminal{3, "meta"}
		terminals[4] = &terminal{4, "double_ampersand"}
		terminals[5] = &terminal{5, "task"}
		terminals[6] = &terminal{6, "identifier"}
		terminals[7] = &terminal{7, "if"}
		terminals[8] = &terminal{8, "lsquare"}
		terminals[9] = &terminal{9, "cmd_part"}
		terminals[10] = &terminal{10, "float"}
		terminals[11] = &terminal{11, "rbrace"}
		terminals[12] = &terminal{12, "double_pipe"}
		terminals[13] = &terminal{13, "string"}
		terminals[14] = &terminal{14, "lbrace"}
		terminals[15] = &terminal{15, "workflow"}
		terminals[16] = &terminal{16, "slash"}
		terminals[17] = &terminal{17, "not"}
		terminals[18] = &terminal{18, "gt"}
		terminals[19] = &terminal{19, "as"}
		terminals[20] = &terminal{20, "gteq"}
		terminals[21] = &terminal{21, "command_start"}
		terminals[22] = &terminal{22, "call"}
		terminals[23] = &terminal{23, "fqn"}
		terminals[24] = &terminal{24, "cmd_param_end"}
		terminals[25] = &terminal{25, "qmark"}
		terminals[26] = &terminal{26, "comma"}
		terminals[27] = &terminal{27, "command"}
		terminals[28] = &terminal{28, "equal"}
		terminals[29] = &terminal{29, "percent"}
		terminals[30] = &terminal{30, "output"}
		terminals[31] = &terminal{31, "not_equal"}
		terminals[32] = &terminal{32, "boolean"}
		terminals[33] = &terminal{33, "dot"}
		terminals[34] = &terminal{34, "integer"}
		terminals[35] = &terminal{35, "cmd_attr_hint"}
		terminals[36] = &terminal{36, "while"}
		terminals[37] = &terminal{37, "lt"}
		terminals[38] = &terminal{38, "dash"}
		terminals[39] = &terminal{39, "parameter_meta"}
		terminals[40] = &terminal{40, "colon"}
		terminals[41] = &terminal{41, "cmd_param_start"}
		terminals[42] = &terminal{42, "rsquare"}
		terminals[43] = &terminal{43, "lteq"}
		terminals[44] = &terminal{44, "e"}
		terminals[45] = &terminal{45, "object"}
		terminals[46] = &terminal{46, "in"}
		terminals[47] = &terminal{47, "input"}
		terminals[48] = &terminal{48, "command_end"}
		terminals[49] = &terminal{49, "double_equal"}
		terminals[50] = &terminal{50, "import"}
		terminals[51] = &terminal{51, "runtime"}
		terminals[52] = &terminal{52, "asterisk"}
		terminals[53] = &terminal{53, "rparen"}
		terminals[54] = &terminal{54, "lparen"}
		terminals[55] = &terminal{55, "plus"}
	}
	return terminals
}
func initNonTerminals() []*nonTerminal {
	initMutex.Lock()
	defer initMutex.Unlock()
	if nonterminals == nil {
		nonterminals = make([]*nonTerminal, 59)
		var first []int
		var follow []int
		var rules []int
		first = []int{36}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{68}
		nonterminals[0] = &nonTerminal{56, "while_loop", first, follow, rules}
		first = []int{15}
		follow = []int{0, 1, 5, -1, 15}
		rules = []int{44}
		nonterminals[1] = &nonTerminal{57, "workflow", first, follow, rules}
		first = []int{0, 1}
		follow = []int{0, 1, 11}
		rules = []int{27}
		nonterminals[2] = &nonTerminal{58, "output_kv", first, follow, rules}
		first = []int{14}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{57}
		nonterminals[3] = &nonTerminal{59, "call_body", first, follow, rules}
		first = []int{0, 30, 1, 2, 22, 7, 36}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{45, 46, 47, 48, 49, 50}
		nonterminals[4] = &nonTerminal{60, "wf_body_element", first, follow, rules}
		first = []int{19}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 14, 36}
		rules = []int{61}
		nonterminals[5] = &nonTerminal{61, "alias", first, follow, rules}
		first = []int{30}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{26}
		nonterminals[6] = &nonTerminal{62, "outputs", first, follow, rules}
		first = []int{-1, 6}
		follow = []int{11}
		rules = []int{31}
		nonterminals[7] = &nonTerminal{63, "_gen8", first, follow, rules}
		first = []int{41, 9}
		follow = []int{41, 9, 48}
		rules = []int{20, 21}
		nonterminals[8] = &nonTerminal{64, "command_part", first, follow, rules}
		first = []int{41}
		follow = []int{41, 9, 48}
		rules = []int{23}
		nonterminals[9] = &nonTerminal{65, "cmd_param", first, follow, rules}
		first = []int{0, 1}
		follow = []int{25, 42, 6, 26, 55}
		rules = []int{73, 74}
		nonterminals[10] = &nonTerminal{66, "type_e", first, follow, rules}
		first = []int{38, 6, 8, 10, 44, 13, 45, 17, 14, 32, -1, 34, 54, 55}
		follow = []int{53, 42}
		rules = []int{91}
		nonterminals[11] = &nonTerminal{67, "_gen19", first, follow, rules}
		first = []int{7}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{69}
		nonterminals[12] = &nonTerminal{68, "if_stmt", first, follow, rules}
		first = []int{5}
		follow = []int{0, 1, 5, -1, 15}
		rules = []int{12}
		nonterminals[13] = &nonTerminal{69, "task", first, follow, rules}
		first = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		follow = []int{0, 1, 2, 4, 3, 5, 6, 7, 8, 10, 11, 12, 13, 17, 14, 16, 15, 18, 20, 22, 29, 24, 26, 32, 27, 34, 31, 30, 35, 36, 37, 38, 39, 40, 42, 43, 44, -1, 45, 47, 49, 51, 52, 53, 54, 55}
		rules = []int{75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 92, 93, 94, 96, 97, 99, 100, 101, 102, 103, 104, 105}
		nonterminals[14] = &nonTerminal{70, "e", first, follow, rules}
		first = []int{-1, 25, 55}
		follow = []int{6}
		rules = []int{34, 35}
		nonterminals[15] = &nonTerminal{71, "_gen9", first, follow, rules}
		first = []int{-1, 6}
		follow = []int{47, 11}
		rules = []int{58}
		nonterminals[16] = &nonTerminal{72, "_gen15", first, follow, rules}
		first = []int{-1, 50}
		follow = []int{0, 15, -1, 1, 5}
		rules = []int{0}
		nonterminals[17] = &nonTerminal{73, "_gen0", first, follow, rules}
		first = []int{3}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{30}
		nonterminals[18] = &nonTerminal{74, "meta", first, follow, rules}
		first = []int{39}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{29}
		nonterminals[19] = &nonTerminal{75, "parameter_meta", first, follow, rules}
		first = []int{-1, 1, 0}
		follow = []int{42}
		rules = []int{72}
		nonterminals[20] = &nonTerminal{76, "_gen18", first, follow, rules}
		first = []int{0, 1, 2, 22, 7, -1, 30, 36}
		follow = []int{11}
		rules = []int{43}
		nonterminals[21] = &nonTerminal{77, "_gen11", first, follow, rules}
		first = []int{33}
		follow = []int{23, 11}
		rules = []int{67}
		nonterminals[22] = &nonTerminal{78, "wf_output_wildcard", first, follow, rules}
		first = []int{19}
		follow = []int{0, -1, 50, 1, 5, 15}
		rules = []int{9}
		nonterminals[23] = &nonTerminal{79, "import_namespace", first, follow, rules}
		first = []int{2}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{70}
		nonterminals[24] = &nonTerminal{80, "scatter", first, follow, rules}
		first = []int{6}
		follow = []int{11, 26, 47}
		rules = []int{60}
		nonterminals[25] = &nonTerminal{81, "mapping", first, follow, rules}
		first = []int{-1, 19}
		follow = []int{0, -1, 50, 1, 5, 15}
		rules = []int{6, 7}
		nonterminals[26] = &nonTerminal{82, "_gen2", first, follow, rules}
		first = []int{6}
		follow = []int{26, 11}
		rules = []int{71}
		nonterminals[27] = &nonTerminal{83, "object_kv", first, follow, rules}
		first = []int{-1, 19}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 14, 36}
		rules = []int{51, 52}
		nonterminals[28] = &nonTerminal{84, "_gen12", first, follow, rules}
		first = []int{39, 30, 51, 27, 3}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{13, 14, 15, 16, 17}
		nonterminals[29] = &nonTerminal{85, "sections", first, follow, rules}
		first = []int{-1, 23}
		follow = []int{11}
		rules = []int{62}
		nonterminals[30] = &nonTerminal{86, "_gen16", first, follow, rules}
		first = []int{28}
		follow = []int{0, 1, 2, 39, 3, 5, 7, 11, -1, 15, 47, 22, 51, 27, 30, 36}
		rules = []int{39}
		nonterminals[31] = &nonTerminal{87, "setter", first, follow, rules}
		first = []int{30}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{63}
		nonterminals[32] = &nonTerminal{88, "wf_outputs", first, follow, rules}
		first = []int{-1, 1, 0}
		follow = []int{30, 39, 3, 51, 47, 27}
		rules = []int{10}
		nonterminals[33] = &nonTerminal{89, "_gen3", first, follow, rules}
		first = []int{-1, 1, 0}
		follow = []int{11}
		rules = []int{25}
		nonterminals[34] = &nonTerminal{90, "_gen7", first, follow, rules}
		first = []int{0, 1, 5, -1, 15}
		follow = []int{-1}
		rules = []int{1}
		nonterminals[35] = &nonTerminal{91, "_gen1", first, follow, rules}
		first = []int{50}
		follow = []int{0, -1, 50, 1, 5, 15}
		rules = []int{8}
		nonterminals[36] = &nonTerminal{92, "import", first, follow, rules}
		first = []int{51}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{28}
		nonterminals[37] = &nonTerminal{93, "runtime", first, follow, rules}
		first = []int{27}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{19}
		nonterminals[38] = &nonTerminal{94, "command", first, follow, rules}
		first = []int{35}
		follow = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 35, 14, 54, 55}
		rules = []int{24}
		nonterminals[39] = &nonTerminal{95, "cmd_param_kv", first, follow, rules}
		first = []int{23}
		follow = []int{23, 11}
		rules = []int{66}
		nonterminals[40] = &nonTerminal{96, "wf_output", first, follow, rules}
		first = []int{-1, 47}
		follow = []int{11}
		rules = []int{56}
		nonterminals[41] = &nonTerminal{97, "_gen14", first, follow, rules}
		first = []int{-1, 33}
		follow = []int{23, 11}
		rules = []int{64, 65}
		nonterminals[42] = &nonTerminal{98, "_gen17", first, follow, rules}
		first = []int{-1, 41, 9}
		follow = []int{48}
		rules = []int{18}
		nonterminals[43] = &nonTerminal{99, "_gen5", first, follow, rules}
		first = []int{22}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{55}
		nonterminals[44] = &nonTerminal{100, "call", first, follow, rules}
		first = []int{47}
		follow = []int{47, 11}
		rules = []int{59}
		nonterminals[45] = &nonTerminal{101, "call_input", first, follow, rules}
		first = []int{-1, 14}
		follow = []int{0, 1, 2, 22, 7, 11, 30, 36}
		rules = []int{53, 54}
		nonterminals[46] = &nonTerminal{102, "_gen13", first, follow, rules}
		first = []int{-1, 30, 39, 3, 51, 27}
		follow = []int{11}
		rules = []int{11}
		nonterminals[47] = &nonTerminal{103, "_gen4", first, follow, rules}
		first = []int{6}
		follow = []int{6, 11}
		rules = []int{33}
		nonterminals[48] = &nonTerminal{104, "kv", first, follow, rules}
		first = []int{25, 55}
		follow = []int{6}
		rules = []int{40, 41}
		nonterminals[49] = &nonTerminal{105, "postfix_quantifier", first, follow, rules}
		first = []int{38, 14, 6, 8, 10, 32, 34, 44, 13, 45, 17, 54, 55}
		follow = []int{26, 11}
		rules = []int{42}
		nonterminals[50] = &nonTerminal{106, "map_kv", first, follow, rules}
		first = []int{14}
		follow = []int{39, 3, 51, 27, 11, 30}
		rules = []int{32}
		nonterminals[51] = &nonTerminal{107, "map", first, follow, rules}
		first = []int{-1, 6}
		follow = []int{11}
		rules = []int{95}
		nonterminals[52] = &nonTerminal{108, "_gen20", first, follow, rules}
		first = []int{0, 1}
		follow = []int{0, 1, 2, 39, 3, 5, 7, 11, -1, 15, 47, 22, 51, 27, 30, 36}
		rules = []int{38}
		nonterminals[53] = &nonTerminal{109, "declaration", first, follow, rules}
		first = []int{0, -1, 1, 50, 5, 15}
		follow = []int{-1}
		rules = []int{2}
		nonterminals[54] = &nonTerminal{110, "document", first, follow, rules}
		first = []int{-1, 28}
		follow = []int{0, 1, 2, 39, 3, 5, 7, 11, -1, 15, 47, 22, 51, 27, 30, 36}
		rules = []int{36, 37}
		nonterminals[55] = &nonTerminal{111, "_gen10", first, follow, rules}
		first = []int{5, 15, 1, 0}
		follow = []int{0, 1, 5, -1, 15}
		rules = []int{3, 4, 5}
		nonterminals[56] = &nonTerminal{112, "workflow_or_task_or_decl", first, follow, rules}
		first = []int{38, 6, 8, 10, 44, 13, 45, 17, 14, 32, -1, 34, 54, 55}
		follow = []int{11}
		rules = []int{98}
		nonterminals[57] = &nonTerminal{113, "_gen21", first, follow, rules}
		first = []int{-1, 35}
		follow = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules = []int{22}
		nonterminals[58] = &nonTerminal{114, "_gen6", first, follow, rules}
	}
	return nonterminals
}
func initRules() []*rule {
	initMutex.Lock()
	defer initMutex.Unlock()
	if rules == nil {
		rules = make([]*rule, 106)
		var firstSet []int
		firstSet = []int{-1, 50}
		rules[0] = &rule{0, "$_gen0 = list($import)", firstSet}
		firstSet = []int{5, 15, 1, 0, -1}
		rules[1] = &rule{1, "$_gen1 = list($workflow_or_task_or_decl)", firstSet}
		firstSet = []int{0, 1, 50, 5, -1, 15}
		rules[2] = &rule{2, "$document = $_gen0 $_gen1 -> Namespace( imports=$0, body=$1 )", firstSet}
		firstSet = []int{15}
		rules[3] = &rule{3, "$workflow_or_task_or_decl = $workflow", firstSet}
		firstSet = []int{5}
		rules[4] = &rule{4, "$workflow_or_task_or_decl = $task", firstSet}
		firstSet = []int{0, 1}
		rules[5] = &rule{5, "$workflow_or_task_or_decl = $declaration", firstSet}
		firstSet = []int{19}
		rules[6] = &rule{6, "$_gen2 = $import_namespace", firstSet}
		firstSet = []int{-1}
		rules[7] = &rule{7, "$_gen2 = :_empty", firstSet}
		firstSet = []int{50}
		rules[8] = &rule{8, "$import = :import :string $_gen2 -> Import( uri=$1, namespace=$2 )", firstSet}
		firstSet = []int{19}
		rules[9] = &rule{9, "$import_namespace = :as :identifier -> $1", firstSet}
		firstSet = []int{0, 1, -1}
		rules[10] = &rule{10, "$_gen3 = list($declaration)", firstSet}
		firstSet = []int{39, 3, 51, 27, -1, 30}
		rules[11] = &rule{11, "$_gen4 = list($sections)", firstSet}
		firstSet = []int{5}
		rules[12] = &rule{12, "$task = :task :identifier :lbrace $_gen3 $_gen4 :rbrace -> Task( name=$1, declarations=$3, sections=$4 )", firstSet}
		firstSet = []int{27}
		rules[13] = &rule{13, "$sections = $command", firstSet}
		firstSet = []int{30}
		rules[14] = &rule{14, "$sections = $outputs", firstSet}
		firstSet = []int{51}
		rules[15] = &rule{15, "$sections = $runtime", firstSet}
		firstSet = []int{39}
		rules[16] = &rule{16, "$sections = $parameter_meta", firstSet}
		firstSet = []int{3}
		rules[17] = &rule{17, "$sections = $meta", firstSet}
		firstSet = []int{-1, 41, 9}
		rules[18] = &rule{18, "$_gen5 = list($command_part)", firstSet}
		firstSet = []int{27}
		rules[19] = &rule{19, "$command = :command :command_start $_gen5 :command_end -> RawCommand( parts=$2 )", firstSet}
		firstSet = []int{9}
		rules[20] = &rule{20, "$command_part = :cmd_part", firstSet}
		firstSet = []int{41}
		rules[21] = &rule{21, "$command_part = $cmd_param", firstSet}
		firstSet = []int{-1, 35}
		rules[22] = &rule{22, "$_gen6 = list($cmd_param_kv)", firstSet}
		firstSet = []int{41}
		rules[23] = &rule{23, "$cmd_param = :cmd_param_start $_gen6 $e :cmd_param_end -> CommandParameter( attributes=$1, expr=$2 )", firstSet}
		firstSet = []int{35}
		rules[24] = &rule{24, "$cmd_param_kv = :cmd_attr_hint :identifier :equal $e -> CommandParameterAttr( key=$1, value=$3 )", firstSet}
		firstSet = []int{0, 1, -1}
		rules[25] = &rule{25, "$_gen7 = list($output_kv)", firstSet}
		firstSet = []int{30}
		rules[26] = &rule{26, "$outputs = :output :lbrace $_gen7 :rbrace -> Outputs( attributes=$2 )", firstSet}
		firstSet = []int{0, 1}
		rules[27] = &rule{27, "$output_kv = $type_e :identifier :equal $e -> Output( type=$0, name=$1, expression=$3 )", firstSet}
		firstSet = []int{51}
		rules[28] = &rule{28, "$runtime = :runtime $map -> Runtime( map=$1 )", firstSet}
		firstSet = []int{39}
		rules[29] = &rule{29, "$parameter_meta = :parameter_meta $map -> ParameterMeta( map=$1 )", firstSet}
		firstSet = []int{3}
		rules[30] = &rule{30, "$meta = :meta $map -> Meta( map=$1 )", firstSet}
		firstSet = []int{-1, 6}
		rules[31] = &rule{31, "$_gen8 = list($kv)", firstSet}
		firstSet = []int{14}
		rules[32] = &rule{32, "$map = :lbrace $_gen8 :rbrace -> $1", firstSet}
		firstSet = []int{6}
		rules[33] = &rule{33, "$kv = :identifier :colon $e -> RuntimeAttribute( key=$0, value=$2 )", firstSet}
		firstSet = []int{25, 55}
		rules[34] = &rule{34, "$_gen9 = $postfix_quantifier", firstSet}
		firstSet = []int{-1}
		rules[35] = &rule{35, "$_gen9 = :_empty", firstSet}
		firstSet = []int{28}
		rules[36] = &rule{36, "$_gen10 = $setter", firstSet}
		firstSet = []int{-1}
		rules[37] = &rule{37, "$_gen10 = :_empty", firstSet}
		firstSet = []int{0, 1}
		rules[38] = &rule{38, "$declaration = $type_e $_gen9 :identifier $_gen10 -> Declaration( type=$0, postfix=$1, name=$2, expression=$3 )", firstSet}
		firstSet = []int{28}
		rules[39] = &rule{39, "$setter = :equal $e -> $1", firstSet}
		firstSet = []int{25}
		rules[40] = &rule{40, "$postfix_quantifier = :qmark", firstSet}
		firstSet = []int{55}
		rules[41] = &rule{41, "$postfix_quantifier = :plus", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[42] = &rule{42, "$map_kv = $e :colon $e -> MapLiteralKv( key=$0, value=$2 )", firstSet}
		firstSet = []int{0, -1, 30, 1, 2, 22, 7, 36}
		rules[43] = &rule{43, "$_gen11 = list($wf_body_element)", firstSet}
		firstSet = []int{15}
		rules[44] = &rule{44, "$workflow = :workflow :identifier :lbrace $_gen11 :rbrace -> Workflow( name=$1, body=$3 )", firstSet}
		firstSet = []int{22}
		rules[45] = &rule{45, "$wf_body_element = $call", firstSet}
		firstSet = []int{0, 1}
		rules[46] = &rule{46, "$wf_body_element = $declaration", firstSet}
		firstSet = []int{36}
		rules[47] = &rule{47, "$wf_body_element = $while_loop", firstSet}
		firstSet = []int{7}
		rules[48] = &rule{48, "$wf_body_element = $if_stmt", firstSet}
		firstSet = []int{2}
		rules[49] = &rule{49, "$wf_body_element = $scatter", firstSet}
		firstSet = []int{30}
		rules[50] = &rule{50, "$wf_body_element = $wf_outputs", firstSet}
		firstSet = []int{19}
		rules[51] = &rule{51, "$_gen12 = $alias", firstSet}
		firstSet = []int{-1}
		rules[52] = &rule{52, "$_gen12 = :_empty", firstSet}
		firstSet = []int{14}
		rules[53] = &rule{53, "$_gen13 = $call_body", firstSet}
		firstSet = []int{-1}
		rules[54] = &rule{54, "$_gen13 = :_empty", firstSet}
		firstSet = []int{22}
		rules[55] = &rule{55, "$call = :call :fqn $_gen12 $_gen13 -> Call( task=$1, alias=$2, body=$3 )", firstSet}
		firstSet = []int{-1, 47}
		rules[56] = &rule{56, "$_gen14 = list($call_input)", firstSet}
		firstSet = []int{14}
		rules[57] = &rule{57, "$call_body = :lbrace $_gen3 $_gen14 :rbrace -> CallBody( declarations=$1, io=$2 )", firstSet}
		firstSet = []int{-1, 6}
		rules[58] = &rule{58, "$_gen15 = list($mapping, :comma)", firstSet}
		firstSet = []int{47}
		rules[59] = &rule{59, "$call_input = :input :colon $_gen15 -> Inputs( map=$2 )", firstSet}
		firstSet = []int{6}
		rules[60] = &rule{60, "$mapping = :identifier :equal $e -> IOMapping( key=$0, value=$2 )", firstSet}
		firstSet = []int{19}
		rules[61] = &rule{61, "$alias = :as :identifier -> $1", firstSet}
		firstSet = []int{-1, 23}
		rules[62] = &rule{62, "$_gen16 = list($wf_output)", firstSet}
		firstSet = []int{30}
		rules[63] = &rule{63, "$wf_outputs = :output :lbrace $_gen16 :rbrace -> WorkflowOutputs( outputs=$2 )", firstSet}
		firstSet = []int{33}
		rules[64] = &rule{64, "$_gen17 = $wf_output_wildcard", firstSet}
		firstSet = []int{-1}
		rules[65] = &rule{65, "$_gen17 = :_empty", firstSet}
		firstSet = []int{23}
		rules[66] = &rule{66, "$wf_output = :fqn $_gen17 -> WorkflowOutput( fqn=$0, wildcard=$1 )", firstSet}
		firstSet = []int{33}
		rules[67] = &rule{67, "$wf_output_wildcard = :dot :asterisk -> $1", firstSet}
		firstSet = []int{36}
		rules[68] = &rule{68, "$while_loop = :while :lparen $e :rparen :lbrace $_gen11 :rbrace -> WhileLoop( expression=$2, body=$5 )", firstSet}
		firstSet = []int{7}
		rules[69] = &rule{69, "$if_stmt = :if :lparen $e :rparen :lbrace $_gen11 :rbrace -> If( expression=$2, body=$5 )", firstSet}
		firstSet = []int{2}
		rules[70] = &rule{70, "$scatter = :scatter :lparen :identifier :in $e :rparen :lbrace $_gen11 :rbrace -> Scatter( item=$2, collection=$4, body=$7 )", firstSet}
		firstSet = []int{6}
		rules[71] = &rule{71, "$object_kv = :identifier :colon $e -> ObjectKV( key=$0, value=$2 )", firstSet}
		firstSet = []int{0, 1, -1}
		rules[72] = &rule{72, "$_gen18 = list($type_e, :comma)", firstSet}
		firstSet = []int{0}
		rules[73] = &rule{73, "$type_e = :type <=> :lsquare $_gen18 :rsquare -> Type( name=$0, subtype=$2 )", firstSet}
		firstSet = []int{0}
		rules[74] = &rule{74, "$type_e = :type", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[75] = &rule{75, "$e = $e :double_pipe $e -> LogicalOr( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[76] = &rule{76, "$e = $e :double_ampersand $e -> LogicalAnd( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[77] = &rule{77, "$e = $e :double_equal $e -> Equals( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[78] = &rule{78, "$e = $e :not_equal $e -> NotEquals( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[79] = &rule{79, "$e = $e :lt $e -> LessThan( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[80] = &rule{80, "$e = $e :lteq $e -> LessThanOrEqual( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[81] = &rule{81, "$e = $e :gt $e -> GreaterThan( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[82] = &rule{82, "$e = $e :gteq $e -> GreaterThanOrEqual( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[83] = &rule{83, "$e = $e :plus $e -> Add( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[84] = &rule{84, "$e = $e :dash $e -> Subtract( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[85] = &rule{85, "$e = $e :asterisk $e -> Multiply( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[86] = &rule{86, "$e = $e :slash $e -> Divide( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, 44, 13, 45, 14, 54, 55}
		rules[87] = &rule{87, "$e = $e :percent $e -> Remainder( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{17}
		rules[88] = &rule{88, "$e = :not $e -> LogicalNot( expression=$1 )", firstSet}
		firstSet = []int{55}
		rules[89] = &rule{89, "$e = :plus $e -> UnaryPlus( expression=$1 )", firstSet}
		firstSet = []int{38}
		rules[90] = &rule{90, "$e = :dash $e -> UnaryNegation( expression=$1 )", firstSet}
		firstSet = []int{38, 17, 6, 8, 10, 32, 34, -1, 44, 13, 45, 14, 54, 55}
		rules[91] = &rule{91, "$_gen19 = list($e, :comma)", firstSet}
		firstSet = []int{6}
		rules[92] = &rule{92, "$e = :identifier <=> :lparen $_gen19 :rparen -> FunctionCall( name=$0, params=$2 )", firstSet}
		firstSet = []int{6}
		rules[93] = &rule{93, "$e = :identifier <=> :lsquare $e :rsquare -> ArrayOrMapLookup( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{6}
		rules[94] = &rule{94, "$e = :identifier <=> :dot :identifier -> MemberAccess( lhs=$0, rhs=$2 )", firstSet}
		firstSet = []int{-1, 6}
		rules[95] = &rule{95, "$_gen20 = list($object_kv, :comma)", firstSet}
		firstSet = []int{45}
		rules[96] = &rule{96, "$e = :object :lbrace $_gen20 :rbrace -> ObjectLiteral( map=$2 )", firstSet}
		firstSet = []int{8}
		rules[97] = &rule{97, "$e = :lsquare $_gen19 :rsquare -> ArrayLiteral( values=$1 )", firstSet}
		firstSet = []int{38, 14, 6, 8, 10, 32, 34, -1, 44, 13, 45, 17, 54, 55}
		rules[98] = &rule{98, "$_gen21 = list($map_kv, :comma)", firstSet}
		firstSet = []int{14}
		rules[99] = &rule{99, "$e = :lbrace $_gen21 :rbrace -> MapLiteral( map=$1 )", firstSet}
		firstSet = []int{54}
		rules[100] = &rule{100, "$e = :lparen $e :rparen -> $1", firstSet}
		firstSet = []int{13}
		rules[101] = &rule{101, "$e = :string", firstSet}
		firstSet = []int{6}
		rules[102] = &rule{102, "$e = :identifier", firstSet}
		firstSet = []int{32}
		rules[103] = &rule{103, "$e = :boolean", firstSet}
		firstSet = []int{34}
		rules[104] = &rule{104, "$e = :integer", firstSet}
		firstSet = []int{10}
		rules[105] = &rule{105, "$e = :float", firstSet}
	}
	return rules
}

type ParserContext struct {
	parser             *WdlParser
	tokens             *TokenStream
	errors             SyntaxErrorHandler
	nonterminal_string string
	rule_string        string
}

func (ctx *ParserContext) expect(terminal_id int) (*Token, error) {
	current := ctx.tokens.current()
	if current == nil {
		err := ctx.errors.no_more_tokens(ctx.nonterminal_string, terminals[terminal_id], ctx.tokens.last())
		return nil, err
	}
	if current.terminal.id != terminal_id {
		expected := make([]*terminal, 1)
		expected[0] = ctx.parser.terminals[terminal_id] // TODO: don't use initTerminals here
		err := ctx.errors.unexpected_symbol(ctx.nonterminal_string, current, expected, ctx.rule_string)
		return nil, err
	}
	next := ctx.tokens.advance()
	if next != nil && !ctx.IsValidTerminalId(next.terminal.id) {
		err := ctx.errors.invalid_terminal(ctx.nonterminal_string, next)
		return nil, err
	}
	return current, nil
}

type WdlParser struct {
	table        [][]int
	terminals    []*terminal
	nonterminals []*nonTerminal
	rules        []*rule
}

func NewWdlParser() *WdlParser {
	return &WdlParser{
		initTable(),
		initTerminals(),
		initNonTerminals(),
		initRules()}
}
func (parser *WdlParser) newParseTree(nonterminalId int) *parseTree {
	var nt *nonTerminal
	for _, n := range parser.nonterminals {
		if n.id == nonterminalId {
			nt = n
		}
	}
	return &parseTree{
		nonterminal:       nt,
		children:          nil,
		astTransform:      nil,
		isExpr:            false,
		isNud:             false,
		isPrefix:          false,
		isInfix:           false,
		nudMorphemeCount:  0,
		isExprNud:         false,
		list_separator_id: -1,
		list:              false}
}
func (parser *WdlParser) ParseTokens(stream *TokenStream, handler SyntaxErrorHandler) (*parseTree, error) {
	ctx := ParserContext{parser, stream, handler, "", ""}
	tree, err := parser.Parse_document(&ctx)
	if err != nil {
		return nil, err
	}
	if stream.current() != nil {
		ctx.errors.excess_tokens()
		return nil, ctx.errors
	}
	return tree, nil
}
func (parser *WdlParser) TerminalFromId(id int) *terminal {
	return parser.terminals[id]
}
func (parser *WdlParser) NonTerminalFromId(id int) *nonTerminal {
	return parser.nonterminals[id-56]
}
func (parser *WdlParser) TerminalFromStringId(id string) *terminal {
	for _, t := range parser.terminals {
		if t.idStr == id {
			return t
		}
	}
	return nil
}
func (parser *WdlParser) Rule(id int) *rule {
	for _, r := range parser.rules {
		if r.id == id {
			return r
		}
	}
	return nil
}
func (ctx *ParserContext) IsValidTerminalId(id int) bool {
	return 0 <= id && id <= 55
}
func (parser *WdlParser) infixBindingPower_e(terminal_id int) int {
	switch terminal_id {
	case 12:
		return 2000 // $e = $e :double_pipe $e -> LogicalOr( lhs=$0, rhs=$2 )
	case 4:
		return 3000 // $e = $e :double_ampersand $e -> LogicalAnd( lhs=$0, rhs=$2 )
	case 49:
		return 4000 // $e = $e :double_equal $e -> Equals( lhs=$0, rhs=$2 )
	case 31:
		return 4000 // $e = $e :not_equal $e -> NotEquals( lhs=$0, rhs=$2 )
	case 37:
		return 5000 // $e = $e :lt $e -> LessThan( lhs=$0, rhs=$2 )
	case 43:
		return 5000 // $e = $e :lteq $e -> LessThanOrEqual( lhs=$0, rhs=$2 )
	case 18:
		return 5000 // $e = $e :gt $e -> GreaterThan( lhs=$0, rhs=$2 )
	case 20:
		return 5000 // $e = $e :gteq $e -> GreaterThanOrEqual( lhs=$0, rhs=$2 )
	case 55:
		return 6000 // $e = $e :plus $e -> Add( lhs=$0, rhs=$2 )
	case 38:
		return 6000 // $e = $e :dash $e -> Subtract( lhs=$0, rhs=$2 )
	case 52:
		return 7000 // $e = $e :asterisk $e -> Multiply( lhs=$0, rhs=$2 )
	case 16:
		return 7000 // $e = $e :slash $e -> Divide( lhs=$0, rhs=$2 )
	case 29:
		return 7000 // $e = $e :percent $e -> Remainder( lhs=$0, rhs=$2 )
	case 54:
		return 9000 // $e = :identifier <=> :lparen list($e, :comma) :rparen -> FunctionCall( name=$0, params=$2 )
	case 8:
		return 10000 // $e = :identifier <=> :lsquare $e :rsquare -> ArrayOrMapLookup( lhs=$0, rhs=$2 )
	case 33:
		return 11000 // $e = :identifier <=> :dot :identifier -> MemberAccess( lhs=$0, rhs=$2 )
	}
	return 0
}
func (parser *WdlParser) prefixBindingPower_e(terminal_id int) int {
	switch terminal_id {
	case 17:
		return 8000 // $e = :not $e -> LogicalNot( expression=$1 )
	case 55:
		return 8000 // $e = :plus $e -> UnaryPlus( expression=$1 )
	case 38:
		return 8000 // $e = :dash $e -> UnaryNegation( expression=$1 )
	}
	return 0
}
func (parser *WdlParser) Parse_e(ctx *ParserContext) (*parseTree, error) {
	return parser._parse_e(ctx, 0)
}
func (parser *WdlParser) _parse_e(ctx *ParserContext, rbp int) (*parseTree, error) {
	left, err := parser.nud_e(ctx)
	if err != nil {
		return nil, err
	}
	if left != nil {
		left.isExpr = true
		left.isNud = true
	}
	for ctx.tokens.current() != nil && rbp < parser.infixBindingPower_e(ctx.tokens.current().terminal.id) {
		left, err = parser.led_e(left, ctx)
		if err != nil {
			return nil, err
		}
	}
	if left != nil {
		left.isExpr = true
	}
	return left, nil
}
func (parser *WdlParser) nud_e(ctx *ParserContext) (*parseTree, error) {
	tree := parser.newParseTree(70)
	current := ctx.tokens.current()
	ctx.nonterminal_string = "e"
	var token *Token
	var err error
	var subtree *parseTree
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return tree, nil
	}
	if parser.Rule(88).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(88).str
		astParameters := make(map[string]int)
		astParameters["expression"] = 1
		tree.astTransform = &AstTransformNodeCreator{"LogicalNot", astParameters, []string{"expression"}}
		tree.nudMorphemeCount = 2
		token, err = ctx.expect(17)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser._parse_e(ctx, parser.prefixBindingPower_e(17))
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		tree.isPrefix = true
		return tree, nil
	}
	if parser.Rule(89).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(89).str
		astParameters := make(map[string]int)
		astParameters["expression"] = 1
		tree.astTransform = &AstTransformNodeCreator{"UnaryPlus", astParameters, []string{"expression"}}
		tree.nudMorphemeCount = 2
		token, err = ctx.expect(55)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser._parse_e(ctx, parser.prefixBindingPower_e(55))
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		tree.isPrefix = true
		return tree, nil
	}
	if parser.Rule(90).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(90).str
		astParameters := make(map[string]int)
		astParameters["expression"] = 1
		tree.astTransform = &AstTransformNodeCreator{"UnaryNegation", astParameters, []string{"expression"}}
		tree.nudMorphemeCount = 2
		token, err = ctx.expect(38)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser._parse_e(ctx, parser.prefixBindingPower_e(38))
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		tree.isPrefix = true
		return tree, nil
	}
	if parser.Rule(92).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(92).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(6)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(93).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(93).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(6)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(94).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(94).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(6)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(96).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(96).str
		astParameters := make(map[string]int)
		astParameters["map"] = 2
		tree.astTransform = &AstTransformNodeCreator{"ObjectLiteral", astParameters, []string{"map"}}
		tree.nudMorphemeCount = 4
		token, err = ctx.expect(45)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen20(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(97).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(97).str
		astParameters := make(map[string]int)
		astParameters["values"] = 1
		tree.astTransform = &AstTransformNodeCreator{"ArrayLiteral", astParameters, []string{"values"}}
		tree.nudMorphemeCount = 3
		token, err = ctx.expect(8)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen19(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(42)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(99).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(99).str
		astParameters := make(map[string]int)
		astParameters["map"] = 1
		tree.astTransform = &AstTransformNodeCreator{"MapLiteral", astParameters, []string{"map"}}
		tree.nudMorphemeCount = 3
		token, err = ctx.expect(14)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen21(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(100).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(100).str
		tree.astTransform = &AstTransformSubstitution{1}
		tree.nudMorphemeCount = 3
		token, err = ctx.expect(54)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(53)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(101).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(101).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(13)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(102).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(102).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(6)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(103).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(103).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(32)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(104).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(104).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(34)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(105).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(105).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(10)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) led_e(left *parseTree, ctx *ParserContext) (*parseTree, error) {
	tree := parser.newParseTree(70)
	current := ctx.tokens.current()
	ctx.nonterminal_string = "e"
	var token *Token
	var err error
	var subtree *parseTree
	_ = token
	_ = err
	_ = subtree
	if current.terminal.id == 12 {
		// $e = $e :double_pipe $e -> LogicalOr( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(75).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"LogicalOr", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(12) // :double_pipe
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(12)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 4 {
		// $e = $e :double_ampersand $e -> LogicalAnd( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(76).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"LogicalAnd", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(4) // :double_ampersand
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(4)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 49 {
		// $e = $e :double_equal $e -> Equals( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(77).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Equals", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(49) // :double_equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(49)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 31 {
		// $e = $e :not_equal $e -> NotEquals( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(78).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"NotEquals", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(31) // :not_equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(31)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 37 {
		// $e = $e :lt $e -> LessThan( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(79).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"LessThan", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(37) // :lt
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(37)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 43 {
		// $e = $e :lteq $e -> LessThanOrEqual( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(80).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"LessThanOrEqual", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(43) // :lteq
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(43)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 18 {
		// $e = $e :gt $e -> GreaterThan( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(81).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"GreaterThan", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(18) // :gt
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(18)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 20 {
		// $e = $e :gteq $e -> GreaterThanOrEqual( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(82).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"GreaterThanOrEqual", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(20) // :gteq
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(20)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 55 {
		// $e = $e :plus $e -> Add( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(83).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Add", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(55) // :plus
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(55)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 38 {
		// $e = $e :dash $e -> Subtract( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(84).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Subtract", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(38) // :dash
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(38)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 52 {
		// $e = $e :asterisk $e -> Multiply( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(85).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Multiply", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(52) // :asterisk
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(52)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 16 {
		// $e = $e :slash $e -> Divide( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(86).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Divide", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(16) // :slash
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(16)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 29 {
		// $e = $e :percent $e -> Remainder( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(87).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Remainder", astParameters, []string{"lhs", "rhs"}}
		tree.isExprNud = true
		tree.Add(left)
		token, err = ctx.expect(29) // :percent
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		tree.isInfix = true
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(29)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
	}
	if current.terminal.id == 54 {
		// $e = :identifier <=> :lparen $_gen19 :rparen -> FunctionCall( name=$0, params=$2 )
		ctx.rule_string = parser.Rule(92).str
		var astParameters = make(map[string]int)
		astParameters["name"] = 0
		astParameters["params"] = 2
		tree.astTransform = &AstTransformNodeCreator{"FunctionCall", astParameters, []string{"name", "params"}}
		tree.Add(left)
		token, err = ctx.expect(54) // :lparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen19(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(53) // :rparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
	}
	if current.terminal.id == 8 {
		// $e = :identifier <=> :lsquare $e :rsquare -> ArrayOrMapLookup( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(93).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"ArrayOrMapLookup", astParameters, []string{"lhs", "rhs"}}
		tree.Add(left)
		token, err = ctx.expect(8) // :lsquare
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		modifier := 0
		subtree, err = parser._parse_e(ctx, parser.infixBindingPower_e(8)-modifier)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(42) // :rsquare
		if err != nil {
			return nil, err
		}
		tree.Add(token)
	}
	if current.terminal.id == 33 {
		// $e = :identifier <=> :dot :identifier -> MemberAccess( lhs=$0, rhs=$2 )
		ctx.rule_string = parser.Rule(94).str
		var astParameters = make(map[string]int)
		astParameters["lhs"] = 0
		astParameters["rhs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"MemberAccess", astParameters, []string{"lhs", "rhs"}}
		tree.Add(left)
		token, err = ctx.expect(33) // :dot
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
	}
	return tree, nil
}
func (parser *WdlParser) infixBindingPower_type_e(terminal_id int) int {
	switch terminal_id {
	case 8:
		return 1000 // $type_e = :type <=> :lsquare list($type_e, :comma) :rsquare -> Type( name=$0, subtype=$2 )
	}
	return 0
}
func (parser *WdlParser) prefixBindingPower_type_e(terminal_id int) int {
	switch terminal_id {
	}
	return 0
}
func (parser *WdlParser) Parse_type_e(ctx *ParserContext) (*parseTree, error) {
	return parser._parse_type_e(ctx, 0)
}
func (parser *WdlParser) _parse_type_e(ctx *ParserContext, rbp int) (*parseTree, error) {
	left, err := parser.nud_type_e(ctx)
	if err != nil {
		return nil, err
	}
	if left != nil {
		left.isExpr = true
		left.isNud = true
	}
	for ctx.tokens.current() != nil && rbp < parser.infixBindingPower_type_e(ctx.tokens.current().terminal.id) {
		left, err = parser.led_type_e(left, ctx)
		if err != nil {
			return nil, err
		}
	}
	if left != nil {
		left.isExpr = true
	}
	return left, nil
}
func (parser *WdlParser) nud_type_e(ctx *ParserContext) (*parseTree, error) {
	tree := parser.newParseTree(66)
	current := ctx.tokens.current()
	ctx.nonterminal_string = "type_e"
	var token *Token
	var err error
	var subtree *parseTree
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return tree, nil
	}
	if parser.Rule(73).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(73).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(0)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if parser.Rule(74).CanStartWith(current.terminal.id) {
		ctx.rule_string = parser.Rule(74).str
		tree.astTransform = &AstTransformSubstitution{0}
		tree.nudMorphemeCount = 1
		token, err = ctx.expect(0)
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) led_type_e(left *parseTree, ctx *ParserContext) (*parseTree, error) {
	tree := parser.newParseTree(66)
	current := ctx.tokens.current()
	ctx.nonterminal_string = "type_e"
	var token *Token
	var err error
	var subtree *parseTree
	_ = token
	_ = err
	_ = subtree
	if current.terminal.id == 8 {
		// $type_e = :type <=> :lsquare $_gen18 :rsquare -> Type( name=$0, subtype=$2 )
		ctx.rule_string = parser.Rule(73).str
		var astParameters = make(map[string]int)
		astParameters["name"] = 0
		astParameters["subtype"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Type", astParameters, []string{"name", "subtype"}}
		tree.Add(left)
		token, err = ctx.expect(8) // :lsquare
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen18(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(42) // :rsquare
		if err != nil {
			return nil, err
		}
		tree.Add(token)
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen0(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(73)
	tree.list = true
	ctx.nonterminal_string = "_gen0"
	list_nonterminal := parser.NonTerminalFromId(73)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(73).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_import(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen0" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen1(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(91)
	tree.list = true
	ctx.nonterminal_string = "_gen1"
	list_nonterminal := parser.NonTerminalFromId(91)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(91).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_workflow_or_task_or_decl(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen1" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen11(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(77)
	tree.list = true
	ctx.nonterminal_string = "_gen11"
	list_nonterminal := parser.NonTerminalFromId(77)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(77).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_wf_body_element(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen11" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen14(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(97)
	tree.list = true
	ctx.nonterminal_string = "_gen14"
	list_nonterminal := parser.NonTerminalFromId(97)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(97).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_call_input(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen14" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen15(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(72)
	tree.list = true
	tree.list_separator_id = 26
	ctx.nonterminal_string = "_gen15"
	list_nonterminal := parser.NonTerminalFromId(72)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(72).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_mapping(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen15" // Horrible -- because parse_* can reset this
		if ctx.tokens.current() != nil && ctx.tokens.current().terminal.id == 26 {
			token, err := ctx.expect(26)
			if err != nil {
				return nil, err
			}
			tree.Add(token)
		} else {
			break
		}
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen16(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(86)
	tree.list = true
	ctx.nonterminal_string = "_gen16"
	list_nonterminal := parser.NonTerminalFromId(86)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(86).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_wf_output(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen16" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen18(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(76)
	tree.list = true
	tree.list_separator_id = 26
	ctx.nonterminal_string = "_gen18"
	list_nonterminal := parser.NonTerminalFromId(76)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(76).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_type_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen18" // Horrible -- because parse_* can reset this
		if ctx.tokens.current() != nil && ctx.tokens.current().terminal.id == 26 {
			token, err := ctx.expect(26)
			if err != nil {
				return nil, err
			}
			tree.Add(token)
		} else {
			break
		}
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen19(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(67)
	tree.list = true
	tree.list_separator_id = 26
	ctx.nonterminal_string = "_gen19"
	list_nonterminal := parser.NonTerminalFromId(67)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(67).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen19" // Horrible -- because parse_* can reset this
		if ctx.tokens.current() != nil && ctx.tokens.current().terminal.id == 26 {
			token, err := ctx.expect(26)
			if err != nil {
				return nil, err
			}
			tree.Add(token)
		} else {
			break
		}
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen20(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(108)
	tree.list = true
	tree.list_separator_id = 26
	ctx.nonterminal_string = "_gen20"
	list_nonterminal := parser.NonTerminalFromId(108)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(108).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_object_kv(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen20" // Horrible -- because parse_* can reset this
		if ctx.tokens.current() != nil && ctx.tokens.current().terminal.id == 26 {
			token, err := ctx.expect(26)
			if err != nil {
				return nil, err
			}
			tree.Add(token)
		} else {
			break
		}
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen21(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(113)
	tree.list = true
	tree.list_separator_id = 26
	ctx.nonterminal_string = "_gen21"
	list_nonterminal := parser.NonTerminalFromId(113)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(113).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_map_kv(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen21" // Horrible -- because parse_* can reset this
		if ctx.tokens.current() != nil && ctx.tokens.current().terminal.id == 26 {
			token, err := ctx.expect(26)
			if err != nil {
				return nil, err
			}
			tree.Add(token)
		} else {
			break
		}
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen3(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(89)
	tree.list = true
	ctx.nonterminal_string = "_gen3"
	list_nonterminal := parser.NonTerminalFromId(89)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(89).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_declaration(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen3" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen4(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(103)
	tree.list = true
	ctx.nonterminal_string = "_gen4"
	list_nonterminal := parser.NonTerminalFromId(103)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(103).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_sections(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen4" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen5(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(99)
	tree.list = true
	ctx.nonterminal_string = "_gen5"
	list_nonterminal := parser.NonTerminalFromId(99)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(99).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_command_part(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen5" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen6(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(114)
	tree.list = true
	ctx.nonterminal_string = "_gen6"
	list_nonterminal := parser.NonTerminalFromId(114)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(114).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_cmd_param_kv(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen6" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen7(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(90)
	tree.list = true
	ctx.nonterminal_string = "_gen7"
	list_nonterminal := parser.NonTerminalFromId(90)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(90).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_output_kv(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen7" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen8(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	tree := parser.newParseTree(63)
	tree.list = true
	ctx.nonterminal_string = "_gen8"
	list_nonterminal := parser.NonTerminalFromId(63)
	if current != nil && list_nonterminal.CanBeFollowedBy(current.terminal.id) && list_nonterminal.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	minimum := 0
	for minimum > 0 || (ctx.tokens.current() != nil && parser.NonTerminalFromId(63).CanStartWith(ctx.tokens.current().terminal.id)) {
		subtree, err := parser.Parse_kv(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		ctx.nonterminal_string = "_gen8" // Horrible -- because parse_* can reset this
		if minimum-1 > 0 {
			minimum = minimum - 1
		} else {
			minimum = 0
		}
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen10(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[55][current.terminal.id]
	}
	tree := parser.newParseTree(111)
	ctx.nonterminal_string = "_gen10"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(111)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 36 { // $_gen10 = $setter
		ctx.rule_string = rules[36].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_setter(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen12(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[28][current.terminal.id]
	}
	tree := parser.newParseTree(84)
	ctx.nonterminal_string = "_gen12"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(84)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 51 { // $_gen12 = $alias
		ctx.rule_string = rules[51].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_alias(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen13(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[46][current.terminal.id]
	}
	tree := parser.newParseTree(102)
	ctx.nonterminal_string = "_gen13"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(102)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 53 { // $_gen13 = $call_body
		ctx.rule_string = rules[53].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_call_body(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen17(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[42][current.terminal.id]
	}
	tree := parser.newParseTree(98)
	ctx.nonterminal_string = "_gen17"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(98)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 64 { // $_gen17 = $wf_output_wildcard
		ctx.rule_string = rules[64].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_wf_output_wildcard(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen2(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[26][current.terminal.id]
	}
	tree := parser.newParseTree(82)
	ctx.nonterminal_string = "_gen2"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(82)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 6 { // $_gen2 = $import_namespace
		ctx.rule_string = rules[6].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_import_namespace(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse__gen9(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[15][current.terminal.id]
	}
	tree := parser.newParseTree(71)
	ctx.nonterminal_string = "_gen9"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(71)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 34 { // $_gen9 = $postfix_quantifier
		ctx.rule_string = rules[34].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_postfix_quantifier(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse_alias(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[5][current.terminal.id]
	}
	tree := parser.newParseTree(61)
	ctx.nonterminal_string = "alias"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 61 { // $alias = :as :identifier -> $1
		ctx.rule_string = rules[61].str
		tree.astTransform = &AstTransformSubstitution{1}
		token, err = ctx.expect(19) // :as
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(61)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[61].str)
}
func (parser *WdlParser) Parse_call(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[44][current.terminal.id]
	}
	tree := parser.newParseTree(100)
	ctx.nonterminal_string = "call"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 55 { // $call = :call :fqn $_gen12 $_gen13 -> Call( task=$1, alias=$2, body=$3 )
		ctx.rule_string = rules[55].str
		astParameters := make(map[string]int)
		astParameters["task"] = 1
		astParameters["alias"] = 2
		astParameters["body"] = 3
		tree.astTransform = &AstTransformNodeCreator{"Call", astParameters, []string{"task", "alias", "body"}}
		token, err = ctx.expect(22) // :call
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(23) // :fqn
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen12(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse__gen13(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(100)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[55].str)
}
func (parser *WdlParser) Parse_call_body(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[3][current.terminal.id]
	}
	tree := parser.newParseTree(59)
	ctx.nonterminal_string = "call_body"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 57 { // $call_body = :lbrace $_gen3 $_gen14 :rbrace -> CallBody( declarations=$1, io=$2 )
		ctx.rule_string = rules[57].str
		astParameters := make(map[string]int)
		astParameters["declarations"] = 1
		astParameters["io"] = 2
		tree.astTransform = &AstTransformNodeCreator{"CallBody", astParameters, []string{"declarations", "io"}}
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen3(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse__gen14(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(59)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[57].str)
}
func (parser *WdlParser) Parse_call_input(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[45][current.terminal.id]
	}
	tree := parser.newParseTree(101)
	ctx.nonterminal_string = "call_input"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 59 { // $call_input = :input :colon $_gen15 -> Inputs( map=$2 )
		ctx.rule_string = rules[59].str
		astParameters := make(map[string]int)
		astParameters["map"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Inputs", astParameters, []string{"map"}}
		token, err = ctx.expect(47) // :input
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(40) // :colon
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen15(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(101)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[59].str)
}
func (parser *WdlParser) Parse_cmd_param(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[9][current.terminal.id]
	}
	tree := parser.newParseTree(65)
	ctx.nonterminal_string = "cmd_param"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 23 { // $cmd_param = :cmd_param_start $_gen6 $e :cmd_param_end -> CommandParameter( attributes=$1, expr=$2 )
		ctx.rule_string = rules[23].str
		astParameters := make(map[string]int)
		astParameters["attributes"] = 1
		astParameters["expr"] = 2
		tree.astTransform = &AstTransformNodeCreator{"CommandParameter", astParameters, []string{"attributes", "expr"}}
		token, err = ctx.expect(41) // :cmd_param_start
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen6(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(24) // :cmd_param_end
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(65)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[23].str)
}
func (parser *WdlParser) Parse_cmd_param_kv(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[39][current.terminal.id]
	}
	tree := parser.newParseTree(95)
	ctx.nonterminal_string = "cmd_param_kv"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 24 { // $cmd_param_kv = :cmd_attr_hint :identifier :equal $e -> CommandParameterAttr( key=$1, value=$3 )
		ctx.rule_string = rules[24].str
		astParameters := make(map[string]int)
		astParameters["key"] = 1
		astParameters["value"] = 3
		tree.astTransform = &AstTransformNodeCreator{"CommandParameterAttr", astParameters, []string{"key", "value"}}
		token, err = ctx.expect(35) // :cmd_attr_hint
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(28) // :equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(95)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[24].str)
}
func (parser *WdlParser) Parse_command(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[38][current.terminal.id]
	}
	tree := parser.newParseTree(94)
	ctx.nonterminal_string = "command"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 19 { // $command = :command :command_start $_gen5 :command_end -> RawCommand( parts=$2 )
		ctx.rule_string = rules[19].str
		astParameters := make(map[string]int)
		astParameters["parts"] = 2
		tree.astTransform = &AstTransformNodeCreator{"RawCommand", astParameters, []string{"parts"}}
		token, err = ctx.expect(27) // :command
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(21) // :command_start
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen5(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(48) // :command_end
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(94)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[19].str)
}
func (parser *WdlParser) Parse_command_part(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[8][current.terminal.id]
	}
	tree := parser.newParseTree(64)
	ctx.nonterminal_string = "command_part"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 20 { // $command_part = :cmd_part
		ctx.rule_string = rules[20].str
		tree.astTransform = &AstTransformSubstitution{0}
		token, err = ctx.expect(9) // :cmd_part
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if rule == 21 { // $command_part = $cmd_param
		ctx.rule_string = rules[21].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_cmd_param(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(64)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[21].str)
}
func (parser *WdlParser) Parse_declaration(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[53][current.terminal.id]
	}
	tree := parser.newParseTree(109)
	ctx.nonterminal_string = "declaration"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 38 { // $declaration = $type_e $_gen9 :identifier $_gen10 -> Declaration( type=$0, postfix=$1, name=$2, expression=$3 )
		ctx.rule_string = rules[38].str
		astParameters := make(map[string]int)
		astParameters["type"] = 0
		astParameters["postfix"] = 1
		astParameters["name"] = 2
		astParameters["expression"] = 3
		tree.astTransform = &AstTransformNodeCreator{"Declaration", astParameters, []string{"type", "postfix", "name", "expression"}}
		subtree, err = parser.Parse_type_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse__gen9(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen10(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(109)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[38].str)
}
func (parser *WdlParser) Parse_document(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[54][current.terminal.id]
	}
	tree := parser.newParseTree(110)
	ctx.nonterminal_string = "document"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	nt := parser.NonTerminalFromId(110)
	if current != nil && nt.CanBeFollowedBy(current.terminal.id) && nt.CanStartWith(current.terminal.id) {
		return tree, nil
	}
	if current == nil {
		return tree, nil
	}
	if rule == 2 { // $document = $_gen0 $_gen1 -> Namespace( imports=$0, body=$1 )
		ctx.rule_string = rules[2].str
		astParameters := make(map[string]int)
		astParameters["imports"] = 0
		astParameters["body"] = 1
		tree.astTransform = &AstTransformNodeCreator{"Namespace", astParameters, []string{"imports", "body"}}
		subtree, err = parser.Parse__gen0(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse__gen1(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	return tree, nil
}
func (parser *WdlParser) Parse_if_stmt(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[12][current.terminal.id]
	}
	tree := parser.newParseTree(68)
	ctx.nonterminal_string = "if_stmt"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 69 { // $if_stmt = :if :lparen $e :rparen :lbrace $_gen11 :rbrace -> If( expression=$2, body=$5 )
		ctx.rule_string = rules[69].str
		astParameters := make(map[string]int)
		astParameters["expression"] = 2
		astParameters["body"] = 5
		tree.astTransform = &AstTransformNodeCreator{"If", astParameters, []string{"expression", "body"}}
		token, err = ctx.expect(7) // :if
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(54) // :lparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(53) // :rparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen11(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(68)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[69].str)
}
func (parser *WdlParser) Parse_import(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[36][current.terminal.id]
	}
	tree := parser.newParseTree(92)
	ctx.nonterminal_string = "import"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 8 { // $import = :import :string $_gen2 -> Import( uri=$1, namespace=$2 )
		ctx.rule_string = rules[8].str
		astParameters := make(map[string]int)
		astParameters["uri"] = 1
		astParameters["namespace"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Import", astParameters, []string{"uri", "namespace"}}
		token, err = ctx.expect(50) // :import
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(13) // :string
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen2(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(92)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[8].str)
}
func (parser *WdlParser) Parse_import_namespace(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[23][current.terminal.id]
	}
	tree := parser.newParseTree(79)
	ctx.nonterminal_string = "import_namespace"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 9 { // $import_namespace = :as :identifier -> $1
		ctx.rule_string = rules[9].str
		tree.astTransform = &AstTransformSubstitution{1}
		token, err = ctx.expect(19) // :as
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(79)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[9].str)
}
func (parser *WdlParser) Parse_kv(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[48][current.terminal.id]
	}
	tree := parser.newParseTree(104)
	ctx.nonterminal_string = "kv"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 33 { // $kv = :identifier :colon $e -> RuntimeAttribute( key=$0, value=$2 )
		ctx.rule_string = rules[33].str
		astParameters := make(map[string]int)
		astParameters["key"] = 0
		astParameters["value"] = 2
		tree.astTransform = &AstTransformNodeCreator{"RuntimeAttribute", astParameters, []string{"key", "value"}}
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(40) // :colon
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(104)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[33].str)
}
func (parser *WdlParser) Parse_map(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[51][current.terminal.id]
	}
	tree := parser.newParseTree(107)
	ctx.nonterminal_string = "map"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 32 { // $map = :lbrace $_gen8 :rbrace -> $1
		ctx.rule_string = rules[32].str
		tree.astTransform = &AstTransformSubstitution{1}
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen8(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(107)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[32].str)
}
func (parser *WdlParser) Parse_map_kv(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[50][current.terminal.id]
	}
	tree := parser.newParseTree(106)
	ctx.nonterminal_string = "map_kv"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 42 { // $map_kv = $e :colon $e -> MapLiteralKv( key=$0, value=$2 )
		ctx.rule_string = rules[42].str
		astParameters := make(map[string]int)
		astParameters["key"] = 0
		astParameters["value"] = 2
		tree.astTransform = &AstTransformNodeCreator{"MapLiteralKv", astParameters, []string{"key", "value"}}
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(40) // :colon
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(106)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[42].str)
}
func (parser *WdlParser) Parse_mapping(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[25][current.terminal.id]
	}
	tree := parser.newParseTree(81)
	ctx.nonterminal_string = "mapping"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 60 { // $mapping = :identifier :equal $e -> IOMapping( key=$0, value=$2 )
		ctx.rule_string = rules[60].str
		astParameters := make(map[string]int)
		astParameters["key"] = 0
		astParameters["value"] = 2
		tree.astTransform = &AstTransformNodeCreator{"IOMapping", astParameters, []string{"key", "value"}}
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(28) // :equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(81)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[60].str)
}
func (parser *WdlParser) Parse_meta(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[18][current.terminal.id]
	}
	tree := parser.newParseTree(74)
	ctx.nonterminal_string = "meta"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 30 { // $meta = :meta $map -> Meta( map=$1 )
		ctx.rule_string = rules[30].str
		astParameters := make(map[string]int)
		astParameters["map"] = 1
		tree.astTransform = &AstTransformNodeCreator{"Meta", astParameters, []string{"map"}}
		token, err = ctx.expect(3) // :meta
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_map(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(74)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[30].str)
}
func (parser *WdlParser) Parse_object_kv(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[27][current.terminal.id]
	}
	tree := parser.newParseTree(83)
	ctx.nonterminal_string = "object_kv"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 71 { // $object_kv = :identifier :colon $e -> ObjectKV( key=$0, value=$2 )
		ctx.rule_string = rules[71].str
		astParameters := make(map[string]int)
		astParameters["key"] = 0
		astParameters["value"] = 2
		tree.astTransform = &AstTransformNodeCreator{"ObjectKV", astParameters, []string{"key", "value"}}
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(40) // :colon
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(83)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[71].str)
}
func (parser *WdlParser) Parse_output_kv(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[2][current.terminal.id]
	}
	tree := parser.newParseTree(58)
	ctx.nonterminal_string = "output_kv"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 27 { // $output_kv = $type_e :identifier :equal $e -> Output( type=$0, name=$1, expression=$3 )
		ctx.rule_string = rules[27].str
		astParameters := make(map[string]int)
		astParameters["type"] = 0
		astParameters["name"] = 1
		astParameters["expression"] = 3
		tree.astTransform = &AstTransformNodeCreator{"Output", astParameters, []string{"type", "name", "expression"}}
		subtree, err = parser.Parse_type_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(28) // :equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(58)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[27].str)
}
func (parser *WdlParser) Parse_outputs(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[6][current.terminal.id]
	}
	tree := parser.newParseTree(62)
	ctx.nonterminal_string = "outputs"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 26 { // $outputs = :output :lbrace $_gen7 :rbrace -> Outputs( attributes=$2 )
		ctx.rule_string = rules[26].str
		astParameters := make(map[string]int)
		astParameters["attributes"] = 2
		tree.astTransform = &AstTransformNodeCreator{"Outputs", astParameters, []string{"attributes"}}
		token, err = ctx.expect(30) // :output
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen7(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(62)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[26].str)
}
func (parser *WdlParser) Parse_parameter_meta(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[19][current.terminal.id]
	}
	tree := parser.newParseTree(75)
	ctx.nonterminal_string = "parameter_meta"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 29 { // $parameter_meta = :parameter_meta $map -> ParameterMeta( map=$1 )
		ctx.rule_string = rules[29].str
		astParameters := make(map[string]int)
		astParameters["map"] = 1
		tree.astTransform = &AstTransformNodeCreator{"ParameterMeta", astParameters, []string{"map"}}
		token, err = ctx.expect(39) // :parameter_meta
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_map(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(75)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[29].str)
}
func (parser *WdlParser) Parse_postfix_quantifier(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[49][current.terminal.id]
	}
	tree := parser.newParseTree(105)
	ctx.nonterminal_string = "postfix_quantifier"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 40 { // $postfix_quantifier = :qmark
		ctx.rule_string = rules[40].str
		tree.astTransform = &AstTransformSubstitution{0}
		token, err = ctx.expect(25) // :qmark
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	if rule == 41 { // $postfix_quantifier = :plus
		ctx.rule_string = rules[41].str
		tree.astTransform = &AstTransformSubstitution{0}
		token, err = ctx.expect(55) // :plus
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(105)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[41].str)
}
func (parser *WdlParser) Parse_runtime(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[37][current.terminal.id]
	}
	tree := parser.newParseTree(93)
	ctx.nonterminal_string = "runtime"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 28 { // $runtime = :runtime $map -> Runtime( map=$1 )
		ctx.rule_string = rules[28].str
		astParameters := make(map[string]int)
		astParameters["map"] = 1
		tree.astTransform = &AstTransformNodeCreator{"Runtime", astParameters, []string{"map"}}
		token, err = ctx.expect(51) // :runtime
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_map(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(93)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[28].str)
}
func (parser *WdlParser) Parse_scatter(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[24][current.terminal.id]
	}
	tree := parser.newParseTree(80)
	ctx.nonterminal_string = "scatter"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 70 { // $scatter = :scatter :lparen :identifier :in $e :rparen :lbrace $_gen11 :rbrace -> Scatter( item=$2, collection=$4, body=$7 )
		ctx.rule_string = rules[70].str
		astParameters := make(map[string]int)
		astParameters["item"] = 2
		astParameters["collection"] = 4
		astParameters["body"] = 7
		tree.astTransform = &AstTransformNodeCreator{"Scatter", astParameters, []string{"item", "collection", "body"}}
		token, err = ctx.expect(2) // :scatter
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(54) // :lparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(46) // :in
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(53) // :rparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen11(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(80)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[70].str)
}
func (parser *WdlParser) Parse_sections(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[29][current.terminal.id]
	}
	tree := parser.newParseTree(85)
	ctx.nonterminal_string = "sections"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 13 { // $sections = $command
		ctx.rule_string = rules[13].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_command(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 14 { // $sections = $outputs
		ctx.rule_string = rules[14].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_outputs(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 15 { // $sections = $runtime
		ctx.rule_string = rules[15].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_runtime(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 16 { // $sections = $parameter_meta
		ctx.rule_string = rules[16].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_parameter_meta(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 17 { // $sections = $meta
		ctx.rule_string = rules[17].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_meta(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(85)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[17].str)
}
func (parser *WdlParser) Parse_setter(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[31][current.terminal.id]
	}
	tree := parser.newParseTree(87)
	ctx.nonterminal_string = "setter"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 39 { // $setter = :equal $e -> $1
		ctx.rule_string = rules[39].str
		tree.astTransform = &AstTransformSubstitution{1}
		token, err = ctx.expect(28) // :equal
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(87)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[39].str)
}
func (parser *WdlParser) Parse_task(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[13][current.terminal.id]
	}
	tree := parser.newParseTree(69)
	ctx.nonterminal_string = "task"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 12 { // $task = :task :identifier :lbrace $_gen3 $_gen4 :rbrace -> Task( name=$1, declarations=$3, sections=$4 )
		ctx.rule_string = rules[12].str
		astParameters := make(map[string]int)
		astParameters["name"] = 1
		astParameters["declarations"] = 3
		astParameters["sections"] = 4
		tree.astTransform = &AstTransformNodeCreator{"Task", astParameters, []string{"name", "declarations", "sections"}}
		token, err = ctx.expect(5) // :task
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen3(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		subtree, err = parser.Parse__gen4(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(69)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[12].str)
}
func (parser *WdlParser) Parse_wf_body_element(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[4][current.terminal.id]
	}
	tree := parser.newParseTree(60)
	ctx.nonterminal_string = "wf_body_element"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 45 { // $wf_body_element = $call
		ctx.rule_string = rules[45].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_call(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 46 { // $wf_body_element = $declaration
		ctx.rule_string = rules[46].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_declaration(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 47 { // $wf_body_element = $while_loop
		ctx.rule_string = rules[47].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_while_loop(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 48 { // $wf_body_element = $if_stmt
		ctx.rule_string = rules[48].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_if_stmt(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 49 { // $wf_body_element = $scatter
		ctx.rule_string = rules[49].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_scatter(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 50 { // $wf_body_element = $wf_outputs
		ctx.rule_string = rules[50].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_wf_outputs(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(60)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[50].str)
}
func (parser *WdlParser) Parse_wf_output(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[40][current.terminal.id]
	}
	tree := parser.newParseTree(96)
	ctx.nonterminal_string = "wf_output"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 66 { // $wf_output = :fqn $_gen17 -> WorkflowOutput( fqn=$0, wildcard=$1 )
		ctx.rule_string = rules[66].str
		astParameters := make(map[string]int)
		astParameters["fqn"] = 0
		astParameters["wildcard"] = 1
		tree.astTransform = &AstTransformNodeCreator{"WorkflowOutput", astParameters, []string{"fqn", "wildcard"}}
		token, err = ctx.expect(23) // :fqn
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen17(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(96)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[66].str)
}
func (parser *WdlParser) Parse_wf_output_wildcard(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[22][current.terminal.id]
	}
	tree := parser.newParseTree(78)
	ctx.nonterminal_string = "wf_output_wildcard"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 67 { // $wf_output_wildcard = :dot :asterisk -> $1
		ctx.rule_string = rules[67].str
		tree.astTransform = &AstTransformSubstitution{1}
		token, err = ctx.expect(33) // :dot
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(52) // :asterisk
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(78)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[67].str)
}
func (parser *WdlParser) Parse_wf_outputs(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[32][current.terminal.id]
	}
	tree := parser.newParseTree(88)
	ctx.nonterminal_string = "wf_outputs"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 63 { // $wf_outputs = :output :lbrace $_gen16 :rbrace -> WorkflowOutputs( outputs=$2 )
		ctx.rule_string = rules[63].str
		astParameters := make(map[string]int)
		astParameters["outputs"] = 2
		tree.astTransform = &AstTransformNodeCreator{"WorkflowOutputs", astParameters, []string{"outputs"}}
		token, err = ctx.expect(30) // :output
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen16(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(88)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[63].str)
}
func (parser *WdlParser) Parse_while_loop(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[0][current.terminal.id]
	}
	tree := parser.newParseTree(56)
	ctx.nonterminal_string = "while_loop"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 68 { // $while_loop = :while :lparen $e :rparen :lbrace $_gen11 :rbrace -> WhileLoop( expression=$2, body=$5 )
		ctx.rule_string = rules[68].str
		astParameters := make(map[string]int)
		astParameters["expression"] = 2
		astParameters["body"] = 5
		tree.astTransform = &AstTransformNodeCreator{"WhileLoop", astParameters, []string{"expression", "body"}}
		token, err = ctx.expect(36) // :while
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(54) // :lparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse_e(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(53) // :rparen
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen11(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(56)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[68].str)
}
func (parser *WdlParser) Parse_workflow(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[1][current.terminal.id]
	}
	tree := parser.newParseTree(57)
	ctx.nonterminal_string = "workflow"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 44 { // $workflow = :workflow :identifier :lbrace $_gen11 :rbrace -> Workflow( name=$1, body=$3 )
		ctx.rule_string = rules[44].str
		astParameters := make(map[string]int)
		astParameters["name"] = 1
		astParameters["body"] = 3
		tree.astTransform = &AstTransformNodeCreator{"Workflow", astParameters, []string{"name", "body"}}
		token, err = ctx.expect(15) // :workflow
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(6) // :identifier
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		token, err = ctx.expect(14) // :lbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		subtree, err = parser.Parse__gen11(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		token, err = ctx.expect(11) // :rbrace
		if err != nil {
			return nil, err
		}
		tree.Add(token)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(57)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[44].str)
}
func (parser *WdlParser) Parse_workflow_or_task_or_decl(ctx *ParserContext) (*parseTree, error) {
	current := ctx.tokens.current()
	rule := -1
	_ = rule
	if current != nil {
		rule = parser.table[56][current.terminal.id]
	}
	tree := parser.newParseTree(112)
	ctx.nonterminal_string = "workflow_or_task_or_decl"
	var subtree *parseTree
	var token *Token
	var err error
	_ = token
	_ = err
	_ = subtree
	if current == nil {
		return nil, ctx.errors.unexpected_eof()
	}
	if rule == 3 { // $workflow_or_task_or_decl = $workflow
		ctx.rule_string = rules[3].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_workflow(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 4 { // $workflow_or_task_or_decl = $task
		ctx.rule_string = rules[4].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_task(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	if rule == 5 { // $workflow_or_task_or_decl = $declaration
		ctx.rule_string = rules[5].str
		tree.astTransform = &AstTransformSubstitution{0}
		subtree, err = parser.Parse_declaration(ctx)
		if err != nil {
			return nil, err
		}
		tree.Add(subtree)
		return tree, nil
	}
	nt := parser.NonTerminalFromId(112)
	expected := make([]*terminal, len(nt.firstSet))
	for _, terminalId := range nt.firstSet {
		for _, terminal := range parser.terminals {
			if terminal.id == terminalId {
				expected = append(expected, terminal)
				break
			}
		}
	}
	return nil, ctx.errors.unexpected_symbol(
		ctx.nonterminal_string,
		ctx.tokens.current(),
		expected,
		rules[5].str)
}

/*
 * Lexer Code
 */
/* START USER CODE */
type escapeSequence struct {
	sequence    *regexp.Regexp
	replaceWith string
}
type charEscape struct {
	sequence *regexp.Regexp
	base     int
}
type wdlContext struct {
	wf_or_task      string
	escapeSequences []escapeSequence
	charEscapes     []charEscape
}

func lexerInit() interface{} {
	ctx := wdlContext{
		"",
		[]escapeSequence{
			escapeSequence{regexp.MustCompile("\\n"), "\n"},
			escapeSequence{regexp.MustCompile("\\r"), "\r"},
			escapeSequence{regexp.MustCompile("\\b"), "\b"},
			escapeSequence{regexp.MustCompile("\\t"), "\t"},
			escapeSequence{regexp.MustCompile("\\a"), "\a"},
			escapeSequence{regexp.MustCompile("\\v"), "\v"},
			escapeSequence{regexp.MustCompile("\\\""), "\""},
			escapeSequence{regexp.MustCompile("\\'"), "'"},
			escapeSequence{regexp.MustCompile("\\?"), "?"},
			escapeSequence{regexp.MustCompile("\\\\"), "\\"}},
		[]charEscape{
			charEscape{regexp.MustCompile("(\\\\([0-7]{1,3}))"), 8},
			charEscape{regexp.MustCompile("(\\\\[xX]([0-9a-fA-F]{1,4}))"), 16},
			charEscape{regexp.MustCompile("(\\\\[uU]([0-9a-fA-F]{4}))"), 16}}}
	return &ctx
}
func workflow(ctx *LexerContext, terminal *terminal, sourceString string, line, col int) {
	switch t := ctx.userContext.(type) {
	case *wdlContext:
		t.wf_or_task = "workflow"
	}
	default_action(ctx, terminal, sourceString, line, col)
}
func task(ctx *LexerContext, terminal *terminal, sourceString string, line, col int) {
	switch t := ctx.userContext.(type) {
	case *wdlContext:
		t.wf_or_task = "task"
	}
	default_action(ctx, terminal, sourceString, line, col)
}
func output(ctx *LexerContext, terminal *terminal, sourceString string, line, col int) {
	switch t := ctx.userContext.(type) {
	case *wdlContext:
		if t != nil && t.wf_or_task == "workflow" {
			ctx.StackPush("wf_output")
		}
	}
	default_action(ctx, terminal, sourceString, line, col)
}
func unescape(ctx *LexerContext, terminal *terminal, sourceString string, line, col int) {
	var userCtx *wdlContext
	userCtx, ok := ctx.userContext.(*wdlContext)
	if !ok {
		panic("unescape(): ctx.userContext could not be cast to *wdlContext")
	}
	for _, seq := range userCtx.escapeSequences {
		sourceString = seq.sequence.ReplaceAllString(sourceString, seq.replaceWith)
	}
	for _, seq := range userCtx.charEscapes {
		for _, matchGroup := range seq.sequence.FindAllStringSubmatch(sourceString, -1) {
			i, err := strconv.ParseInt(matchGroup[1], seq.base, 32)
			if err != nil {
				panic("todo: don't panic here")
			}
			sourceString = strings.Replace(sourceString, matchGroup[0], string([]byte{byte(i)}), -1)
		}
	}
	default_action(ctx, terminal, sourceString, line, col)
}

/* END USER CODE */
func (ctx *LexerContext) emit(terminal *terminal, sourceString string, line, col int) {
	if terminal != nil {
		x := &Token{terminal, sourceString, ctx.resource, line, col}
		ctx.tokens = append(ctx.tokens, x)
	}
}
func default_action(ctx *LexerContext, terminal *terminal, sourceString string, line, col int) {
	ctx.emit(terminal, sourceString, line, col)
}
func post_filter(tokens []*Token) []*Token {
	return tokens
}
func lexerDestroy(context interface{}) {}

type LexerContext struct {
	source      string
	resource    string
	handler     SyntaxErrorHandler
	userContext interface{}
	stack       []string
	line        int
	col         int
	tokens      []*Token
}

func (ctx *LexerContext) StackPush(mode string) {
	ctx.stack = append(ctx.stack, mode)
}
func (ctx *LexerContext) StackPop() {
	ctx.stack = ctx.stack[:len(ctx.stack)-1]
}
func (ctx *LexerContext) StackPeek() string {
	return ctx.stack[len(ctx.stack)-1]
}

type HermesRegex struct {
	regex   *regexp.Regexp
	outputs []HermesLexerAction
}
type HermesLexerAction interface {
	HandleMatch(ctx *LexerContext, groups []string, indexes []int)
}
type LexerRegexOutput struct {
	terminal *terminal
	group    int
	function func(*LexerContext, *terminal, string, int, int)
}

func (lro *LexerRegexOutput) HandleMatch(ctx *LexerContext, groups []string, indexes []int) {
	sourceString := groups[0]
	if lro.group == -1 {
		sourceString = ""
	}
	length := 0
	if lro.group > 0 {
		sourceString = groups[lro.group]
		startIndex := lro.group * 2
		length = indexes[startIndex]
	}
	groupLine, groupCol := _advance_line_col(ctx.source, length, ctx.line, ctx.col)
	lro.function(ctx, lro.terminal, sourceString, groupLine, groupCol)
}

type LexerStackPush struct {
	mode string
}

func (lsp *LexerStackPush) HandleMatch(ctx *LexerContext, groups []string, indexes []int) {
	ctx.StackPush(lsp.mode)
}

type LexerAction struct {
	action string
}

func (la *LexerAction) HandleMatch(ctx *LexerContext, groups []string, indexes []int) {
	if la.action == "pop" {
		ctx.StackPop()
	}
}

var regex map[string][]*HermesRegex

func initRegexes() map[string][]*HermesRegex {
	terminals := initTerminals()
	initMutex.Lock()
	defer initMutex.Unlock()
	var findTerminal = func(name string) *terminal {
		for _, terminal := range terminals {
			if terminal.idStr == name {
				return terminal
			}
		}
		return nil
	}
	if regex == nil {
		regex = make(map[string][]*HermesRegex)
		var matchActions []HermesLexerAction
		var matchFunction func(*LexerContext, *terminal, string, int, int)
		var r *regexp.Regexp
		regex["default"] = make([]*HermesRegex, 51)
		r = regexp.MustCompile("^\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["default"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(?s)/\\*(.*?)\\*/")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["default"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^#.*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["default"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^task\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = task
		matchActions[0] = &LexerRegexOutput{findTerminal("task"), 0, matchFunction}
		regex["default"][3] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(call)\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("call"), 1, matchFunction}
		matchActions[1] = &LexerStackPush{"task_fqn"}
		regex["default"][4] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^workflow\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = workflow
		matchActions[0] = &LexerRegexOutput{findTerminal("workflow"), 0, matchFunction}
		regex["default"][5] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^import\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("import"), 0, matchFunction}
		regex["default"][6] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^input\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("input"), 0, matchFunction}
		regex["default"][7] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^output\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = output
		matchActions[0] = &LexerRegexOutput{findTerminal("output"), 0, matchFunction}
		regex["default"][8] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^as\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("as"), 0, matchFunction}
		regex["default"][9] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^if\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("if"), 0, matchFunction}
		regex["default"][10] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^while\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("while"), 0, matchFunction}
		regex["default"][11] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^runtime\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("runtime"), 0, matchFunction}
		regex["default"][12] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^scatter\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("scatter"), 0, matchFunction}
		matchActions[1] = &LexerStackPush{"scatter"}
		regex["default"][13] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^command\\s*<<<")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("command"), 0, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("command_start"), 0, matchFunction}
		matchActions[2] = &LexerStackPush{"command_alt"}
		regex["default"][14] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^command\\s*\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("command"), 0, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("command_start"), 0, matchFunction}
		matchActions[2] = &LexerStackPush{"command"}
		regex["default"][15] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^parameter_meta\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("parameter_meta"), 0, matchFunction}
		regex["default"][16] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^meta\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("meta"), 0, matchFunction}
		regex["default"][17] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(true|false)\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("boolean"), 0, matchFunction}
		regex["default"][18] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(object)\\s*(\\{)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("object"), 0, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("lbrace"), 0, matchFunction}
		regex["default"][19] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(Array|Map|Object|Boolean|Int|Float|Uri|File|String)\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("type"), 0, matchFunction}
		regex["default"][20] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[a-zA-Z]([a-zA-Z0-9_])*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("identifier"), 0, matchFunction}
		regex["default"][21] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\\"([^\\\\\\\"\\n]|\\\\[\\\"\\'nrbtfav\\\\?]|\\\\[0-7]{1,3}|\\\\x[0-9a-fA-F]+|\\\\[uU]([0-9a-fA-F]{4})([0-9a-fA-F]{4})?)*\\\"")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = unescape
		matchActions[0] = &LexerRegexOutput{findTerminal("string"), 0, matchFunction}
		regex["default"][22] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\\"([^\\\\\\\"\\n]|\\\\[\\\"\\'nrbtfav\\\\?]|\\\\[0-7]{1,3}|\\\\x[0-9a-fA-F]+|\\\\[uU]([0-9a-fA-F]{4})([0-9a-fA-F]{4})?)*\\\"")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = unescape
		matchActions[0] = &LexerRegexOutput{findTerminal("string"), 0, matchFunction}
		regex["default"][23] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^:")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("colon"), 0, matchFunction}
		regex["default"][24] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^,")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("comma"), 0, matchFunction}
		regex["default"][25] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^==")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_equal"), 0, matchFunction}
		regex["default"][26] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\|\\|")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_pipe"), 0, matchFunction}
		regex["default"][27] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\&\\&")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_ampersand"), 0, matchFunction}
		regex["default"][28] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^!=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("not_equal"), 0, matchFunction}
		regex["default"][29] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("equal"), 0, matchFunction}
		regex["default"][30] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\.")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dot"), 0, matchFunction}
		regex["default"][31] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lbrace"), 0, matchFunction}
		regex["default"][32] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\}")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rbrace"), 0, matchFunction}
		regex["default"][33] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\(")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lparen"), 0, matchFunction}
		regex["default"][34] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rparen"), 0, matchFunction}
		regex["default"][35] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\[")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lsquare"), 0, matchFunction}
		regex["default"][36] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\]")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rsquare"), 0, matchFunction}
		regex["default"][37] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("plus"), 0, matchFunction}
		regex["default"][38] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("asterisk"), 0, matchFunction}
		regex["default"][39] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^-")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dash"), 0, matchFunction}
		regex["default"][40] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^/")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("slash"), 0, matchFunction}
		regex["default"][41] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^%")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("percent"), 0, matchFunction}
		regex["default"][42] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^<=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lteq"), 0, matchFunction}
		regex["default"][43] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^<")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lt"), 0, matchFunction}
		regex["default"][44] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^>=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("gteq"), 0, matchFunction}
		regex["default"][45] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^>")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("gt"), 0, matchFunction}
		regex["default"][46] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^!")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("not"), 0, matchFunction}
		regex["default"][47] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\?")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("qmark"), 0, matchFunction}
		regex["default"][48] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^-?[0-9]+\\.[0-9]+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("float"), 0, matchFunction}
		regex["default"][49] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[0-9]+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("integer"), 0, matchFunction}
		regex["default"][50] = &HermesRegex{r, matchActions}
		regex["wf_output"] = make([]*HermesRegex, 7)
		r = regexp.MustCompile("^\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["wf_output"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lbrace"), 0, matchFunction}
		regex["wf_output"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\}")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rbrace"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["wf_output"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^,")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("comma"), 0, matchFunction}
		regex["wf_output"][3] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\.")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dot"), 0, matchFunction}
		regex["wf_output"][4] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("asterisk"), 0, matchFunction}
		regex["wf_output"][5] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[a-zA-Z]([a-zA-Z0-9_])*(\\.[a-zA-Z]([a-zA-Z0-9_])*)*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("fqn"), 0, matchFunction}
		regex["wf_output"][6] = &HermesRegex{r, matchActions}
		regex["task_fqn"] = make([]*HermesRegex, 2)
		r = regexp.MustCompile("^\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["task_fqn"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[a-zA-Z]([a-zA-Z0-9_])*(\\.[a-zA-Z]([a-zA-Z0-9_])*)*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("fqn"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["task_fqn"][1] = &HermesRegex{r, matchActions}
		regex["scatter"] = make([]*HermesRegex, 8)
		r = regexp.MustCompile("^\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["scatter"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rparen"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["scatter"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\(")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lparen"), 0, matchFunction}
		regex["scatter"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\.")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dot"), 0, matchFunction}
		regex["scatter"][3] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\[")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lsquare"), 0, matchFunction}
		regex["scatter"][4] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\]")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rsquare"), 0, matchFunction}
		regex["scatter"][5] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^in\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("in"), 0, matchFunction}
		regex["scatter"][6] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[a-zA-Z]([a-zA-Z0-9_])*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("identifier"), 0, matchFunction}
		regex["scatter"][7] = &HermesRegex{r, matchActions}
		regex["command"] = make([]*HermesRegex, 4)
		r = regexp.MustCompile("^\\}")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("command_end"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["command"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\$\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_param_start"), 0, matchFunction}
		matchActions[1] = &LexerStackPush{"cmd_param"}
		regex["command"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(?s)(.*?)(\\$\\{)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_part"), 1, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("cmd_param_start"), 2, matchFunction}
		matchActions[2] = &LexerStackPush{"cmd_param"}
		regex["command"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(?s)(.*?)(\\})")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_part"), 1, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("command_end"), 0, matchFunction}
		matchActions[2] = &LexerAction{"pop"}
		regex["command"][3] = &HermesRegex{r, matchActions}
		regex["command_alt"] = make([]*HermesRegex, 4)
		r = regexp.MustCompile("^>>>")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("command_end"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["command_alt"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\$\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_param_start"), 0, matchFunction}
		matchActions[1] = &LexerStackPush{"cmd_param"}
		regex["command_alt"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(?s)(.*?)(\\$\\{)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_part"), 1, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("cmd_param_start"), 2, matchFunction}
		matchActions[2] = &LexerStackPush{"cmd_param"}
		regex["command_alt"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(?s)(.*?)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_part"), 0, matchFunction}
		regex["command_alt"][3] = &HermesRegex{r, matchActions}
		regex["cmd_param"] = make([]*HermesRegex, 40)
		r = regexp.MustCompile("^\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 0)
		regex["cmd_param"][0] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\}")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 2)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_param_end"), 0, matchFunction}
		matchActions[1] = &LexerAction{"pop"}
		regex["cmd_param"][1] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\[")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lsquare"), 0, matchFunction}
		regex["cmd_param"][2] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\]")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rsquare"), 0, matchFunction}
		regex["cmd_param"][3] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("equal"), 0, matchFunction}
		regex["cmd_param"][4] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("plus"), 0, matchFunction}
		regex["cmd_param"][5] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("asterisk"), 0, matchFunction}
		regex["cmd_param"][6] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[0-9]+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("integer"), 0, matchFunction}
		regex["cmd_param"][7] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^([a-zA-Z]([a-zA-Z0-9_])*)\\s*(=)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 3)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("cmd_attr_hint"), -1, matchFunction}
		matchFunction = default_action
		matchActions[1] = &LexerRegexOutput{findTerminal("identifier"), 1, matchFunction}
		matchFunction = default_action
		matchActions[2] = &LexerRegexOutput{findTerminal("equal"), 2, matchFunction}
		regex["cmd_param"][8] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(true|false)\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("boolean"), 0, matchFunction}
		regex["cmd_param"][9] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^(Array|Map|Object|Boolean|Int|Float|Uri|File|String)\\s+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("type"), 0, matchFunction}
		regex["cmd_param"][10] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[a-zA-Z]([a-zA-Z0-9_])*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("identifier"), 0, matchFunction}
		regex["cmd_param"][11] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^:")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("colon"), 0, matchFunction}
		regex["cmd_param"][12] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^,")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("comma"), 0, matchFunction}
		regex["cmd_param"][13] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\.")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dot"), 0, matchFunction}
		regex["cmd_param"][14] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^==")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_equal"), 0, matchFunction}
		regex["cmd_param"][15] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\|\\|")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_pipe"), 0, matchFunction}
		regex["cmd_param"][16] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\&\\&")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("double_ampersand"), 0, matchFunction}
		regex["cmd_param"][17] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^!=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("not_equal"), 0, matchFunction}
		regex["cmd_param"][18] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("equal"), 0, matchFunction}
		regex["cmd_param"][19] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\.")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dot"), 0, matchFunction}
		regex["cmd_param"][20] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\{")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lbrace"), 0, matchFunction}
		regex["cmd_param"][21] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\(")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lparen"), 0, matchFunction}
		regex["cmd_param"][22] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\)")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rparen"), 0, matchFunction}
		regex["cmd_param"][23] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\[")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lsquare"), 0, matchFunction}
		regex["cmd_param"][24] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\]")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("rsquare"), 0, matchFunction}
		regex["cmd_param"][25] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("plus"), 0, matchFunction}
		regex["cmd_param"][26] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\*")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("asterisk"), 0, matchFunction}
		regex["cmd_param"][27] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^-")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("dash"), 0, matchFunction}
		regex["cmd_param"][28] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^/")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("slash"), 0, matchFunction}
		regex["cmd_param"][29] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^%")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("percent"), 0, matchFunction}
		regex["cmd_param"][30] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^<=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lteq"), 0, matchFunction}
		regex["cmd_param"][31] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^<")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("lt"), 0, matchFunction}
		regex["cmd_param"][32] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^>=")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("gteq"), 0, matchFunction}
		regex["cmd_param"][33] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^>")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("gt"), 0, matchFunction}
		regex["cmd_param"][34] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^!")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("not"), 0, matchFunction}
		regex["cmd_param"][35] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\\"([^\\\\\\\"\\n]|\\\\[\\\"\\'nrbtfav\\\\?]|\\\\[0-7]{1,3}|\\\\x[0-9a-fA-F]+|\\\\[uU]([0-9a-fA-F]{4})([0-9a-fA-F]{4})?)*\\\"")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = unescape
		matchActions[0] = &LexerRegexOutput{findTerminal("string"), 0, matchFunction}
		regex["cmd_param"][36] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^\\\"([^\\\\\\\"\\n]|\\\\[\\\"\\'nrbtfav\\\\?]|\\\\[0-7]{1,3}|\\\\x[0-9a-fA-F]+|\\\\[uU]([0-9a-fA-F]{4})([0-9a-fA-F]{4})?)*\\\"")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = unescape
		matchActions[0] = &LexerRegexOutput{findTerminal("string"), 0, matchFunction}
		regex["cmd_param"][37] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^-?[0-9]+\\.[0-9]+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("float"), 0, matchFunction}
		regex["cmd_param"][38] = &HermesRegex{r, matchActions}
		r = regexp.MustCompile("^[0-9]+")
		// NOTE: flags are set on regex.regex inside of grammar.py (convert_regex_str)
		matchActions = make([]HermesLexerAction, 1)
		matchFunction = default_action
		matchActions[0] = &LexerRegexOutput{findTerminal("integer"), 0, matchFunction}
		regex["cmd_param"][39] = &HermesRegex{r, matchActions}
	}
	return regex
}

type WdlLexer struct {
	regex map[string][]*HermesRegex
}

func NewWdlLexer() *WdlLexer {
	return &WdlLexer{initRegexes()}
}
func _advance_line_col(s string, length, line, col int) (int, int) {
	if length == 0 {
		return line, col
	}
	c := 0
	for _, r := range s {
		c += 1
		if r == '\n' {
			line += 1
			col = 1
		} else {
			col += 1
		}
		if c == length {
			break
		}
	}
	return line, col
}
func (lexer *WdlLexer) _advance_string(ctx *LexerContext, s string) {
	ctx.line, ctx.col = _advance_line_col(s, len(s), ctx.line, ctx.col)
	ctx.source = ctx.source[len(s):]
}
func (lexer *WdlLexer) _next(ctx *LexerContext) bool {
	for _, regex := range lexer.regex[ctx.StackPeek()] {
		groups := regex.regex.FindStringSubmatch(ctx.source)
		indexes := regex.regex.FindStringSubmatchIndex(ctx.source)
		if len(groups) != 0 && indexes != nil {
			for _, output := range regex.outputs {
				dbgStrs := make([]string, len(groups))
				for i, s := range groups {
					dbgStrs[i] = strconv.QuoteToASCII(s)
				}
				output.HandleMatch(ctx, groups, indexes)
			}
			lexer._advance_string(ctx, groups[0])
			return len(groups[0]) > 0
		}
	}
	return false
}
func (lexer *WdlLexer) lex(source, resource string, handler SyntaxErrorHandler) ([]*Token, error) {
	userContext := lexerInit()
	ctx := &LexerContext{source, resource, handler, userContext, nil, 1, 1, nil}
	ctx.StackPush("default")
	for len(ctx.source) > 0 {
		matched := lexer._next(ctx)
		if matched == false {
			return nil, ctx.handler.unrecognized_token(source, ctx.line, ctx.col)
		}
	}
	lexerDestroy(userContext)
	filteredTokens := post_filter(ctx.tokens)
	return filteredTokens, nil
}
func (lexer *WdlLexer) Lex(source, resource string, handler SyntaxErrorHandler) (*TokenStream, error) {
	tokens, err := lexer.lex(source, resource, handler)
	if err != nil {
		return nil, err
	}
	return &TokenStream{tokens, 0}, nil
}
