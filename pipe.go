package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type NonBlockingFifo struct {
	fp   *os.File
	path string
}

func NewNonBlockingFifo(path string) (*NonBlockingFifo, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		syscall.Mkfifo(path, 0777)
	}
	fifo := NonBlockingFifo{nil, path}
	return &fifo, nil
}

func (fifo *NonBlockingFifo) Write(p []byte) (n int, err error) {
	return fifo.fp.Write(p)
}

/* Give client `timeout` amount of time to connect and read or separate goroutine will connect */
func (fifo *NonBlockingFifo) Unblock(timeout time.Duration) *NonBlockingFifo {
	fifoOpen := make(chan error)

	go func() {
		fp, err := os.OpenFile(fifo.path, os.O_WRONLY, os.ModeNamedPipe)
		if err == nil {
			fifo.fp = fp
		}
		fifoOpen <- err
	}()

	select {
	case err := <-fifoOpen:
		if err != nil {
			fmt.Printf("error opening (w) %s: %s\n", fifo.path, err)
			return nil
		}
	case <-time.After(timeout):
		reader, err := os.OpenFile(fifo.path, os.O_RDONLY, os.ModeNamedPipe)
		if err != nil {
			fmt.Printf("error opening (r) %s: %s\n", fifo.path, err)
			return nil
		}
		reader.Close()
		<-fifoOpen
	}

	return fifo
}

func (fifo *NonBlockingFifo) Close() {
	fifo.fp.Close()
	os.Remove(fifo.path)
}

func main() {
	if os.Args[1] == "client" {
		if os.Args[2] == "submit" {
			resp, err := http.Get("http://localhost:8765/submit")
			if err != nil {
				log.Fatalf("Could not connect to server: %s", err)
			}
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)

			fmt.Printf("/submit: %s\n", body)
		}
		if os.Args[2] == "attach" {
			resp, err := http.Get(fmt.Sprintf("http://localhost:8765/attach?id=%s", os.Args[3]))
			if err != nil {
				log.Fatalf("Could not connect to server: %s", err)
			}
			defer resp.Body.Close()
			fifoPath, err := ioutil.ReadAll(resp.Body)

			fmt.Printf("/attach: %s\n", fifoPath)

			fp, err := os.OpenFile(string(fifoPath), os.O_RDONLY, 0777)
			if err != nil {
				fmt.Printf("Error opening %s: %s\n", fifoPath, err)
			}
			tee := io.TeeReader(fp, os.Stdout)
			ioutil.ReadAll(tee)
		}
	}

	if os.Args[1] == "server" {
		type Proc struct {
			id   int
			fifo *NonBlockingFifo
			log  *Logger
		}

		logger := NewLogger().ToWriter(os.Stdout)
		dots := strings.Repeat(".", 985)
		procs := make(map[int]*Proc)
		procsId := 0
		var procsMutex sync.Mutex

		http.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
			procsMutex.Lock()
			defer procsMutex.Unlock()

			reqId := procsId
			procsId += 1
			proc := &Proc{reqId, nil, logger}
			procs[reqId] = proc

			go func(proc *Proc) {
				for i := 0; i < 100; i++ {
					proc.log.Info("%04d-%010d%s\n", proc.id, i, dots)
					time.Sleep(time.Millisecond * 1000)
				}
				if proc.fifo != nil {
					proc.fifo.Close()
					proc.fifo = nil
				}
			}(proc)

			fmt.Fprintf(w, "%d", reqId)
		})

		http.HandleFunc("/attach", func(w http.ResponseWriter, r *http.Request) {
			procsMutex.Lock()
			defer procsMutex.Unlock()

			reqId, _ := strconv.Atoi(r.URL.Query().Get("id"))
			fifoName := fmt.Sprintf("fifo_%d", reqId)
			val, ok := procs[reqId]

			if !ok {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, "ID does not exist")
				return
			}

			if val.fifo == nil {
				fifo, err := NewNonBlockingFifo(fifoName)
				if err != nil {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					io.WriteString(w, "Could not create FIFO")
					return
				}
				val.fifo = fifo

				go func(reqId int) {
					val.fifo.Unblock(time.Second * 2)
					procsMutex.Lock()
					defer procsMutex.Unlock()
					procs[reqId].fifo = fifo
					procs[reqId].log = procs[reqId].log.ToWriter(fifo)
				}(reqId)
			}

			fmt.Fprintf(w, fifoName)
		})

		log.Fatal(http.ListenAndServe(":8765", nil))
	}
}
