package main

import (
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type WorkflowIdentifier interface {
	dbKey() int64
	id() uuid.UUID
}

type WorkflowSources struct {
	wdl     string
	inputs  string
	options string
}

type CallStatus struct {
	call    string
	index   int
	attempt int
	status  string
}

type WorkflowContext struct {
	uuid       uuid.UUID
	primaryKey int64
	done       chan *WorkflowContext
	source     *WorkflowSources
	status     string
	callStatus *[]CallStatus
}

func (c WorkflowContext) id() uuid.UUID {
	return c.uuid
}

func (c WorkflowContext) dbKey() int64 {
	return c.primaryKey
}

func (s WorkflowSources) String() string {
	return fmt.Sprintf("<workflow %s>", s.wdl)
}

var SubmissionChannel chan *WorkflowContext
var WorkflowMaxRunTime = time.Second * 30
var dispatcherWaitGroup sync.WaitGroup
var dispatcherAbortChannel chan bool
var isDispatcherAlive = false

func HttpEndpoint(w http.ResponseWriter, r *http.Request) {
	fp, _, err := r.FormFile("wdl")
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"message": "no WDL file"}`)
		return
	}

	var bytes, _ = ioutil.ReadAll(fp)
	wdl := string(bytes)

	fp, _, err = r.FormFile("inputs")
	var inputs = "{}"
	if err != nil {
		bytes, _ = ioutil.ReadAll(fp)
		inputs = string(bytes)
	}

	fp, _, err = r.FormFile("options")
	var options = "{}"
	if err != nil {
		bytes, _ = ioutil.ReadAll(fp)
		options = string(bytes)
	}

	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	uuid := uuid.NewV4()
	ctx := <-DbNewWorkflowAsync(uuid, &sources)

	fmt.Printf("HTTP endpoint /submit/ received: %s\n", sources)
	defer func() {
		if r := recover(); r != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/submit/ panic: %s"}`, r))
		}
	}()

	select {
	case SubmissionChannel <- ctx:
	case <-time.After(time.Millisecond * 500):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusRequestTimeout)
		io.WriteString(w, `{"message": "timeout submitting workflow (500ms)"}`)
		return
	}
}

func subprocess(cmd *exec.Cmd, name string, done chan<- string, subprocessAbort <-chan bool, wg *sync.WaitGroup) {
	var cmdDone = make(chan bool, 1)
	defer wg.Done()

	go func() {
		<-time.After(time.Second * 2)
		//cmd.Run()
		cmdDone <- true
	}()

	/*var kill = func(status string) {
		err := cmd.Process.Kill()
		if err != nil {
			panic(err)
		}
	}*/

	for {
		select {
		case <-cmdDone:
			var status = "done"
			if cmd.ProcessState == nil {
				status = "no-create"
			} else if !cmd.ProcessState.Success() {
				status = "failed"
			}
			fmt.Printf("subprocess: finish with: %s\n", status)
			done <- fmt.Sprintf("%s:%s", name, status)
			return
		case _, ok := <-subprocessAbort:
			if !ok {
				fmt.Printf("subprocess: abort signal received\n")
				return
			}
		}
	}
}

func workflowWorker(context *WorkflowContext, done chan<- *WorkflowContext, wg *sync.WaitGroup, abortChannel <-chan bool) {
	fmt.Printf("workflow %s: start\n", context.uuid)
	<-DbSetStatusAsync(context, "Started")
	context.status = "Started"

	var subprocessDone = make(chan string)
	var workflowDone = make(chan bool)
	var subprocessAbort = make(map[string]chan bool)
	var subprocessWaitGroup sync.WaitGroup
	var call_status = make(map[string]string)
	var calls = make(chan string, 20)
	var timeout = time.After(WorkflowMaxRunTime)

	defer func() {
		context.done <- context
		close(context.done)
		done <- context
		subprocessWaitGroup.Wait()
		wg.Done()
		fmt.Printf("workflow %s: end\n", context.uuid)
	}()

	var abortSubprocesses = func() {
		for _, v := range subprocessAbort {
			close(v)
		}
		subprocessWaitGroup.Wait()
		<-DbSetStatusAsync(context, "Aborted")
		context.status = "Aborted"
	}

	var doneCalls = make(chan string)
	var runnableDependents = func(c string) []string {
		if c == "workflow" {
			return []string{"A", "B", "C", "D"}
		} else if c == "D" {
			return []string{"E", "F"}
		} else if c == "F" {
			return []string{"G"}
		}
		return []string{}
	}

	go func() {
		for call := range doneCalls {
			call_status[call] = "Done"
			if len(call_status) == 8 {
				workflowDone <- true
				return
			} else {
				for _, nextCall := range runnableDependents(call) {
					calls <- nextCall
				}
			}
		}
	}()

	doneCalls <- "workflow"

	for {
		select {
		case <-timeout:
			fmt.Printf("workflow %s: done (timeout %s)\n", context.uuid, WorkflowMaxRunTime)
			abortSubprocesses()
			return
		case call := <-calls:
			fmt.Printf("workflow %s: launching call: %s\n", context.uuid, call)
			subprocessAbort[call] = make(chan bool, 1)
			subprocessWaitGroup.Add(1)
			go subprocess(exec.Command("sleep", "2"), call, subprocessDone, subprocessAbort[call], &subprocessWaitGroup)
		case <-workflowDone:
			var m = make(map[string]int)
			for _, v := range call_status {
				m[v] += 1
			}
			var a = make([]string, len(m))
			var i = 0
			for k, v := range m {
				a[i] = fmt.Sprintf("%s=%d", k, v)
				i++
			}
			context.status = strings.Join(a, ",")
			<-DbSetStatusAsync(context, "Done")
			return
		case status := <-subprocessDone:
			fmt.Printf("workflow %s: subprocess finished: %s\n", context.uuid, status)
			var split = strings.Split(status, ":")
			var call = split[0]
			var sts = split[1]
			doneCalls <- call
			call_status[call] = sts
		case _, ok := <-abortChannel:
			if !ok {
				fmt.Printf("workflow %s: aborting...\n", context.uuid)
				abortSubprocesses()
				return
			}
		}
	}
}

func workflowDispatcher(max int) {
	var workers = 0
	var done = make(chan *WorkflowContext)
	var workflowAbort = make(map[string]chan bool)
	var wg sync.WaitGroup

	fmt.Printf("dispatcher: enter\n")
	defer func() {
		dispatcherWaitGroup.Done()
		fmt.Printf("dispatcher: exit\n")
	}()

	var abortWorkflows = func() {
		fmt.Println("dispatcher: abort signal received...")
		close(SubmissionChannel)
		for _, v := range workflowAbort {
			close(v)
		}
		wg.Wait()
		fmt.Println("dispatcher: aborted")
	}

	var processDone = func(result *WorkflowContext) {
		fmt.Printf("dispatcher: workflow %s finished: %s\n", result.uuid, result.status)
		<-DbSetStatusAsync(result, result.status)
		delete(workflowAbort, fmt.Sprintf("%s", result.uuid))
		workers--
	}

	for {
		if workers < max {
			select {
			case wf := <-SubmissionChannel:
				// wf can be a new workflow, or a workflow in progress (restart)
				workers++
				wg.Add(1)
				fmt.Printf("dispatcher: starting workflow %s [%d used, %d max, %d queued]\n", wf.uuid, workers, max, len(SubmissionChannel))
				abortChannel := make(chan bool, 1)
				workflowAbort[fmt.Sprintf("%s", wf.uuid)] = abortChannel
				go workflowWorker(wf, done, &wg, abortChannel)
			case d := <-done:
				processDone(d)
			case _, ok := <-dispatcherAbortChannel:
				if !ok {
					abortWorkflows()
					return
				}
			}
		} else {
			select {
			case d := <-done:
				processDone(d)
			case _, ok := <-dispatcherAbortChannel:
				if !ok {
					abortWorkflows()
					return
				}
			}
		}
	}
}

func StartDispatcher(workers int, buffer int) {
	if !isDispatcherAlive {
		SubmissionChannel = make(chan *WorkflowContext, buffer)
		dispatcherAbortChannel = make(chan bool)
		dispatcherWaitGroup.Add(1)
		go workflowDispatcher(workers)
		isDispatcherAlive = true
	} else {
		fmt.Println("Dispatcher already started")
	}
}

func AbortDispatcher() {
	close(dispatcherAbortChannel)
	dispatcherWaitGroup.Wait()
	isDispatcherAlive = false
}

func IsAlive() bool {
	return isDispatcherAlive
}

func RunWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	// TODO: duplicated

	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	uuid := uuid.NewV4()
	ctx := <-DbNewWorkflowAsync(uuid, &sources)
	SubmissionChannel <- ctx
	result := <-ctx.done

	wfStatus := <-DbGetStatusAsync(ctx)
	fmt.Printf("--- Workflow Completed: %s (status %s)\n", id, wfStatus)
	return result
}

func AbortWorkflow(id uuid.UUID) {
	return
}

func SignalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		fmt.Printf("%s signal detected!  Aborting dispatcher...\n", sig)
		AbortDispatcher()
		fmt.Printf("%s signal detected!  Aborted.\n", sig)
		os.Exit(130)
	}()
}

func main() {
	StartDispatcher(1000, 4000)
	StartDbDispatcher()
	SignalHandler()

	abortedWorkflows := <-DbGetByStatusAsync("Aborted")
	var restartWg sync.WaitGroup
	for _, abortedWfContext := range abortedWorkflows {
		fmt.Printf("restarting %s %d\n", abortedWfContext, len(abortedWorkflows))
		restartWg.Add(1)
		go func(ctx *WorkflowContext) {
			SubmissionChannel <- ctx
			<-ctx.done
			restartWg.Done()
		}(abortedWfContext)
	}
	restartWg.Wait()

	var wg sync.WaitGroup
	for i := 0; i < 0; i++ {
		wg.Add(1)
		go func() {
			RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
			wg.Done()
		}()
		time.Sleep(time.Millisecond * 10)
	}
	wg.Wait()

	//fmt.Println("Listening on :8000 ...")
	//http.HandleFunc("/submit", HttpEndpoint)
	//http.ListenAndServe(":8000", nil)
}
