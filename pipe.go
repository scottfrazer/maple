package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"syscall"
	"time"
)

type NonBlockingFifo struct {
	fp   *os.File
	path string
}

func NewNonBlockingFifo(path string, timeout time.Duration) *NonBlockingFifo {
	syscall.Mkfifo(path, 0777)
	fifo := NonBlockingFifo{nil, path}
	fifoOpen := make(chan bool)

	go func() {
		fifo.fp, _ = os.OpenFile(path, os.O_WRONLY, os.ModeNamedPipe)
		fifoOpen <- true
	}()

	select {
	case <-fifoOpen:
	case <-time.After(timeout):
		reader, _ := os.OpenFile(path, os.O_RDONLY, os.ModeNamedPipe)
		reader.Close()
		<-fifoOpen
	}

	return &fifo
}

func (fifo *NonBlockingFifo) Write(p []byte) (n int, err error) {
	return fifo.fp.Write(p)
}

func (fifo *NonBlockingFifo) Close() {
	fifo.fp.Close()
	os.Remove(fifo.path)
}

func main() {
	var dots [990]byte
	for i := 0; i < len(dots); i++ {
		dots[i] = '.'
	}

	if os.Args[1] == "client" {
		fp, err := os.OpenFile(os.Args[2], os.O_RDONLY, 0777)
		if err != nil {
			fmt.Printf("Error opening %s: %s", os.Args[2], err)
		}
		tee := io.TeeReader(fp, os.Stdout)
		ioutil.ReadAll(tee)
	}

	if os.Args[1] == "server" {
		fifo := NewNonBlockingFifo("fifo0", time.Second*5)
		defer fifo.Close()
		w := io.MultiWriter(os.Stdout, fifo)
		for i := 0; i < 100; i++ {
			_, err := fmt.Fprintf(w, "%010d%s\n", i, dots)
			if err != nil {
				fmt.Printf("error: %s\n", err)
			}
			time.Sleep(time.Millisecond * 200)
		}
	}
}
