package maple

import (
	"fmt"
	"io/ioutil"
)

type GraphNode interface {
	upstream() []GraphNode
	downstream() []GraphNode
}

type Expression struct {
	ast *Ast
}

func (*Expression) Eval(map[string]WdlValue) (WdlValue, error) {
	return nil, nil
}

type Command struct {
	ast   *Ast
	parts []CommandPart
}

func (*Command) Instantiate(inputs map[string]WdlValue) string {
	return "echo 3"
}

type CommandPart interface {
	Instantiate(inputs map[string]WdlValue) string
}

type CommandPartString struct {
	str string
}

func (part *CommandPartString) Instantiate(inputs map[string]WdlValue) string {
	return part.str
}

type CommandPartExpression struct {
	expr *Expression
}

func (part *CommandPartExpression) Instantiate(inputs map[string]WdlValue) string {
	return "foobar"
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
	fmt.Println(ast.PrettyString())
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
