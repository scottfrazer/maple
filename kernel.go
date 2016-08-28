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

type JobInstance struct {
	entry  *JobEntry
	cancel func()
	abort  func()
	wi     *WorkflowInstance
	db     *MapleDb
	log    *Logger
}

func (ji *JobInstance) status() string {
	return ji.entry.LatestStatusEntry().status
}

func (ji *JobInstance) node() *Node {
	return ji.wi.Graph().Find(ji.entry.fqn)
}

func (ji *JobInstance) setStatus(status string) error {
	if status == ji.status() {
		ji.log.Info("warning: already has status %s", status)
		return nil
	}
	ji.log.Info("status change %s -> %s", ji.status(), status)
	statusEntry, err := ji.db.NewJobStatusEntry(ji.entry.primaryKey, status, time.Now(), ji.log)
	if err != nil {
		return err
	}
	ji.entry.statusEntries = append(ji.entry.statusEntries, statusEntry)
	return nil
}

func (ji *JobInstance) getStatus() string {
	return ji.entry.LatestStatusEntry().status
}

func (ji *JobInstance) run(backend Backend, cmd *exec.Cmd, done chan<- *JobInstance, ctx context.Context, abortCtx context.Context) {
	var backendJobDone = make(chan bool, 1)
	log := ji.log
	log.Info("run: enter")
	defer log.Info("run: exit")
	backendJobCtx, backendJobCancel := context.WithCancel(ctx)
	backendJobAbortCtx, backendJobAbort := context.WithCancel(context.Background())

	ji.setStatus("Started")

	backend.Submit(ji, backendJobDone, backendJobCtx, backendJobAbortCtx)

	for {
		select {
		case <-backendJobDone:
			ji.setStatus("Done")
			done <- ji
			return
		case <-ctx.Done():
			log.Info("run: shutdown...")
			backendJobCancel()
			<-backendJobDone
			done <- ji
			return
		case <-abortCtx.Done():
			log.Info("run: abort...")
			backendJobAbort()
			<-backendJobDone
			ji.setStatus("Aborted")
			done <- ji
			return
		}
	}
}

type WorkflowInstance struct {
	entry        *WorkflowEntry
	done         chan *WorkflowInstance
	runnableJobs chan *JobInstance
	doneJobs     chan *JobInstance
	running      map[*JobInstance]struct{}
	jobsMutex    *sync.Mutex
	cancel       func()
	abort        func()
	shuttingDown bool
	aborting     bool
	backend      Backend
	db           *MapleDb
	log          *Logger
	start        time.Time // TODO: eventually move this to the entry (and DB)
	_graph       *Graph
}

func (wi *WorkflowInstance) Uuid() uuid.UUID {
	return wi.entry.uuid
}

func (wi *WorkflowInstance) Status() string {
	return wi.entry.LatestStatusEntry().status
}

func (wi *WorkflowInstance) Abort() {
	wi.aborting = true
	wi.abort()
}

func (wi *WorkflowInstance) Graph() *Graph {
	if wi._graph == nil {
		reader := strings.NewReader(wi.entry.sources.wdl)
		wi._graph = LoadGraph(reader)
	}
	return wi._graph
}

func (wi *WorkflowInstance) isTerminal() *string {
	// A job is terminal if everything that could be run has been run
	status := "Done"

	for _, jobEntry := range wi.entry.jobs {
		jobStatus := jobEntry.LatestStatusEntry().status
		if jobStatus == "Failed" {
			status = "Failed"
		}
		if jobStatus == "Started" || jobStatus == "NotStarted" || jobStatus == "Aborted" {
			return nil
		}
	}
	return &status
}

func (wi *WorkflowInstance) setStatus(status string) error {
	if status == wi.Status() {
		wi.log.Info("warning: already has status %s", status)
		return nil
	}
	wi.log.Info("status change %s -> %s", wi.Status(), status)
	statusEntry, err := wi.db.NewWorkflowStatusEntry(wi.entry.primaryKey, status, time.Now(), wi.log)
	if err != nil {
		return err
	}
	wi.entry.statusEntries = append(wi.entry.statusEntries, statusEntry)
	return nil
}

func (wi *WorkflowInstance) newJobInstance(node *Node) (*JobInstance, error) {
	entry, err := wi.db.NewJobEntry(wi.entry.primaryKey, node.name, 0, 1, wi.log)
	if err != nil {
		return nil, err
	}

	return wi.jobInstanceFromJobEntry(entry), nil
}

func (wi *WorkflowInstance) jobInstanceFromJobEntry(entry *JobEntry) *JobInstance {
	return &JobInstance{
		entry:  entry,
		cancel: func() {},
		abort:  func() {},
		wi:     wi,
		db:     wi.db,
		log:    wi.log.ForJob(wi.Uuid(), entry.fqn)}
}

func (wi *WorkflowInstance) isAcceptingJobs() bool {
	return wi.shuttingDown == false && wi.aborting == false
}

func (wi *WorkflowInstance) checkWorkflowTerminal(workflowDone chan<- string, ctx context.Context) bool {
	terminalStatus := wi.isTerminal()
	if terminalStatus != nil {
		wi.setStatus(*terminalStatus)
		go func(ctx context.Context) {
			select {
			case workflowDone <- *terminalStatus:
			case <-ctx.Done():
			}
		}(ctx)
		return true
	}
	return false
}

func (wi *WorkflowInstance) initJobs(workflowDone chan<- string, ctx context.Context) {
	// If workflow has not been started yet, launch the root nodes
	if len(wi.entry.jobs) == 0 {
		wi.persistAndLaunch(wi.Graph().Root(), ctx)
		return
	}

	// Otherwise, this is a workflow in progress.  Re-launch everything that's 'started'
	var newNodes []*Node
	subCtx, _ := context.WithCancel(ctx)
	wi.checkWorkflowTerminal(workflowDone, subCtx)

	for _, jobEntry := range wi.entry.jobs {
		latestStatusEntry := jobEntry.LatestStatusEntry()
		switch latestStatusEntry.status {
		case "NotStarted":
			fallthrough
		case "Started":
			ji := wi.jobInstanceFromJobEntry(jobEntry)
			subCtx, _ = context.WithCancel(ctx)
			wi.launch(ji, subCtx)
		case "Done":
			node := wi.Graph().Find(jobEntry.fqn)
			if node == nil {
				panic("cannot find node") // TODO
			}

			nodes := wi.Graph().Downstream(node)
			for _, node := range nodes {
				_, err := wi.db.LoadJobEntry(wi.log, wi.entry.primaryKey, node.name, 0, 1)
				if err != nil {
					newNodes = append(newNodes, node)
				}
			}
		}
	}

	wi.persistAndLaunch(newNodes, ctx)
}

func (wi *WorkflowInstance) launch(job *JobInstance, ctx context.Context) {
	go func() {
		if wi.isAcceptingJobs() {
			select {
			case wi.runnableJobs <- job:
			case <-ctx.Done():
			}
		}
	}()
}

func (wi *WorkflowInstance) persistAndLaunch(nodes []*Node, pctx context.Context) {
	jobs := wi.persist(nodes)
	for _, job := range jobs {
		ctx, _ := context.WithCancel(pctx)
		wi.launch(job, ctx)
	}
}

func (wi *WorkflowInstance) persist(nodes []*Node) []*JobInstance {
	jobs := make([]*JobInstance, len(nodes))
	for index, node := range nodes {
		job, err := wi.newJobInstance(node)
		if err != nil {
			// TODO: don't panic, should fail workflow
			panic(err)
		}
		wi.entry.jobs = append(wi.entry.jobs, job.entry)
		jobs[index] = job
	}
	return jobs
}

func (wi *WorkflowInstance) doneJobsHandler(workflowDone chan<- string, ctx context.Context) {
	var subCtx context.Context

	for doneJob := range wi.doneJobs {
		var jobs []*JobInstance

		if doneJob.getStatus() != "Done" {
			continue
		}

		// Persist all new downstream nodes to the database first
		jobs = wi.persist(wi.Graph().Downstream(doneJob.node()))

		// This might have been the last job, maybe workflow is completed
		subCtx, _ = context.WithCancel(ctx)
		wi.checkWorkflowTerminal(workflowDone, subCtx)

		for _, job := range jobs {
			subCtx, _ = context.WithCancel(ctx)
			wi.launch(job, subCtx)
		}
	}
}

func (wi *WorkflowInstance) run(done chan<- *WorkflowInstance, ctx context.Context, abortCtx context.Context, wg *sync.WaitGroup) {
	var log = wi.log.ForWorkflow(wi.Uuid())

	wi.start = time.Now()
	log.Info("run: enter")
	defer func() {
		log.Info("run: exit (runtime %s)", time.Since(wi.start))
	}()

	wi.setStatus("Started")

	var jobDone = make(chan *JobInstance)
	var workflowDone = make(chan string)

	defer func() {
		close(wi.doneJobs) // This will cause the doneJobsHandler() goroutine to exit

		if wi.aborting {
			wi.setStatus("Aborted")
		}
		log.Info("workflow: exiting with state: %s", wi.Status())

		if !wi.shuttingDown {
			wi.done <- wi
			close(wi.done)
			done <- wi
		}
		wg.Done()
	}()

	var processDone = func(ji *JobInstance) {
		log.Info("workflow: job finished: %s", ji.status())
		wi.doneJobs <- ji
		delete(wi.running, ji)
	}

	subCtx, _ := context.WithCancel(ctx)
	wi.initJobs(workflowDone, ctx)
	go wi.doneJobsHandler(workflowDone, subCtx)

	for {
		if wi.isAcceptingJobs() == false {
			if wi.aborting && len(wi.running) == 0 {
				return
			}

			select {
			case <-wi.runnableJobs:
			case ji := <-jobDone:
				processDone(ji)
			case <-workflowDone:
				return
			}
		} else {
			select {
			case ji := <-wi.runnableJobs:
				log.Info("workflow: launching call: %s", ji.node().String())
				wi.running[ji] = struct{}{}
				jiCtx, jiCancel := context.WithCancel(ctx)
				ji.cancel = jiCancel
				jiAbortCtx, jiAbort := context.WithCancel(context.Background())
				ji.abort = jiAbort
				go ji.run(wi.backend, exec.Command("sleep", "2"), jobDone, jiCtx, jiAbortCtx)
			case <-workflowDone:
				return
			case ji := <-jobDone:
				processDone(ji)
			case <-ctx.Done():
				log.Info("workflow: shutdown...")
				wi.shuttingDown = true
				return
			case <-abortCtx.Done():
				log.Info("workflow: abort...")
				wi.Abort()
				for ji, _ := range wi.running {
					ji.abort()
				}
				wi.setStatus("Aborting")
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
	mutex              *sync.Mutex
	running            map[*WorkflowInstance]struct{}
	cancel             func()
	waitGroup          *sync.WaitGroup
	workflowMaxRuntime time.Duration
	backends           map[string]Backend
	backendsMutex      *sync.Mutex
}

func (kernel *Kernel) run(ctx context.Context) {
	var workflowWaitGroup sync.WaitGroup
	var workers = 0
	var workflowDone = make(chan *WorkflowInstance)
	var log = kernel.log

	log.Info("kernel: enter")
	defer func() {
		kernel.log.Info("kernel.run exit (1)")
		kernel.mutex.Lock()
		kernel.on = false
		kernel.mutex.Unlock()
		kernel.log.Info("kernel.run exit (2)")
		workflowWaitGroup.Wait()
		kernel.log.Info("kernel.run exit (3)")
		kernel.waitGroup.Done()
		log.Info("kernel: exit")
	}()

	go func() {
		resumableEntries, err := kernel.db.LoadWorkflowsByStatus(log, "Started", "NotStarted")
		if err != nil {
			panic(err) // TODO: don't panic
		}
		for _, entry := range resumableEntries {
			log.Info("kernel: resume workflow %s", entry.uuid)
			wi, err := kernel.newWorkflowInstanceFromEntry(entry)
			if err != nil {
				panic(err) // TODO: don't panic
			}
			kernel.enqueue(wi, time.Minute)
		}
	}()

	var processDone = func(result *WorkflowInstance) {
		log.Info("kernel: workflow %s finished: %s", result.Uuid(), result.Status())
		delete(kernel.running, result)
		workers--
	}

	for {
		// TODO: need mutex around kernel.on
		if kernel.on == false {
			if len(kernel.running) == 0 {
				return
			}
			select {
			case wi := <-workflowDone:
				processDone(wi)
			case <-kernel.submitChannel:
			}
		} else if workers < kernel.maxWorkers {
			select {
			case wi, ok := <-kernel.submitChannel:
				if ok {
					if wi.aborting {
						wi.setStatus("Aborted")
						go func() { workflowDone <- wi }()
						continue
					}
					workers++
					wiCtx, workflowCancel := context.WithCancel(ctx)
					wiAbortCtx, workflowAbort := context.WithTimeout(context.Background(), kernel.workflowMaxRuntime)
					wi.cancel = workflowCancel
					wi.abort = workflowAbort
					log.Info("kernel: starting %s", wi.Uuid())
					workflowWaitGroup.Add(1)
					go wi.run(workflowDone, wiCtx, wiAbortCtx, &workflowWaitGroup)
				}
			case wi := <-workflowDone:
				processDone(wi)
			case <-ctx.Done():
				log.Info("kernel: shutdown...")
				return
			}
		} else {
			select {
			case wi := <-workflowDone:
				processDone(wi)
			case <-ctx.Done():
				log.Info("kernel: shutdown...")
				return
			}
		}
	}
}

// TODO: do I still need this?
func (kernel Kernel) signalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(kernel Kernel) {
		sig := <-sigs
		kernel.log.Info("%s signal detected... turning off kernel", sig)
		kernel.cancel()
		kernel.waitGroup.Wait()
		kernel.log.Info("%s signal detected... kernel off", sig)
		os.Exit(130)
	}(kernel)
}

func (kernel *Kernel) enqueue(wi *WorkflowInstance, timeout time.Duration) error {
	kernel.mutex.Lock()
	defer kernel.mutex.Unlock()
	if kernel.on == true {
		select {
		case kernel.submitChannel <- wi:
			kernel.running[wi] = struct{}{}
		case <-time.After(timeout):
			return errors.New("Timeout submitting workflow")
		}
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (kernel *Kernel) newWorkflowInstance(uuid uuid.UUID, source *WorkflowSources, backend string) (*WorkflowInstance, error) {
	entry, err := kernel.db.NewWorkflowEntry(uuid, source.wdl, source.inputs, source.options, backend, kernel.log)
	if err != nil {
		return nil, err
	}
	return kernel.newWorkflowInstanceFromEntry(entry)
}

func (kernel *Kernel) newWorkflowInstanceFromEntry(entry *WorkflowEntry) (*WorkflowInstance, error) {
	var jobsMutex sync.Mutex

	kernel.backendsMutex.Lock()
	if _, ok := kernel.backends[entry.backend]; !ok {
		return nil, errors.New(fmt.Sprintf("No backend named '%s' found", entry.backend))
	}
	kernel.backendsMutex.Unlock()

	wi := WorkflowInstance{
		entry:        entry,
		done:         make(chan *WorkflowInstance, 1),
		runnableJobs: make(chan *JobInstance),
		doneJobs:     make(chan *JobInstance),
		running:      make(map[*JobInstance]struct{}),
		jobsMutex:    &jobsMutex,
		cancel:       func() {},
		abort:        func() {},
		shuttingDown: false,
		aborting:     false,
		backend:      kernel.backends[entry.backend],
		db:           kernel.db,
		log:          kernel.log.ForWorkflow(entry.uuid),
		_graph:       nil}

	return &wi, nil
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	var wg sync.WaitGroup
	var mutex sync.Mutex
	var backendsMutex sync.Mutex
	// TODO: move DB creation into On()?  Semantics should be: zero DB connections on Off()
	db := NewMapleDb(dbName, dbConnection, log)
	kernel := Kernel{
		log:                log,
		db:                 db,
		start:              time.Now(),
		on:                 false,
		maxWorkers:         concurrentWorkflows,
		submitChannel:      make(chan *WorkflowInstance, submitQueueSize),
		mutex:              &mutex,
		running:            make(map[*WorkflowInstance]struct{}),
		cancel:             func() {},
		waitGroup:          &wg,
		workflowMaxRuntime: time.Second * 600,
		backends:           make(map[string]Backend),
		backendsMutex:      &backendsMutex}
	return &kernel
}

func (kernel *Kernel) On() {
	if kernel.on == true {
		return
	}

	kernel.mutex.Lock()
	defer kernel.mutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	kernel.on = true
	kernel.cancel = cancel
	kernel.waitGroup.Add(1)
	go kernel.run(ctx)
	kernel.signalHandler() // TODO: make this part of kernel.run(), also make it shutdown on Off()
}

func (kernel *Kernel) Off() {
	if kernel.on == false {
		return
	}

	kernel.cancel()
	kernel.waitGroup.Wait()
}

func (kernel *Kernel) Run(wdl, inputs, options, backend string, id uuid.UUID) (*WorkflowInstance, error) {
	err := kernel.Submit(wdl, inputs, options, backend, id, time.Hour)
	if err != nil {
		return nil, err
	}
	wi, err := kernel.Wait(id, time.Hour*1000) // TODO: infinite timeout?
	if err != nil {
		return nil, err
	}
	return wi, nil
}

func (kernel *Kernel) Submit(wdl, inputs, options, backend string, id uuid.UUID, timeout time.Duration) error {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options), nil}
	wi, err := kernel.newWorkflowInstance(id, &sources, backend)
	if err != nil {
		return err
	}

	err = kernel.enqueue(wi, timeout)
	if err != nil {
		return err
	}

	return nil
}

func (kernel *Kernel) Wait(id uuid.UUID, timeout time.Duration) (*WorkflowInstance, error) {
	wi := kernel.find(id)
	if wi == nil {
		return nil, errors.New(fmt.Sprintf("Could not find running workflow %s", id))
	}
	<-wi.done
	return wi, nil
}

func (kernel *Kernel) find(id uuid.UUID) *WorkflowInstance {
	kernel.mutex.Lock()
	defer kernel.mutex.Unlock()
	for wi, _ := range kernel.running {
		if wi.Uuid() == id {
			return wi
		}
	}
	return nil
}

func (kernel *Kernel) Abort(id uuid.UUID, timeout time.Duration) error {
	wi := kernel.find(id)
	if wi == nil {
		return errors.New(fmt.Sprintf("Could not find running workflow %s", id))
	}

	wi.Abort()

	_, err := kernel.Wait(id, timeout)
	if err != nil {
		return err
	}
	return nil
}

func (kernel *Kernel) AbortCall(id uuid.UUID, timeout time.Duration) error {
	return nil
}

func (kernel *Kernel) List() ([]*WorkflowEntry, error) {
	return kernel.db.LoadWorkflowsByStatus(kernel.log, "Started", "NotStarted", "Done", "Aborted")
}

func (kernel *Kernel) Uptime() time.Duration {
	return time.Since(kernel.start)
}

func (kernel *Kernel) RegisterBackend(name string, be Backend) error {
	kernel.backendsMutex.Lock()
	defer kernel.backendsMutex.Unlock()

	err := be.Init()
	if err != nil {
		return err
	}

	kernel.backends[name] = be
	return nil
}
