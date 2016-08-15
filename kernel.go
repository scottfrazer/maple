package maple

import (
	"errors"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type WorkflowSources struct {
	wdl     string
	inputs  string
	options string
	_graph  *Graph
}

func (sources *WorkflowSources) graph() *Graph {
	if sources._graph != nil {
		return sources._graph
	}

	reader := strings.NewReader(sources.wdl)
	sources._graph = LoadGraph(reader)
	return sources._graph
}

type JobContext struct {
	primaryKey int64
	node       *Node
	shard      int
	attempt    int
	status     string
	cancel     func()
	wi         *WorkflowInstance
	db         *MapleDb
	log        *Logger
}

type WorkflowInstance struct {
	uuid       uuid.UUID
	primaryKey int64
	done       chan *WorkflowInstance
	source     *WorkflowSources
	status     string
	jobs       []*JobContext
	jobsMutex  *sync.Mutex
	cancel     func()
	db         *MapleDb
	log        *Logger
}

// TODO: this algorithm could use some work
func (ctx *WorkflowInstance) isTerminal(aborting bool) bool {
	ctx.jobsMutex.Lock()
	defer ctx.jobsMutex.Unlock()

	if !aborting && len(ctx.jobs) != len(ctx.source.graph().nodes) {
		return false
	}

	for _, job := range ctx.jobs {
		if job.status == "Started" || job.status == "NotStarted" {
			return false
		}
	}
	return true
}

type workflowDispatcher struct {
	isAlive            bool
	maxWorkers         int
	submitChannel      chan *WorkflowInstance
	submitChannelMutex *sync.Mutex
	abortChannel       chan *WorkflowInstance
	running            map[*WorkflowInstance]struct{}
	cancel             func()
	waitGroup          *sync.WaitGroup
	db                 *MapleDb
	workflowMaxRuntime time.Duration
	log                *Logger
}

func newWorkflowDispatcher(workers int, buffer int, log *Logger, db *MapleDb) *workflowDispatcher {
	var WaitGroup sync.WaitGroup
	var mutex sync.Mutex
	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())

	mutex.Lock()
	defer mutex.Unlock()

	wd := &workflowDispatcher{
		true,
		workers,
		make(chan *WorkflowInstance, buffer),
		&mutex,
		make(chan *WorkflowInstance),
		make(map[*WorkflowInstance]struct{}),
		dispatcherCancel,
		&WaitGroup,
		db,
		time.Second * 600,
		log}

	WaitGroup.Add(1)
	go wd.run(dispatcherCtx)
	return wd
}

func (ji *JobContext) run(cmd *exec.Cmd, done chan<- *JobContext, jobCtx context.Context) {
	var cmdDone = make(chan bool, 1)
	var isAborting = false
	var log = ji.log.ForJob(ji.wi.uuid, ji)
	log.Info("run: enter")
	defer log.Info("run: exit")
	subprocessCtx, subprocessCancel := context.WithCancel(jobCtx)

	if ji.status == "NotStarted" {
		// TODO: move this to ji.SetStatus()
		ji.db.SetJobStatus(ji, "Started", log)
	}

	go func() {
		select {
		case <-time.After(time.Second * 5):
		case <-subprocessCtx.Done():
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
			var status = "Done"
			/*if cmd.ProcessState == nil {
				status = "no-create"
			} else if !cmd.ProcessState.Success() {
				status = "failed"
			}*/
			log.Info("run: done (status %s)", status)
			ji.db.SetJobStatus(ji, status, log)
			done <- ji
			return
		case <-jobCtx.Done():
			if !isAborting {
				log.Info("run: abort")
				subprocessCancel()
				isAborting = true
			}
		}
	}
}

func (wi *WorkflowInstance) run(workflowResultsChannel chan<- *WorkflowInstance, ctx context.Context) {
	var log = wi.log.ForWorkflow(wi.uuid)

	log.Info("run: enter")
	defer log.Info("run: exit")

	if wi.status == "NotStarted" {
		// TODO: don't allow SetWorkflowStatus to modify the pointer,
		// instead implement  wi.SetStatus()
		// TODO: Also put db and log on the WorkflowInstance
		wi.db.SetWorkflowStatus(wi, "Started", log)
	}

	var jobDone = make(chan *JobContext)
	var workflowDone = make(chan bool)
	var runnableJobs = make(chan *JobContext)
	var isAborting = false
	var doneJobs = make(chan *JobContext)

	defer func() {
		wi.done <- wi
		close(wi.done)
		close(doneJobs)
		workflowResultsChannel <- wi
	}()

	var abortJobs = func() {
		wi.jobsMutex.Lock()
		defer wi.jobsMutex.Unlock()

		for _, job := range wi.jobs {
			job.cancel()
		}
		wi.db.SetWorkflowStatus(wi, "Aborted", log)
		isAborting = true
	}

	// TODO: pull this into a separate function wi.DoneJobsHandler
	go func() {
		graph := wi.source.graph()

		var launch = func(node *Node) {
			job, err := wi.db.NewJob(wi, node, log)
			if err != nil {
				// TODO: don't panic
				panic(err)
			}
			wi.jobsMutex.Lock()
			wi.jobs = append(wi.jobs, job)
			wi.jobsMutex.Unlock()
			runnableJobs <- job
		}

		if len(wi.jobs) > 0 {
			for _, job := range wi.jobs {
				if job.status == "Started" {
					runnableJobs <- job
				}
			}
		} else {
			for _, root := range graph.Root() {
				launch(root)
			}
		}

		for doneJob := range doneJobs {
			if wi.isTerminal(isAborting) {
				workflowDone <- true
				return
			} else if !isAborting {
				go func() {
					for _, nextNode := range graph.Downstream(doneJob.node) {
						launch(nextNode)
					}
				}()
			}
		}
	}()

	for {
		if isAborting {
			select {
			case <-workflowDone:
				log.Info("workflow: completed")
				if wi.status != "Aborted" {
					wi.db.SetWorkflowStatus(wi, "Done", log)
				}
				return
			case call := <-jobDone:
				log.Info("workflow: job finished: %s", call.status)
				doneJobs <- call
			}
		} else {
			select {
			case job := <-runnableJobs:
				log.Info("workflow: launching call: %s", job.node.String())
				jobCtx, cancel := context.WithCancel(context.Background())
				job.cancel = cancel
				go job.run(exec.Command("sleep", "2"), jobDone, jobCtx)
			case <-workflowDone:
				log.Info("workflow: completed")
				wi.db.SetWorkflowStatus(wi, "Done", log)
				return
			case call := <-jobDone:
				log.Info("workflow: job finished: %s", call.status)
				doneJobs <- call
			case <-ctx.Done():
				// this is for cancellations AND timeouts
				log.Info("workflow: aborting...")
				abortJobs()

				if wi.isTerminal(true) {
					return
				}
			}
		}
	}
}

func (wd *workflowDispatcher) run(ctx context.Context) {
	var workers = 0
	var isAborting = false
	var workflowDone = make(chan *WorkflowInstance)
	var log = wd.log

	log.Info("dispatcher: enter")
	defer func() {
		wd.waitGroup.Done()
		log.Info("dispatcher: exit")
	}()

	go func() {
		restartable, _ := wd.db.GetWorkflowsByStatus(log, "Started")
		for _, wfCtx := range restartable {
			log.Info("dispatcher: resume workflow %s", wfCtx.uuid)
			wd.SubmitExistingWorkflow(wfCtx, time.Minute)
		}
	}()

	var abort = func() {
		wd.submitChannelMutex.Lock()
		close(wd.submitChannel)
		wd.isAlive = false
		wd.submitChannelMutex.Unlock()

		isAborting = true
		for wfCtx, _ := range wd.running {
			wfCtx.cancel()
		}
	}

	var processDone = func(result *WorkflowInstance) {
		log.Info("dispatcher: workflow %s finished: %s", result.uuid, result.status)
		delete(wd.running, result)
		workers--
	}

	for {
		if isAborting {
			if len(wd.running) == 0 {
				return
			}
			select {
			case d := <-workflowDone:
				processDone(d)
			}
		} else if workers < wd.maxWorkers {
			select {
			case wfContext := <-wd.submitChannel:
				workers++
				wd.running[wfContext] = struct{}{}
				subCtx, workflowCancel := context.WithTimeout(ctx, wd.workflowMaxRuntime)
				wfContext.cancel = workflowCancel
				log.Info("dispatcher: starting %s", wfContext.uuid)
				go wfContext.run(workflowDone, subCtx)
			case abortUuid := <-wd.abortChannel:
				for context, _ := range wd.running {
					if context.uuid == abortUuid.uuid {
						context.cancel()
					}
				}
			case d := <-workflowDone:
				processDone(d)
			case <-ctx.Done():
				abort()
			}
		} else {
			select {
			case d := <-workflowDone:
				processDone(d)
			case <-ctx.Done():
				abort()
			}
		}
	}
}

func (wd *workflowDispatcher) Abort() {
	if !wd.isAlive {
		return
	}
	wd.cancel()
	wd.Wait()
}

func (wd *workflowDispatcher) Wait() {
	wd.waitGroup.Wait()
}

func (wd *workflowDispatcher) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowInstance, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options), nil}
	log := wd.log.ForWorkflow(id)
	ctx, err := wd.db.NewWorkflow(id, &sources, log)
	if err != nil {
		return nil, err
	}
	wd.SubmitExistingWorkflow(ctx, timeout)
	return ctx, nil
}

func (wd *workflowDispatcher) SubmitExistingWorkflow(ctx *WorkflowInstance, timeout time.Duration) error {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	if wd.isAlive == true {
		select {
		case wd.submitChannel <- ctx:
		case <-time.After(timeout):
			return errors.New("Timeout submitting workflow")
		}
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (wd *workflowDispatcher) AbortWorkflow(id uuid.UUID) {
	for context, _ := range wd.running {
		if context.uuid == id {
			wd.abortChannel <- context
			<-context.done
		}
	}
}

func (wd *workflowDispatcher) SignalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(wd *workflowDispatcher) {
		sig := <-sigs
		wd.log.Info("%s signal detected... aborting dispatcher", sig)
		wd.log.Info("%s signal detected... abort turned off", sig)
		//wd.Abort()
		//wd.log.Info("%s signal detected... aborted dispatcher", sig)
		os.Exit(130)
	}(wd)
}

type Kernel struct {
	wd    *workflowDispatcher
	log   *Logger
	db    *MapleDb
	start time.Time
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	db := NewMapleDb(dbName, dbConnection, log)
	wd := newWorkflowDispatcher(concurrentWorkflows, submitQueueSize, log, db)
	wd.SignalHandler()
	return &Kernel{wd, log, db, time.Now()}
}

func (kernel *Kernel) RunWorkflow(wdl, inputs, options string, id uuid.UUID) (*WorkflowInstance, error) {
	ctx, err := kernel.wd.SubmitWorkflow(wdl, inputs, options, id, time.Hour)
	if err != nil {
		return nil, err
	}
	return <-ctx.done, nil
}

func (kernel *Kernel) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowInstance, error) {
	ctx, err := kernel.wd.SubmitWorkflow(wdl, inputs, options, id, timeout)
	if err != nil {
		return nil, err
	}
	return ctx, nil
}

func (kernel *Kernel) AbortWorkflow(id uuid.UUID) error {
	kernel.wd.AbortWorkflow(id)
	return nil
}

func (kernel *Kernel) ListWorkflows() []uuid.UUID {
	return nil
}

func (kernel *Kernel) Uptime() time.Duration {
	return time.Since(kernel.start)
}

func (kernel *Kernel) Shutdown() {
	// Shutdown dispatcher, call db.Close()
}
