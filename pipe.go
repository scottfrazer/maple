package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

type NonBlockingFifo struct {
	fp   *os.File
	path string
}

func NewNonBlockingFifo(path string, timeout time.Duration) (*NonBlockingFifo, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		syscall.Mkfifo(path, 0777)
	}
	fifo := NonBlockingFifo{nil, path}
	fifoOpen := make(chan error)

	go func() {
		fp, err := os.OpenFile(path, os.O_WRONLY, os.ModeNamedPipe)
		if err == nil {
			fifo.fp = fp
		}
		fifoOpen <- err
	}()

	select {
	case err := <-fifoOpen:
		if err != nil {
			return nil, err
		}
	case <-time.After(timeout):
		reader, err := os.OpenFile(path, os.O_RDONLY, os.ModeNamedPipe)
		if err != nil {
			return nil, err
		}
		reader.Close()
		<-fifoOpen
	}

	fmt.Printf("returning fifo: %v\n", fifo)
	return &fifo, nil
}

func (fifo *NonBlockingFifo) Write(p []byte) (n int, err error) {
	return fifo.fp.Write(p)
}

func (fifo *NonBlockingFifo) Close() {
	fifo.fp.Close()
	os.Remove(fifo.path)
}

func main() {
	if os.Args[1] == "client" {
		resp, err := http.Get("http://localhost:8765/run")
		if err != nil {
			log.Fatalf("Could not connect to server: %s", err)
		}
		defer resp.Body.Close()
		fifoPath, err := ioutil.ReadAll(resp.Body)

		fmt.Printf("FIFO: %s\n", fifoPath)
		fp, err := os.OpenFile(string(fifoPath), os.O_RDONLY, 0777)
		if err != nil {
			fmt.Printf("Error opening %s: %s", fifoPath, err)
		}
		tee := io.TeeReader(fp, os.Stdout)
		ioutil.ReadAll(tee)
	}

	if os.Args[1] == "server" {
		type Proc struct {
			fifo string
			//log  *Logger
		}
		dots := strings.Repeat(".", 990)
		procs := make(map[int]*Proc)
		procsId := 0
		var procsMutex sync.Mutex

		fmt.Println(procs, procsId, procsMutex)

		// GET /run: spin up goroutine, adds procId -> Proc{null, globalLogger}
		// GET /attach/ID: create fifo; respond; goroutine to test FIFO and add
		//                 to procs[ID].log = procs[ID].log.WithWriter(fifo)
		http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "fifo0")
			go func() {
				fifo, err := NewNonBlockingFifo("fifo0", time.Second*5)
				if err != nil {
					log.Fatalf("could not create fifo: %s\n", err)
				}
				defer fifo.Close()

				log := io.MultiWriter(os.Stdout, fifo)
				for i := 0; i < 5; i++ {
					_, err := fmt.Fprintf(log, "%010d%s\n", i, dots)
					if err != nil {
						fmt.Printf("error: %s\n", err)
					}
					time.Sleep(time.Millisecond * 200)
				}
			}()
		})
		log.Fatal(http.ListenAndServe(":8765", nil))
	}
}
