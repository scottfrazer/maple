package maple

import (
	"errors"
	"fmt"
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

type JobInstance struct {
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

func (ji *JobInstance) run(cmd *exec.Cmd, done chan<- *JobInstance, jobCtx context.Context) {
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

type WorkflowInstance struct {
	uuid       uuid.UUID
	primaryKey int64
	done       chan *WorkflowInstance
	source     *WorkflowSources
	status     string
	jobs       []*JobInstance
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

func (wi *WorkflowInstance) setStatus(status string) {
	// TODO: don't allow SetWorkflowStatus to modify the pointer,
	wi.db.SetWorkflowStatus(wi, status, wi.log)
	wi.status = status
}

func (wi *WorkflowInstance) run(workflowResultsChannel chan<- *WorkflowInstance, ctx context.Context) {
	var log = wi.log.ForWorkflow(wi.uuid)

	log.Info("run: enter")
	defer log.Info("run: exit")

	if wi.status == "NotStarted" {
		wi.setStatus("Started")
	}

	var jobDone = make(chan *JobInstance)
	var workflowDone = make(chan bool)
	var runnableJobs = make(chan *JobInstance)
	var isAborting = false
	var doneJobs = make(chan *JobInstance)

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
		wi.setStatus("Aborted")
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
					wi.setStatus("Done")
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
				wi.setStatus("Done")
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

type Kernel struct {
	log                *Logger
	db                 *MapleDb
	start              time.Time
	on                 bool
	maxWorkers         int
	submitChannel      chan *WorkflowInstance
	submitChannelMutex *sync.Mutex
	abortChannel       chan *WorkflowInstance
	running            map[*WorkflowInstance]struct{}
	cancel             func()
	waitGroup          *sync.WaitGroup
	workflowMaxRuntime time.Duration
}

func (kernel Kernel) run(ctx context.Context) {
	var workers = 0
	var isAborting = false
	var workflowDone = make(chan *WorkflowInstance)
	var log = kernel.log

	log.Info("kernel: enter")
	defer func() {
		kernel.waitGroup.Done()
		log.Info("kernel: exit")
	}()

	go func() {
		restartable, _ := kernel.db.GetWorkflowsByStatus(log, "Started")
		for _, wfCtx := range restartable {
			log.Info("kernel: resume workflow %s", wfCtx.uuid)
			kernel.enqueue(wfCtx, time.Minute)
		}
	}()

	var abort = func() {
		kernel.submitChannelMutex.Lock()
		close(kernel.submitChannel)
		kernel.on = false
		kernel.submitChannelMutex.Unlock()

		isAborting = true
		for wfCtx, _ := range kernel.running {
			wfCtx.cancel()
		}
	}

	var processDone = func(result *WorkflowInstance) {
		log.Info("kernel: workflow %s finished: %s", result.uuid, result.status)
		delete(kernel.running, result)
		workers--
	}

	for {
		if isAborting {
			if len(kernel.running) == 0 {
				return
			}
			select {
			case d := <-workflowDone:
				processDone(d)
			}
		} else if workers < kernel.maxWorkers {
			select {
			case wfContext := <-kernel.submitChannel:
				workers++
				kernel.running[wfContext] = struct{}{}
				subCtx, workflowCancel := context.WithTimeout(ctx, kernel.workflowMaxRuntime)
				wfContext.cancel = workflowCancel
				log.Info("kernel: starting %s", wfContext.uuid)
				go wfContext.run(workflowDone, subCtx)
			case abortUuid := <-kernel.abortChannel:
				for context, _ := range kernel.running {
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

func (kernel Kernel) signalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(kernel Kernel) {
		sig := <-sigs
		kernel.log.Info("%s signal detected... aborting dispatcher", sig)
		kernel.log.Info("%s signal detected... abort turned off", sig)
		//kernel.Abort()
		//kernel.log.Info("%s signal detected... aborted dispatcher", sig)
		os.Exit(130)
	}(kernel)
}

func (kernel *Kernel) enqueue(ctx *WorkflowInstance, timeout time.Duration) error {
	kernel.submitChannelMutex.Lock()
	defer kernel.submitChannelMutex.Unlock()
	if kernel.on == true {
		select {
		case kernel.submitChannel <- ctx:
		case <-time.After(timeout):
			return errors.New("Timeout submitting workflow")
		}
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	var wg sync.WaitGroup
	var mutex sync.Mutex
	// TODO: move DB creation into On()?  Semantics should be: zero DB connections on Off()
	db := NewMapleDb(dbName, dbConnection, log)
	kernel := Kernel{
		log:                log,
		db:                 db,
		start:              time.Now(),
		on:                 false,
		maxWorkers:         concurrentWorkflows,
		submitChannel:      make(chan *WorkflowInstance, submitQueueSize),
		submitChannelMutex: &mutex,
		abortChannel:       make(chan *WorkflowInstance),
		running:            make(map[*WorkflowInstance]struct{}),
		cancel:             func() {},
		waitGroup:          &wg,
		workflowMaxRuntime: time.Second * 600}
	return &kernel
}

func (kernel *Kernel) On() {
	if kernel.on {
		return
	}

	kernel.submitChannelMutex.Lock()
	defer kernel.submitChannelMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	kernel.cancel = cancel
	kernel.waitGroup.Add(1)
	go kernel.run(ctx)
	kernel.signalHandler() // TODO: make this part of kernel.run(), also make it shutdown on Off()
	kernel.on = true
}

func (kernel *Kernel) Off() {
	if !kernel.on {
		return
	}
	// TODO: call db.Close()
	kernel.cancel()
	kernel.waitGroup.Wait()
}

func (kernel *Kernel) Run(wdl, inputs, options string, id uuid.UUID) (*WorkflowInstance, error) {
	ctx, err := kernel.Submit(wdl, inputs, options, id, time.Hour)
	if err != nil {
		return nil, err
	}
	return <-ctx.done, nil
}

func (kernel *Kernel) Submit(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowInstance, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options), nil}
	log := kernel.log.ForWorkflow(id)
	ctx, err := kernel.db.NewWorkflow(id, &sources, log)
	if err != nil {
		return nil, err
	}
	kernel.enqueue(ctx, timeout)
	return ctx, nil
}

func (kernel *Kernel) Abort(id uuid.UUID, timeout time.Duration) error {
	for context, _ := range kernel.running {
		if context.uuid == id {
			timer := time.After(timeout)

			select {
			case kernel.abortChannel <- context:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout submitting workflow %s to be aborted", context.uuid))
			}

			select {
			case <-context.done:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout aborting workflow %s", context.uuid))
			}
		}
	}
	return nil
}

func (kernel *Kernel) AbortCall(id uuid.UUID, timeout time.Duration) error {
	return nil
}

func (kernel *Kernel) ListWorkflows() []uuid.UUID {
	return nil
}

func (kernel *Kernel) Uptime() time.Duration {
	return time.Since(kernel.start)
}
