package main

import (
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"os"
	"sync"
	"time"
)

var GlobalLogger = NewLogger("my.log")

type Logger struct {
	prefix string
	writer io.Writer
	mutex  *sync.Mutex
}

func NewLogger(path string) *Logger {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("Failed to open log file %s: %s", path, err))
	}

	multi := io.MultiWriter(file, os.Stdout)
	var mutex sync.Mutex
	log := &Logger{"", multi, &mutex}
	return log
}

func (log *Logger) ForWorkflow(uuid uuid.UUID) *Logger {
	prefix := fmt.Sprintf("[%s] ", uuid.String()[:8])
	return &Logger{prefix, log.writer, log.mutex}
}

func (log *Logger) Info(format string, args ...interface{}) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	fmt.Fprintf(log.writer, time.Now().Format("2006-01-02 15:04:05.999")+" "+log.prefix+fmt.Sprintf(format, args...)+"\n")
}
