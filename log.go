package maple

import (
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	prefix       string
	writer       io.Writer
	mutex        *sync.Mutex
	wfLogsPath   string
	callLogsPath string
	logQueries   bool
}

func NewLogger() *Logger {
	var mutex sync.Mutex
	return &Logger{"", ioutil.Discard, &mutex, "", "", true}
}

func (log *Logger) ToFile(path string) *Logger {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("Failed to open log file %s: %s", path, err))
	}
	log.writer = io.MultiWriter(log.writer, file)
	return log
}

func (log *Logger) ToWriter(writer io.Writer) *Logger {
	w := io.MultiWriter(log.writer, writer)
	return &Logger{log.prefix, w, log.mutex, log.wfLogsPath, log.callLogsPath, log.logQueries}
}

func (log *Logger) ForWorkflow(uuid uuid.UUID) *Logger {
	prefix := fmt.Sprintf("[%s] ", uuid.String()[:8])
	return &Logger{prefix, log.writer, log.mutex, log.wfLogsPath, log.callLogsPath, log.logQueries}
}

func (log *Logger) ForJob(uuid uuid.UUID, job *JobInstance) *Logger {
	prefix := fmt.Sprintf("[%s:%s] ", uuid.String()[:8], job.node.name)
	return &Logger{prefix, log.writer, log.mutex, log.wfLogsPath, log.callLogsPath, log.logQueries}
}

func (log *Logger) Info(format string, args ...interface{}) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	now := time.Now().Format("2006-01-02 15:04:05.999")
	fmt.Fprintf(log.writer, now+" "+log.prefix+fmt.Sprintf(format, args...)+"\n")
}

func (log *Logger) DbQuery(query string, args ...string) {
	if log.logQueries {
		log.Info("[QUERY] %s [ARGS] "+strings.Join(args, ", "), query)
	}
}
