package main

import (
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type WorkflowDispatcher struct {
	isAlive            bool
	maxWorkers         int
	submitChannel      chan *WorkflowContext
	submitChannelMutex *sync.Mutex
	abortChannel       chan bool
	waitGroup          *sync.WaitGroup
	workflowMaxRuntime time.Duration
	log                *Logger
}

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

func SubmitHttpEndpoint(wd *WorkflowDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		case wd.submitChannel <- ctx:
		case <-time.After(time.Millisecond * 500):
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusRequestTimeout)
			io.WriteString(w, `{"message": "timeout submitting workflow (500ms)"}`)
			return
		}
	}
}

func runJob(cmd *exec.Cmd, name string, done chan<- string, jobAbort <-chan bool, wg *sync.WaitGroup) {
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
			/*if cmd.ProcessState == nil {
				status = "no-create"
			} else if !cmd.ProcessState.Success() {
				status = "failed"
			}*/
			fmt.Printf("subprocess: finish with: %s\n", status)
			done <- fmt.Sprintf("%s:%s", name, status)
			return
		case _, ok := <-jobAbort:
			if !ok {
				//fmt.Printf("subprocess: abort signal received\n")
				return
			}
		}
	}
}

func (wd *WorkflowDispatcher) runWorkflow(context *WorkflowContext, workflowResultsChannel chan<- *WorkflowContext, workflowAbortChannel <-chan bool) {
	fmt.Printf("workflow %s: start\n", context.uuid)
	<-DbSetStatusAsync(context, "Started")
	context.status = "Started"

	var jobDone = make(chan string)
	var jobAbort = make(map[string]chan bool)
	var jobWaitGroup sync.WaitGroup
	var workflowDone = make(chan bool)
	var workflowTimeout = time.After(wd.workflowMaxRuntime)
	var call_status = make(map[string]string)
	var calls = make(chan string, 20)

	defer func() {
		context.done <- context
		close(context.done)
		workflowResultsChannel <- context
		jobWaitGroup.Wait()
	}()

	var abortSubprocesses = func() {
		for _, v := range jobAbort {
			close(v)
		}
		jobWaitGroup.Wait()
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
		case <-workflowTimeout:
			fmt.Printf("workflow %s: done (timeout %s)\n", context.uuid, wd.workflowMaxRuntime)
			abortSubprocesses()
			return
		case call := <-calls:
			fmt.Printf("workflow %s: launching call: %s\n", context.uuid, call)
			jobAbort[call] = make(chan bool, 1)
			jobWaitGroup.Add(1)
			go runJob(exec.Command("sleep", "2"), call, jobDone, jobAbort[call], &jobWaitGroup)
		case <-workflowDone:
			context.status = "Done"
			<-DbSetStatusAsync(context, "Done")
			return
		case status := <-jobDone:
			fmt.Printf("workflow %s: subprocess finished: %s\n", context.uuid, status)
			var split = strings.Split(status, ":")
			var call = split[0]
			doneCalls <- call
		case _, ok := <-workflowAbortChannel:
			if !ok {
				fmt.Printf("workflow %s: aborting...\n", context.uuid)
				abortSubprocesses()
				return
			}
		}
	}
}

func (wd *WorkflowDispatcher) runDispatcher() {
	var workers = 0
	var isAborting = false
	var workflowDone = make(chan *WorkflowContext)
	var workflowAbort = make(map[string]chan bool)
	var runningWorkflows = make(map[string]*WorkflowContext)

	fmt.Printf("dispatcher: enter\n")
	defer func() {
		wd.waitGroup.Done()
		fmt.Printf("dispatcher: exit\n")
	}()

	var abort = func() {
		wd.submitChannelMutex.Lock()
		close(wd.submitChannel)
		wd.isAlive = false
		wd.submitChannelMutex.Unlock()

		isAborting = true
		for _, v := range workflowAbort {
			close(v)
		}
	}

	var processDone = func(result *WorkflowContext) {
		fmt.Printf("dispatcher: workflow %s finished: %s\n", result.uuid, result.status)
		delete(workflowAbort, fmt.Sprintf("%s", result.uuid))
		delete(runningWorkflows, fmt.Sprintf("%s", result.uuid))
		workers--
	}

	for {
		if isAborting {
			if len(runningWorkflows) == 0 {
				return
			}
			select {
			case d := <-workflowDone:
				processDone(d)
			}
		} else if workers < wd.maxWorkers {
			select {
			case wfContext := <-wd.submitChannel:
				// wf can be a new workflow, or a workflow in progress (restart)
				workers++
				runningWorkflows[fmt.Sprintf("%s", wfContext.uuid)] = wfContext
				workflowAbortChannel := make(chan bool, 1)
				workflowAbort[fmt.Sprintf("%s", wfContext.uuid)] = workflowAbortChannel
				fmt.Printf("dispatcher: starting %s\n", wfContext.uuid)
				go wd.runWorkflow(wfContext, workflowDone, workflowAbortChannel)
			case d := <-workflowDone:
				processDone(d)
			case _, ok := <-wd.abortChannel:
				if !ok {
					abort()
				}
			}
		} else {
			select {
			case d := <-workflowDone:
				processDone(d)
			case _, ok := <-wd.abortChannel:
				if !ok {
					abort()
				}
			}
		}
	}
}

func NewDispatcher(workers int, buffer int) *WorkflowDispatcher {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	logger := NewLogger("my.log", 1000)

	mutex.Lock()
	defer mutex.Unlock()

	wd := &WorkflowDispatcher{
		true,
		workers,
		make(chan *WorkflowContext, buffer),
		&mutex,
		make(chan bool),
		&waitGroup,
		time.Second * 30,
		logger}

	waitGroup.Add(1)
	go wd.runDispatcher()
	return wd
}

func (wd *WorkflowDispatcher) Abort() {
	if !wd.isAlive {
		return
	}
	close(wd.abortChannel)
	wd.Wait()
}

func (wd *WorkflowDispatcher) Wait() {
	wd.waitGroup.Wait()
}

func (wd *WorkflowDispatcher) IsAlive() bool {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	return wd.isAlive
}

func (wd *WorkflowDispatcher) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	uuid := uuid.NewV4()
	ctx := <-DbNewWorkflowAsync(uuid, &sources)
	wd.SubmitExistingWorkflow(ctx)
	return ctx
}

func (wd *WorkflowDispatcher) SubmitExistingWorkflow(ctx *WorkflowContext) error {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	if wd.isAlive == true {
		wd.submitChannel <- ctx
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (wd *WorkflowDispatcher) RunWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	ctx := wd.SubmitWorkflow(wdl, inputs, options, id)
	if ctx == nil {
		return nil
	}
	result := <-ctx.done
	wfStatus := <-DbGetStatusAsync(ctx)
	fmt.Printf("--- Workflow Completed: %s (status %s)\n", id, wfStatus)
	return result
}

func (wd *WorkflowDispatcher) AbortWorkflow(id uuid.UUID) {
	return
}

func SignalHandler(wd *WorkflowDispatcher) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(wd *WorkflowDispatcher) {
		sig := <-sigs
		fmt.Printf("%s signal detected... aborting dispatcher\n", sig)
		wd.Abort()
		fmt.Printf("%s signal detected... aborted dispatcher\n", sig)
		os.Exit(130)
	}(wd)
}

func main() {
	wd := NewDispatcher(5000, 5000)
	StartDbDispatcher()
	SignalHandler(wd)

	/*go func() {
		fmt.Println("Listening on :8000 ...")
		http.HandleFunc("/submit", HttpEndpoint)
		http.ListenAndServe(":8000", nil)
	}()*/

	if len(os.Args) < 2 {
		fmt.Println("usage: %s <run|restart>")
		os.Exit(1)
	}

	if os.Args[1] == "restart" {
		restartableWorkflows := <-DbGetByStatusAsync("Aborted", "NotStarted")
		var restartWg sync.WaitGroup
		for _, restartableWfContext := range restartableWorkflows {
			fmt.Printf("restarting %s\n", restartableWfContext.uuid)
			restartWg.Add(1)
			go func(ctx *WorkflowContext) {
				wd.SubmitExistingWorkflow(ctx)
				<-ctx.done
				restartWg.Done()
			}(restartableWfContext)
		}
		restartWg.Wait()
	}

	if os.Args[1] == "run" {
		if len(os.Args) < 3 {
			fmt.Printf("usage: %s run <job_count>\n", os.Args[0])
			os.Exit(1)
		}
		var wg sync.WaitGroup
		var jobCount, _ = strconv.ParseInt(os.Args[2], 10, 64)
		for i := int64(0); i < jobCount; i++ {
			wg.Add(1)
			go func() {
				wd.RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
				wg.Done()
			}()
		}
		wg.Wait()
	}

	wd.Abort()
}
