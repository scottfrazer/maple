package maple

import (
	"fmt"
	"github.com/satori/go.uuid"
	"sort"
	"strings"
	"sync"
	"testing"
)

type JobStatusByDate []*JobStatusEntry

func (a JobStatusByDate) Len() int           { return len(a) }
func (a JobStatusByDate) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a JobStatusByDate) Less(i, j int) bool { return a[i].date.Before(a[j].date) }

type WorkflowStatusByDate []*WorkflowStatusEntry

func (a WorkflowStatusByDate) Len() int           { return len(a) }
func (a WorkflowStatusByDate) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a WorkflowStatusByDate) Less(i, j int) bool { return a[i].date.Before(a[j].date) }

func sortedWorkflowStatuses(entry *WorkflowEntry) []string {
	sort.Sort(WorkflowStatusByDate(entry.statusEntries))
	statuses := make([]string, len(entry.statusEntries))
	for index, entry := range entry.statusEntries {
		statuses[index] = entry.status
	}
	return statuses
}

func sortedJobStatuses(entry *JobEntry) []string {
	sort.Sort(JobStatusByDate(entry.statusEntries))
	statuses := make([]string, len(entry.statusEntries))
	for index, entry := range entry.statusEntries {
		statuses[index] = entry.status
	}
	return statuses
}

func sortedJobTags(entry *WorkflowEntry) []string {
	tags := make([]string, len(entry.jobs))
	for index, entry := range entry.jobs {
		tags[index] = entry.Tag()
	}
	sort.Sort(sort.StringSlice(tags))
	return tags
}

func assertStatuses(t *testing.T, actual []string, tag string, expected ...string) {
	actualStr := strings.Join(actual, " -> ")
	expectedStr := strings.Join(expected, " -> ")
	if actualStr != expectedStr {
		t.Fatalf("%s: Expected statuses [%s] Got [%s]", tag, expectedStr, actualStr)
	}
}

func assertValidUuid(t *testing.T, validUuids []string, id string) {
	for _, x := range validUuids {
		if id == x {
			return
		}
	}
	t.Fatalf("UUID %s was not among list of UUIDs submitted", id)
}

func TestGetByStatus(t *testing.T) {
	graph := "[1]A[2]\n[1]B[3,4]\n[1]C[2]\n[1]D[1]\n[D,1]E[]\n[D]F[0]\n[F]G[]\n[G]H[]\n[G]I[]\n[G]J[]\n[G]K[]\n[G]L[]"
	_, _, kernel := makeTestKernel(t)
	var uuids []string

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		id := uuid.NewV4()
		uuids = append(uuids, id.String())
		go func(id uuid.UUID) {
			defer wg.Done()
			_, err := kernel.Run(graph, "inputs", "options", "testbackend", id)
			if err != nil {
				t.Fatalf("fatal: %s", err)
			}
		}(id)
	}
	wg.Wait()

	wfs, err := kernel.db.LoadWorkflowsByStatus(kernel.log, "Started", "NotStarted", "Done", "Aborted")
	if err != nil {
		panic(err)
	}

	for _, wf := range wfs {
		assertStatuses(t, sortedWorkflowStatuses(wf), fmt.Sprintf("Workflow %s", wf.uuid.String()[:8]), "NotStarted", "Started", "Done")
		assertValidUuid(t, uuids, wf.uuid.String())
		if wf.backend != "testbackend" {
			t.Fatalf("Expecting backend to be 'testbackend'")
		}
		if wf.sources.wdl != graph {
			t.Fatalf("Expecting WDL to be %s", graph)
		}
		if len(wf.jobs) != 12 {
			t.Fatalf("Expecting there to be 12 jobs, found %d", len(wf.jobs))
		}
		assertStatuses(
			t,
			sortedJobTags(wf),
			fmt.Sprintf("Workflow %s", wf.uuid.String()[:8]),
			"A:0:1", "B:0:1", "C:0:1", "D:0:1", "E:0:1", "F:0:1", "G:0:1", "H:0:1", "I:0:1", "J:0:1", "K:0:1", "L:0:1")
		for _, job := range wf.jobs {
			assertStatuses(t, sortedJobStatuses(job), fmt.Sprintf("Job %s:%s", wf.uuid.String()[:8], job.fqn), "NotStarted", "Started", "Done")
		}
	}
}
