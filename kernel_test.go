package maple

import (
	"bytes"
	"fmt"
	"github.com/satori/go.uuid"
	"testing"
)

func TestRunWorkflow(t *testing.T) {
	var buf bytes.Buffer
	uuid := uuid.NewV4()
	kernel := NewKernel(NewLogger().ToWriter(&buf), "sqlite3", "testDB", 1, 1)
	kernel.On()
	wi, err := kernel.Run("[1]A[2]\n[A]B[3,4]", "inputs", "options", uuid)

	if err != nil {
		t.Fatalf("Got error trying to run workflow: %s", err)
	}

	if wi.Status() != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

	fmt.Printf(buf.String())
}
