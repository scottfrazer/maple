package main

import (
	//"fmt"
	"github.com/satori/go.uuid"
	"testing"
	//"time"
)

func Make() {

}

func TestStartDispatcher(t *testing.T) {
	wd := NewWorkflowDispatcher(1, 1)
	if !wd.IsAlive() {
		t.Fatalf("Expecting the dispatcher to be alive after starting it")
	}
}

func TestRunWorkflow(t *testing.T) {
	StartDbDispatcher()
	wd := NewWorkflowDispatcher(1, 1)
	context := wd.RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
	if context.status != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}
}
