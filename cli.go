package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	Version = "Unknown"
	GitHash = "Unknown"
)

func main() {

	var (
		app          = kingpin.New("maple", "A workflow engine")
		host         = app.Flag("host", "Host where Maple server is running").Default("localhost:8765").String()
		dbDriver     = app.Flag("db-driver", "Database driver.  Only accepts 'sqlite3'").Default("sqlite3").String()
		dbConnection = app.Flag("db", "Database connection string.  For sqlite, the file that will hold the database").Default("DB").String()
		queueSize    = app.Flag("queue-size", "Submission queue size").Default("1000").Int()
		concurrentWf = app.Flag("concurrent-workflows", "Number of workflows").Default("1000").Int()
		logPath      = app.Flag("log", "Path to write logs").Default("maple.log").String()
		run          = app.Command("run", "Run workflows")
		runGraph     = run.Arg("wdl", "Graph file").Required().String()
		server       = app.Command("server", "Start HTTP server")
		ping         = app.Command("ping", "Send ping to Maple server")
		abort        = app.Command("abort", "Send ping to Maple server")
		abortUuid    = abort.Arg("uuid", "UUID of workflow to abort").Required().String()
	)

	kingpin.Version(Version)
	args, err := app.Parse(os.Args[1:])

	switch kingpin.MustParse(args, err) {
	case abort.FullCommand():
		resp, err := http.Get(fmt.Sprintf("http://%s/abort?uuid=%s", *host, *abortUuid))
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		fmt.Println(resp.Status)
	case ping.FullCommand():
		resp, err := http.Get(fmt.Sprintf("http://%s/ping", *host))
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)

		var dat map[string]interface{}

		if err := json.Unmarshal([]byte(body), &dat); err != nil {
			panic(err)
		}
		version := dat["version"].(string)
		hash := dat["hash"].(string)
		uptime := dat["uptime"].(string)

		fmt.Printf("Server version: %s\n", version)
		fmt.Printf("Server Git hash: %s\n", hash)
		fmt.Printf("Server uptime: %s\n", uptime)
	case run.FullCommand():
		contents, err := ioutil.ReadFile(*runGraph)
		if err != nil {
			// TODO: don't panic.  here and below
			panic(err)
		}

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("wdl", "wdl")
		if err != nil {
			panic(err)
		}
		_, err = io.Copy(part, strings.NewReader(string(contents)))

		part, err = writer.CreateFormFile("inputs", "inputs")
		if err != nil {
			panic(err)
		}
		_, err = io.Copy(part, strings.NewReader("inputs"))

		part, err = writer.CreateFormFile("options", "options")
		if err != nil {
			panic(err)
		}
		_, err = io.Copy(part, strings.NewReader("options"))
		writer.Close()

		client := &http.Client{}
		req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/submit", *host), body)
		if err != nil {
			panic(err)
		}
		req.Header.Add("Content-Type", writer.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}

		fmt.Println(string(respBody))
	case server.FullCommand():
		logger := NewLogger().ToFile(*logPath).ToWriter(os.Stdout)
		kernel := NewKernel(logger, *dbDriver, *dbConnection, *concurrentWf, *queueSize)

		http.HandleFunc("/submit", submitHttpEndpoint(kernel))
		http.HandleFunc("/ping", pingHttpEndpoint(kernel, Version, GitHash))
		http.HandleFunc("/abort", abortHttpEndpoint(kernel))

		logger.Info("Listening on %s ...", *host)
		http.ListenAndServe(*host, nil)
	}
}
