package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"

	"github.com/satori/go.uuid"
	"github.com/scottfrazer/maple/foo"
	"gopkg.in/alecthomas/kingpin.v2"
)

func main() {

	foo.Foobar()

	var (
		app          = kingpin.New("myapp", "A workflow engine")
		queueSize    = app.Flag("queue-size", "Submission queue size").Default("1000").Int()
		concurrentWf = app.Flag("concurrent-workflows", "Number of workflows").Default("1000").Int()
		logPath      = app.Flag("log", "Path to write logs").Default("maple.log").String()
		restart      = app.Command("restart", "Restart workflows")
		run          = app.Command("run", "Run workflows")
		runGraph     = run.Arg("wdl", "Graph file").Required().String()
		runN         = run.Arg("count", "Number of instances").Required().Int()
		server       = app.Command("server", "Start HTTP server")
	)

	args, err := app.Parse(os.Args[1:])
	log := NewLogger().ToFile(*logPath).ToWriter(os.Stdout)
	engine := NewKernel(log, "sqlite3", "DB", *concurrentWf, *queueSize)

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
				contents, err := ioutil.ReadFile(*runGraph)
				if err != nil {
					// TODO: don't panic
					panic(err)
				}

				id := uuid.NewV4()
				ctx := engine.RunWorkflow(string(contents), "inputs", "options", id)
				if ctx != nil {
					engine.log.Info("Workflow Complete: %s (status %s)", id, ctx.status)
				} else {
					engine.log.Info("Workflow Incomplete")
				}
				wg.Done()
			}()
		}
		wg.Wait()
	case server.FullCommand():
		log.Info("Listening on :8000 ...")
		http.HandleFunc("/submit", SubmitHttpEndpoint(engine.wd))
		http.ListenAndServe(":8000", nil)
	}

	engine.wd.Abort()
}
