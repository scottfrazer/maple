package maple

import (
	"golang.org/x/net/context"
	"strconv"
	"sync"
	"time"
)

type JobHandle interface {
	String() string
}

type LocalJobHandle struct {
	id int
}

func (handle *LocalJobHandle) String() string {
	return strconv.FormatInt(int64(handle.id), 10)
}

type TestBackendJob struct {
	ctx      context.Context
	abortCtx context.Context
	ticker   <-chan time.Time
}

type Backend interface {
	Submit(done chan<- bool, ctx context.Context, abortCtx context.Context) JobHandle
}

type TestBackend struct {
	counter   int
	jobs      map[int]*TestBackendJob
	jobsMutex *sync.Mutex
}

func NewTestBackend() Backend {
	var mutex sync.Mutex
	be := TestBackend{0, make(map[int]*TestBackendJob), &mutex}
	return be
}

func (be TestBackend) Submit(done chan<- bool, ctx context.Context, abortCtx context.Context) JobHandle {
	be.jobsMutex.Lock()
	defer be.jobsMutex.Unlock()
	job := TestBackendJob{ctx, abortCtx, time.After(time.Second * 2)}
	handle := &LocalJobHandle{be.counter}
	be.jobs[be.counter] = &job
	be.counter += 1

	go func(job *TestBackendJob) {
		select {
		case <-job.ticker:
		case <-job.ctx.Done():
		case <-job.abortCtx.Done():
		}

		//cmd.Run()

		be.jobsMutex.Lock()
		delete(be.jobs, handle.id)
		be.jobsMutex.Unlock()
		done <- true
	}(&job)

	return handle
}

func Abort(handle *JobHandle) {
	/*err := cmd.Process.Kill()
	if err != nil {
		panic(err)
	}*/
}

func (be *TestBackend) Wait(handle JobHandle) {

}

func (be *TestBackend) Recover(handle JobHandle) {

}
