package maple

import (
	"bytes"
	"github.com/satori/go.uuid"
	"strings"
	"testing"
)

func testEngine(buf *bytes.Buffer) *Engine {
	log := NewLogger().ToWriter(buf)
	return NewEngine(log, 1, 1)
}

func TestStartDispatcher(t *testing.T) {
	var buf bytes.Buffer
	engine := testEngine(&buf)
	if !engine.wd.IsAlive() {
		t.Fatalf("Expecting the dispatcher to be alive after starting it")
	}
}

func TestCreateWorkflow(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger().ToWriter(&buf)
	db := NewMapleDb("sqlite3", "DBfortest", log)
	ctx := db.NewWorkflow(uuid.NewV4(), &WorkflowSources{"wdl", "inputs", "options"}, log)
	if db.GetWorkflowStatus(ctx, log) != "NotStarted" {
		t.Fatalf("Expecting workflow in NotStarted state")
	}
	db.SetWorkflowStatus(ctx, "Started", log)
	if db.GetWorkflowStatus(ctx, log) != "Started" {
		t.Fatalf("Expecting workflow in Started state")
	}
}

func TestRunWorkflow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	engine := testEngine(&buf)
	context := engine.RunWorkflow("wdl", "inputs", "options", uuid.NewV4())

	if context.status != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}

	if !strings.Contains(buf.String(), "Workflow Completed") {
		t.Fatalf("Expecting a 'Workflow Completed' message")
	}
}

func TestRunWorkflow2(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	engine := testEngine(&buf)
	context := engine.RunWorkflow("wdl", "inputs", "options", uuid.NewV4())

	if context.status != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}

	if !strings.Contains(buf.String(), "Workflow Completed") {
		t.Fatalf("Expecting a 'Workflow Completed' message")
	}
}
