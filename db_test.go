package maple

import (
	"bytes"
	"github.com/satori/go.uuid"
	"testing"
)

func TestDbDispatcher(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger().ToWriter(&buf)
	dsp := NewMapleDb("sqlite3", "testdb", log)
	id := uuid.NewV4()
	dsp.NewWorkflow(id, &WorkflowSources{"wdl", "inputs", "options"}, log)
	wf := dsp.LoadWorkflow(id, log)
	if wf.uuid != id {
		t.Fatalf("Bad UUID")
	}
	if wf.status != "NotStarted" {
		t.Fatalf("Bad Status")
	}
}
