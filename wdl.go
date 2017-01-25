package maple

import (
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
)

type GraphNode interface {
	upstream() []GraphNode
	downstream() []GraphNode
}

type Expression struct {
	ast *Ast
}

func (expr *Expression) Evaluate(inputs map[string]WdlValue) (WdlValue, error) {
	return evaluate(expr.ast, inputs)
}

func evaluate(node AstNode, inputs map[string]WdlValue) (WdlValue, error) {
	switch n := node.(type) {
	case *Ast:
		if n.name == "Add" {
			lhs, err := evaluate(n.attributes["lhs"], inputs)
			if err != nil {
				return nil, err
			}
			rhs, err := evaluate(n.attributes["rhs"], inputs)
			if err != nil {
				return nil, err
			}
			return lhs.Add(rhs)
		}
	case *Token:
		switch n.TerminalType() {
		case "identifier":
			return inputs[n.sourceString], nil
		case "integer":
			num, err := strconv.Atoi(n.sourceString)
			if err != nil {
				return nil, fmt.Errorf("integer token %v does not contain an integer", n)
			}
			return WdlIntegerValue{num}, nil
		default:
			return nil, fmt.Errorf("fill me in")
		}
	}
	return nil, fmt.Errorf("fill me in")
}

type Command struct {
	ast   *Ast
	parts []CommandPart
}

func (c *Command) Instantiate(inputs map[string]WdlValue) (string, error) {
	var stringParts []string
	for _, part := range c.parts {
		partString, err := part.Instantiate(inputs)
		if err != nil {
			return "", err
		}
		stringParts = append(stringParts, partString)
	}

	instantiatedCommand := strings.TrimLeft(strings.Join(stringParts, ""), "\n")
	instantiatedCommand = strings.TrimRight(instantiatedCommand, " \n")
	if len(instantiatedCommand) == 0 {
		return instantiatedCommand, nil
	}

	commonLineWhitespace := 9999
	counting := true
	currentLineWhitespace := 0
	for _, char := range instantiatedCommand {
		if counting && char == ' ' {
			currentLineWhitespace += 1
			continue
		}
		counting = false
		if currentLineWhitespace < commonLineWhitespace {
			commonLineWhitespace = currentLineWhitespace
		}
		if char == '\n' {
			currentLineWhitespace = 0
			counting = true
		}
	}

	stringParts = nil
	strippedString := ""
	for _, line := range strings.Split(instantiatedCommand, "\n") {
		if len(line) == 0 {
			strippedString = "\n"
		} else {
			strippedString = line[commonLineWhitespace:]
		}
		stringParts = append(stringParts, strippedString)
	}
	return strings.Join(stringParts, ""), nil
}

type CommandPart interface {
	Instantiate(inputs map[string]WdlValue) (string, error)
}

type CommandPartString struct {
	str string
}

func (part *CommandPartString) Instantiate(inputs map[string]WdlValue) (string, error) {
	return part.str, nil
}

type CommandPartExpression struct {
	expr *Expression
}

func (part *CommandPartExpression) Instantiate(inputs map[string]WdlValue) (string, error) {
	value, err := part.expr.Evaluate(inputs)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

type WdlType interface {
	Equals(other WdlType) bool
	WdlString() string
}

type WdlIntegerType struct{}

func (WdlIntegerType) Equals(other WdlType) bool {
	switch other.(type) {
	case WdlIntegerType:
		return true
	default:
		return false
	}
}

func (WdlIntegerType) WdlString() string {
	return "Int"
}

type WdlValue interface {
	Add(other WdlValue) (WdlValue, error)
	Type() WdlType
	String() string
}

type WdlIntegerValue struct {
	value int
}

func (l WdlIntegerValue) Add(other WdlValue) (WdlValue, error) {
	switch r := other.(type) {
	case WdlIntegerValue:
		return WdlIntegerValue{l.value + r.value}, nil
	default:
		return nil, fmt.Errorf("Cannot add")
	}
}

func (v WdlIntegerValue) Type() WdlType {
	return WdlIntegerType{}
}

func (v WdlIntegerValue) String() string {
	return strconv.Itoa(v.value)
}

///////////////////////////////////////////////////////////////////////////////

type WdlNamespace struct {
	namespaces   map[string]*WdlNamespace
	declarations []*Declaration
	tasks        []*Task
	workflows    []*Workflow
	ast          *Ast
}

func (ns *WdlNamespace) FindTask(name string) *Task {
	for _, task := range ns.tasks {
		if task.name == name {
			return task
		}
	}
	return nil
}

func (ns *WdlNamespace) Resolve(name string) Scope {
	return nil
}

func LoadWdlFromFile(path string) (*WdlNamespace, error) {
	parser := NewWdlParser()
	lexer := NewWdlLexer()
	handler := &DefaultSyntaxErrorHandler{}

	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	tokens, err := lexer.Lex(string(bytes), path, handler)
	if err != nil {
		return nil, err
	}

	tree, err := parser.ParseTokens(tokens, handler)
	if err != nil {
		return nil, err
	}

	ast := tree.Ast()
	return loadNamespace(ast.(*Ast))
}

func loadNamespace(ast *Ast) (*WdlNamespace, error) {
	ns := WdlNamespace{ast: ast}

	// Handle 'import' statements
	if val, ok := ast.attributes["imports"]; ok {
		for _, importAstNode := range *val.(*AstList) {
			importAst := importAstNode.(*Ast)
			uri := importAst.attributes["uri"].(*Token).sourceString
			subNs, err := doImport(uri)
			if err != nil {
				return nil, err
			}
			nsNameToken := importAst.attributes["namespace"]
			if nsNameToken != nil {
				ns.namespaces[nsNameToken.(*Token).sourceString] = subNs
			} else {
				ns.tasks = append(ns.tasks, subNs.tasks...)
				ns.workflows = append(ns.workflows, subNs.workflows...)
				ns.declarations = append(ns.declarations, subNs.declarations...)
			}
		}
	}

	// Load all tasks
	if val, ok := ast.attributes["body"]; ok {
		for _, bodyAstNode := range *val.(*AstList) {
			bodyAst := bodyAstNode.(*Ast)
			switch bodyAst.name {
			case "Task":
				task, err := loadTask(bodyAst)
				if err != nil {
					return nil, err
				}
				ns.tasks = append(ns.tasks, task)
			}
		}
	}

	// Load declarations and workflows
	if val, ok := ast.attributes["body"]; ok {
		for _, bodyAstNode := range *val.(*AstList) {
			bodyAst := bodyAstNode.(*Ast)
			switch bodyAst.name {
			case "Declaration":
				decl, err := loadDeclaration(bodyAst)
				if err != nil {
					return nil, err
				}
				ns.declarations = append(ns.declarations, decl)
			case "Workflow":
				wf, err := loadWorkflow(&ns, bodyAst)
				if err != nil {
					return nil, err
				}
				ns.workflows = append(ns.workflows, wf)
			case "Task":
				continue
			default:
				return nil, fmt.Errorf("Invalid AST node: %s", bodyAst.String())
			}
		}
	}

	return &ns, nil
}

func doImport(path string) (*WdlNamespace, error) {
	return nil, nil
}

type Scope interface {
	Name() string
}

type Workflow struct {
	name string
	body []Scope
	ast  *Ast
}

func loadWorkflow(ns *WdlNamespace, ast *Ast) (*Workflow, error) {
	wf := Workflow{ast: ast}
	wf.name = ast.attributes["name"].(*Token).sourceString
	if val, ok := ast.attributes["body"]; ok {
		for _, bodyAstNode := range *val.(*AstList) {
			bodyAst := bodyAstNode.(*Ast)
			switch bodyAst.name {
			case "Call":
				call, err := loadCall(ns, bodyAst)
				if err != nil {
					return nil, err
				}
				wf.body = append(wf.body, call)
			default:
				return nil, fmt.Errorf("Invalid AST: %s", ast.String())
			}
			continue
		}
	}
	return &wf, nil
}

type Call struct {
	ast   *Ast
	task  *Task
	alias string
}

func (c *Call) Name() string {
	return c.alias
}

func loadCall(ns *WdlNamespace, ast *Ast) (*Call, error) {
	call := Call{ast: ast}
	taskName := ast.attributes["task"].(*Token).sourceString
	task := ns.FindTask(taskName)
	if task == nil {
		return nil, fmt.Errorf("Cannot find task with name %s", taskName)
	}
	call.task = task
	return &call, nil
}

type Task struct {
	name         string
	declarations []*Declaration
	command      *Command
	outputs      []*Declaration
	ast          *Ast
}

func loadTask(ast *Ast) (*Task, error) {
	task := Task{}
	task.name = ast.attributes["name"].(*Token).sourceString
	if val, ok := ast.attributes["declarations"]; ok {
		for _, declAstNode := range *val.(*AstList) {
			declAst := declAstNode.(*Ast)
			decl, err := loadDeclaration(declAst)
			if err != nil {
				return nil, err
			}
			task.declarations = append(task.declarations, decl)
		}
	}

	if val, ok := ast.attributes["sections"]; ok {
		for _, sectionAstNode := range *val.(*AstList) {
			sectionAst := sectionAstNode.(*Ast)
			switch sectionAst.name {
			case "RawCommand":
				command, err := loadCommand(sectionAst)
				if err != nil {
					return nil, err
				}
				task.command = command
			case "Outputs":
			}
		}
	}
	return &task, nil
}

func loadCommand(ast *Ast) (*Command, error) {
	command := Command{ast: ast}
	for _, cmdPartAst := range *ast.attributes["parts"].(*AstList) {
		switch t := cmdPartAst.(type) {
		case *Token:
			command.parts = append(command.parts, &CommandPartString{t.sourceString})
		case *Ast:
			if t.name != "CommandParameter" {
				return nil, fmt.Errorf("expecting 'CommandParameter' AST, got %s", ast.String())
			}
			expr, err := loadExpression(t.attributes["expr"].(*Ast))
			if err != nil {
				return nil, err
			}
			command.parts = append(command.parts, &CommandPartExpression{expr})
		}
	}
	return &command, nil
}

func loadExpression(ast *Ast) (*Expression, error) {
	return &Expression{ast: ast}, nil
}

type Declaration struct {
	ast *Ast
}

func loadDeclaration(ast *Ast) (*Declaration, error) {
	return nil, nil
}
