package main

import (
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	"gopkg.in/alecthomas/kingpin.v2"
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

type WorkflowDispatcher struct {
	isAlive            bool
	maxWorkers         int
	submitChannel      chan *WorkflowContext
	submitChannelMutex *sync.Mutex
	abortChannel       chan bool
	waitGroup          *sync.WaitGroup
	db                 *DatabaseDispatcher
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
		ctx, err := wd.db.NewWorkflow(uuid, &sources, wd.log)
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "could not persist worflow"}`, r))
			return
		}

		wd.log.Info("HTTP endpoint /submit/ received: %s\n", sources)
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

func (wd *WorkflowDispatcher) runJob(context *WorkflowContext, cmd *exec.Cmd, name string, done chan<- string, jobAbort <-chan bool) {
	var cmdDone = make(chan bool, 1)
	var log = wd.log.ForWorkflow(context.uuid)
	var isAborting = false
	log.Info("runJob: start %s", name)
	defer log.Info("runJob: done %s", name)
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
			log.Info("runJob: %s finish with: %s", name, status)
			done <- fmt.Sprintf("%s:%s", name, status)
			return
		case _, ok := <-jobAbort:
			if !ok && !isAborting {
				log.Info("runJob: abort")
				hack <- 1
				isAborting = true
			}
		}
	}
}

func (wd *WorkflowDispatcher) runWorkflow(context *WorkflowContext, workflowResultsChannel chan<- *WorkflowContext, workflowAbortChannel <-chan bool) {
	var log = wd.log.ForWorkflow(context.uuid)

	log.Info("runWorkflow: start")
	wd.db.SetWorkflowStatus(context, "Started", log)
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
		wd.db.SetWorkflowStatus(context, "Aborted", log)
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
				for _, nextCall := range runnableDependents(call) {
					calls <- nextCall
				}
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
					wd.db.SetWorkflowStatus(context, "Done", log)
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
				go wd.runJob(context, exec.Command("sleep", "2"), call, jobDone, jobAbort[call])
				jobAbortMutex.Unlock()
			case <-workflowDone:
				log.Info("workflow: completed")
				context.status = "Done"
				wd.db.SetWorkflowStatus(context, "Done", log)
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
			log.Info("------------ accepting messages")
			select {
			case wfContext := <-wd.submitChannel:
				log.Info("------------ workflow submission")
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

func NewWorkflowDispatcher(workers int, buffer int, log *Logger, db *DatabaseDispatcher) *WorkflowDispatcher {
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
		db,
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

func (wd *WorkflowDispatcher) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID) (*WorkflowContext, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	log := wd.log.ForWorkflow(id)
	ctx, err := wd.db.NewWorkflow(id, &sources, log)
	if err != nil {
		return nil, err
	}
	wd.SubmitExistingWorkflow(ctx)
	return ctx, nil
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

type Engine struct {
	wd  *WorkflowDispatcher
	log *Logger
	db  *DatabaseDispatcher
}

func NewEngine(log *Logger, concurrentWorkflows int, submitQueueSize int) *Engine {
	db := NewDatabaseDispatcher("sqlite3", "DB", log)
	wd := NewWorkflowDispatcher(concurrentWorkflows, submitQueueSize, log, db)
	SignalHandler(wd)
	return &Engine{wd, log, db}
}

func (engine *Engine) RunWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	ctx, err := engine.wd.SubmitWorkflow(wdl, inputs, options, id)
	if err != nil {
		return nil
	}
	result := <-ctx.done
	wfStatus, err := engine.db.GetWorkflowStatus(ctx, engine.log)
	if err != nil {
		return nil
	}
	engine.log.Info("--- Workflow Completed: %s (status %s)", id, wfStatus)
	return result
}

func (engine *Engine) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	return nil
}

func (engine *Engine) AbortWorkflow(uuid uuid.UUID) error {
	return nil
}

func (engine *Engine) ListWorkflows() []uuid.UUID {
	return nil
}

func (engine *Engine) Shutdown() {
}

func main() {

	var (
		app          = kingpin.New("myapp", "A workflow engine")
		queueSize    = app.Flag("queue-size", "Submission queue size").Default("1000").Int()
		concurrentWf = app.Flag("concurrent-workflows", "Number of workflows").Default("1000").Int()
		restart      = app.Command("restart", "Restart workflows")
		run          = app.Command("run", "Run workflows")
		runN         = run.Arg("count", "Number of workflows").Required().Int()
		server       = app.Command("server", "Start HTTP server")
		test         = app.Command("test", "test")
	)

	args, err := app.Parse(os.Args[1:])
	log := NewLogger().ToFile("myapp.log").ToWriter(os.Stdout)
	engine := NewEngine(log, *concurrentWf, *queueSize)

	switch kingpin.MustParse(args, err) {
	case restart.FullCommand():
		restartableWorkflows, _ := engine.db.GetWorkflowsByStatus(log, "Aborted", "NotStarted", "Started")
		var restartWg sync.WaitGroup
		for _, restartableWfContext := range restartableWorkflows {
			fmt.Printf("restarting %s\n", restartableWfContext.uuid)
			restartWg.Add(1)
			go func(ctx *WorkflowContext) {
				engine.wd.SubmitExistingWorkflow(ctx)
				<-ctx.done
				restartWg.Done()
			}(restartableWfContext)
		}
		restartWg.Wait()
	case run.FullCommand():
		var wg sync.WaitGroup
		for i := 0; i < *runN; i++ {
			wg.Add(1)
			go func() {
				id := uuid.NewV4()
				log.Info("RunWorkflow start: %s\n", id)
				engine.RunWorkflow("wdl", "inputs", "options", id)
				log.Info("RunWorkflow exit: %s\n", id)
				wg.Done()
			}()
		}
		wg.Wait()
	case test.FullCommand():
		sources := WorkflowSources{"a", "b", "c"}
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := engine.db.NewWorkflow(uuid.NewV4(), &sources, engine.log)
				if err != nil {
					panic(err)
				}
			}()
		}
		fmt.Println("exit")
		wg.Wait()
	case server.FullCommand():
		log.Info("Listening on :8000 ...")
		http.HandleFunc("/submit", SubmitHttpEndpoint(engine.wd))
		http.ListenAndServe(":8000", nil)
	}

	engine.wd.Abort()
}
