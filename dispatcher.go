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

func GetHttpEndpoint(wd *WorkflowDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "this is a message")
	}
}

func (wd *WorkflowDispatcher) runJob(cmd *exec.Cmd, name string, done chan<- string, jobAbort <-chan bool) {
	var cmdDone = make(chan bool, 1)
	var log = wd.log
	var isAborting = false
	log.Info("runJob: start")
	defer log.Info("runJob: done")
	hack := make(chan int, 1)

	go func() {
		select {
		case <-time.After(time.Second * 2):
		case <-hack:
		}
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
			log.Info("runJob: finish with: %s", status)
			done <- fmt.Sprintf("%s:%s", name, status)
			log.Info("runJob: sent done message")
			return
		case _, ok := <-jobAbort:
			if !ok && !isAborting {
				log.Info("runJob: abort")
				hack <- 1
				isAborting = true
			}
		}
	}
	log.Info("runJob: done2")
}

func (wd *WorkflowDispatcher) runWorkflow(context *WorkflowContext, workflowResultsChannel chan<- *WorkflowContext, workflowAbortChannel <-chan bool) {
	var log = wd.log.ForWorkflow(context.uuid)

	log.Info("runWorkflow: start")
	<-DbSetStatusAsync(context, "Started")
	context.status = "Started"

	var jobDone = make(chan string)
	var jobAbort = make(map[string]chan bool)
	var jobAbortMutex sync.Mutex
	var workflowDone = make(chan bool)
	var workflowTimeout = time.After(wd.workflowMaxRuntime)
	var call_status = make(map[string]string)
	var calls = make(chan string, 20)
	var isAborting = false

	defer func() {
		context.done <- context
		close(context.done)
		workflowResultsChannel <- context
	}()

	var abortSubprocesses = func() {
		for _, v := range jobAbort {
			close(v)
		}
		<-DbSetStatusAsync(context, "Aborted")
		context.status = "Aborted"
		isAborting = true
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

			jobAbortMutex.Lock()
			delete(jobAbort, call)
			jobAbortMutex.Unlock()

			if len(call_status) == 8 || (isAborting && len(jobAbort) == 0) {
				workflowDone <- true
				return
			} else if !isAborting {
				//newWork := false
				for _, nextCall := range runnableDependents(call) {
					calls <- nextCall
					//newWork = true
				}

				/*log.Info("workflow: newWork=%t, len(jobAbort)=%d", newWork, len(jobAbort))
				if !newWork && len(jobAbort) == 0 {
					workflowDone <- true
					return
				}*/
			}
		}
	}()

	doneCalls <- "workflow"

	for {
		if isAborting {
			log.Info("isAborting state... receiving message")
			select {
			case <-workflowDone:
				log.Info("workflow: completed")
				if context.status != "Aborted" {
					context.status = "Done"
					<-DbSetStatusAsync(context, "Done")
				}
				return
			case status := <-jobDone:
				log.Info("workflow: subprocess finished: %s", status)
				var split = strings.Split(status, ":")
				var call = split[0]
				doneCalls <- call
			}
		} else {
			select {
			case <-workflowTimeout:
				log.Info("workflow: done (timeout %s)", wd.workflowMaxRuntime)
				abortSubprocesses()
			case call := <-calls:
				log.Info("workflow: launching call: %s", call)
				jobAbortMutex.Lock()
				jobAbort[call] = make(chan bool, 1)
				go wd.runJob(exec.Command("sleep", "2"), call, jobDone, jobAbort[call])
				jobAbortMutex.Unlock()
			case <-workflowDone:
				log.Info("workflow: completed")
				context.status = "Done"
				<-DbSetStatusAsync(context, "Done")
				return
			case status := <-jobDone:
				log.Info("workflow: subprocess finished: %s", status)
				var split = strings.Split(status, ":")
				var call = split[0]
				doneCalls <- call
			case _, ok := <-workflowAbortChannel:
				if !ok {
					log.Info("workflow: aborting...")
					abortSubprocesses()

					jobAbortMutex.Lock()
					if len(jobAbort) == 0 {
						jobAbortMutex.Unlock()
						return
					}
					jobAbortMutex.Unlock()
				}
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
	var log = wd.log

	log.Info("dispatcher: enter")
	defer func() {
		wd.waitGroup.Done()
		log.Info("dispatcher: exit")
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
		log.Info("dispatcher: workflow %s finished: %s", result.uuid, result.status)
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
				log.Info("dispatcher: starting %s", wfContext.uuid)
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

func NewWorkflowDispatcher(workers int, buffer int, log *Logger) *WorkflowDispatcher {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex

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
		log}

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
	wd.log.Info("--- Workflow Completed: %s (status %s)", id, wfStatus)
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
		wd.log.Info("%s signal detected... aborting dispatcher", sig)
		wd.Abort()
		wd.log.Info("%s signal detected... aborted dispatcher", sig)
		os.Exit(130)
	}(wd)
}

func main() {
	log := NewLogger("my.log")
	wd := NewWorkflowDispatcher(5000, 5000, log)
	StartDbDispatcher()
	SignalHandler(wd)

	if len(os.Args) < 2 {
		fmt.Println("usage: %s <run|restart>")
		os.Exit(1)
	}

	if os.Args[1] == "server" {
		log.Info("Listening on :8000 ...")
		http.HandleFunc("/submit", SubmitHttpEndpoint(wd))
		http.HandleFunc("/get", GetHttpEndpoint(wd))
		http.ListenAndServe(":8000", nil)
	}

	if os.Args[1] == "restart" {
		restartableWorkflows := <-DbGetByStatusAsync("Aborted", "NotStarted", "Started")
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
