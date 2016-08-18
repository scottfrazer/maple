package maple

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	uuid "github.com/satori/go.uuid"
)

func SubmitHttpEndpoint(kernel *Kernel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fp, _, err := r.FormFile("wdl")
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"message": "no WDL file"}`)
			return
		}

		var bytes, _ = ioutil.ReadAll(fp)
		wdl := string(bytes)

		fp, _, err = r.FormFile("inputs")
		var inputs = "{}"
		if err != nil {
			bytes, _ = ioutil.ReadAll(fp)
			inputs = string(bytes)
		}

		fp, _, err = r.FormFile("options")
		var options = "{}"
		if err != nil {
			bytes, _ = ioutil.ReadAll(fp)
			options = string(bytes)
		}

		uuid := uuid.NewV4()
		workflow, err := kernel.Submit(wdl, inputs, options, uuid, time.Millisecond*500)
		if err != nil {
			if err.Error() == "Timeout submitting workflow" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusRequestTimeout)
				io.WriteString(w, `{"message": "timeout submitting workflow (500ms)"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/submit/: %s"}`, err))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, fmt.Sprintf("%s", workflow.Uuid()))
	}
}

func PingHttpEndpoint(kernel *Kernel, version, gitHash string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, fmt.Sprintf(`{"version": "maple %s", "hash": "%s", "uptime": "%s"}`, version, gitHash, kernel.Uptime()))
	}
}

func AbortHttpEndpoint(kernel *Kernel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.FromString(r.URL.Query().Get("uuid"))

		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/abort/: %s"}`, err))
			return
		}

		err = kernel.Abort(id, time.Minute)

		// TODO: Error handling code is duplicated, here and above
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/abort/: %s"}`, err))
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func ListHttpEndpoint(kernel *Kernel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := kernel.List()

		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/list/: %s"}`, err))
			return
		}

		if len(entries) == 0 {
			io.WriteString(w, "[]")
			return
		}

		var strs []string
		for _, entry := range entries {
			strs = append(strs, fmt.Sprintf(`{"Uuid": "%s", "Status": "%s"}`, entry.uuid, entry.LatestStatusEntry()))
		}
		io.WriteString(w, fmt.Sprintf("[\n  %s\n]", strings.Join(strs, ",\n  ")))
	}
}
