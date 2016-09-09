package maple

import (
	"io"
	"log"
	"net/http"
	//"os/exec"
	"fmt"
	"strings"
	"time"
)

type flushWriter struct {
	f http.Flusher
	w io.Writer
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	fw := flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
	}
	for i := 0; i < 1000; i++ {
		fw.Write([]byte(fmt.Sprintf("%d%s\n", i, strings.Repeat(".", 100))))
		time.Sleep(time.Millisecond * 1)
	}
}

func mainxyz() {
	// http -S http://localhost:8080/
	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
