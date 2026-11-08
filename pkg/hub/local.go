package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/diphyx/grid/pkg/wire"
)

// The local API: HTTP over a Unix socket, JSON. The CLI is one client of it.
//
//	GET  /status              Status
//	GET  /nodes               []NodeView
//	GET  /jobs                []Record
//	GET  /job/{id}            JobView
//	GET  /tail/{id}?after=N   JSON lines: wire.JSONEvent, until the job ends
//	GET  /files/{id}          the pulled files, a tar
//	GET  /history?kind=K      []history.Entry, all kinds or one
//	GET  /stored/{manifest}   {stored}: whether the hub has a tar for a source with that manifest
//	POST /jobs                multipart: "job" (JSON Job), then per push, in order, a "push" part
//	                          (a tar, its file name the source's manifest) or a "stored" part
//	                          (a manifest the hub has) → {id}
//	POST /kill/{id}           stop a running job
//	POST /drop/{id}           forget a job and its files

// Status is `grid hub status`.
type Status struct {
	Key     string    `json:"key"`
	Signal  bool      `json:"signal"`
	Port    int       `json:"port,omitempty"`
	Since   time.Time `json:"since"`
	Nodes   int       `json:"nodes"`
	Online  int       `json:"online"`
	Queued  int       `json:"queued"`
	Running int       `json:"running"`
	Done    int       `json:"done"`
}

// JobView is `grid hub job`: the record and the pulled files.
type JobView struct {
	Record

	Files []wire.File `json:"files"`
}

// Status sums up the hub.
func (hub *Hub) Status() Status {
	status := Status{Key: hub.key.Public.String(), Signal: hub.config.Signal, Since: hub.started, Nodes: len(hub.links)}
	if hub.config.Signal {
		status.Port = hub.config.Port
		if status.Port == 0 {
			status.Port = wire.DEFAULT_PORT
		}
	}
	for _, view := range hub.Nodes() {
		if view.Online {
			status.Online++
		}
	}
	for _, record := range hub.Jobs() {
		switch record.State {
		case STATE_QUEUED:
			{
				status.Queued++
			}
		case STATE_RUNNING:
			{
				status.Running++
			}
		default:
			{
				status.Done++
			}
		}
	}

	return status
}

// View is one job with its pulled files.
func (hub *Hub) View(id string) (JobView, error) {
	found, entryError := hub.entry(id)
	if entryError != nil {
		return JobView{}, entryError
	}
	view := JobView{Record: found.snapshot(), Files: []wire.File{}}
	root, openError := os.OpenRoot(found.path("files"))
	if openError != nil {
		return view, nil
	}
	defer func() { _ = root.Close() }()

	entries, _ := wire.Collect(root, ".", 0)
	for _, each := range entries {
		if !each.Dir && each.Link == "" {
			view.Files = append(view.Files, wire.File{Path: each.Path, Size: uint64(each.Size), Modified: uint64(each.ModTime.UnixMilli())})
		}
	}

	return view, nil
}

// ServeLocal serves the API on the config's socket until Close.
func (hub *Hub) ServeLocal() error {
	path := hub.config.Socket
	mkdirError := os.MkdirAll(filepath.Dir(path), 0o755)
	if mkdirError != nil {
		return mkdirError
	}
	removeError := os.Remove(path)
	if removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
		return removeError
	}
	listener, listenError := net.Listen("unix", path)
	if listenError != nil {
		return listenError
	}
	chmodError := os.Chmod(path, 0o660)
	if chmodError != nil {
		return chmodError
	}
	go func() {
		<-hub.stop
		_ = listener.Close()
	}()
	serveError := http.Serve(listener, hub.Mux())
	if errors.Is(serveError, net.ErrClosed) {
		return nil
	}

	return serveError
}

// Mux is the API's handler, to serve it elsewhere too.
func (hub *Hub) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, hub.Status()) })
	mux.HandleFunc("GET /nodes", func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, hub.Nodes()) })
	mux.HandleFunc("GET /jobs", func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, hub.Jobs()) })
	mux.HandleFunc("GET /job/{id}", func(writer http.ResponseWriter, request *http.Request) {
		view, viewError := hub.View(request.PathValue("id"))
		if viewError != nil {
			http.Error(writer, viewError.Error(), http.StatusNotFound)

			return
		}
		writeJSON(writer, view)
	})
	mux.HandleFunc("GET /tail/{id}", func(writer http.ResponseWriter, request *http.Request) {
		after, _ := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
		_, entryError := hub.entry(request.PathValue("id"))
		if entryError != nil {
			http.Error(writer, entryError.Error(), http.StatusNotFound)

			return
		}
		writer.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(writer)
		flusher, _ := writer.(http.Flusher)
		_ = hub.Events(request.PathValue("id"), after, request.Context().Done(), func(event wire.Event) bool {
			encodeError := encoder.Encode(event.ToJSON())
			if flusher != nil {
				flusher.Flush()
			}

			return encodeError == nil
		})
	})
	mux.HandleFunc("GET /files/{id}", func(writer http.ResponseWriter, request *http.Request) {
		write, filesError := hub.Files(request.PathValue("id"))
		if errors.Is(filesError, ErrUnknown) {
			http.Error(writer, filesError.Error(), http.StatusNotFound)

			return
		}
		if filesError != nil {
			http.Error(writer, filesError.Error(), http.StatusInternalServerError)

			return
		}
		writer.Header().Set("Content-Type", "application/x-tar")
		_ = write(writer)
	})
	mux.HandleFunc("GET /history", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, hub.history.Read(request.URL.Query().Get("kind")))
	})
	mux.HandleFunc("GET /stored/{manifest}", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, map[string]bool{"stored": hub.Stored(request.PathValue("manifest"))})
	})
	mux.HandleFunc("POST /jobs", hub.submitHTTP)
	mux.HandleFunc("POST /kill/{id}", func(writer http.ResponseWriter, request *http.Request) {
		writeResult(writer, hub.Kill(request.PathValue("id")))
	})
	mux.HandleFunc("POST /drop/{id}", func(writer http.ResponseWriter, request *http.Request) {
		writeResult(writer, hub.Drop(request.PathValue("id")))
	})

	return mux
}

// submitHTTP reads a multipart job: the "job" part, then one "push" or "stored" part per
// job.Push, in order.
func (hub *Hub) submitHTTP(writer http.ResponseWriter, request *http.Request) {
	reader, multipartError := request.MultipartReader()
	if multipartError != nil {
		http.Error(writer, multipartError.Error(), http.StatusBadRequest)

		return
	}
	var job *Job
	var pushes []Packed
	var files []*os.File
	defer func() {
		for _, file := range files {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	for {
		part, nextError := reader.NextPart()
		if errors.Is(nextError, io.EOF) {
			break
		}
		if nextError != nil {
			http.Error(writer, nextError.Error(), http.StatusBadRequest)

			return
		}
		switch part.FormName() {
		case "job":
			{
				job = &Job{}
				decodeError := json.NewDecoder(part).Decode(job)
				if decodeError != nil {
					http.Error(writer, "job: "+decodeError.Error(), http.StatusBadRequest)

					return
				}
			}
		case "push":
			{
				file, createError := os.CreateTemp("", "grid-push-*.tar")
				if createError != nil {
					http.Error(writer, createError.Error(), http.StatusInternalServerError)

					return
				}
				files = append(files, file)
				_, copyError := io.Copy(file, part)
				if copyError != nil {
					http.Error(writer, copyError.Error(), http.StatusBadRequest)

					return
				}
				_, _ = file.Seek(0, io.SeekStart)
				pushes = append(pushes, Packed{Manifest: part.FileName(), Tar: file})
			}
		case "stored":
			{
				manifest, readError := io.ReadAll(io.LimitReader(part, 128))
				if readError != nil {
					http.Error(writer, readError.Error(), http.StatusBadRequest)

					return
				}
				pushes = append(pushes, Packed{Manifest: string(manifest)})
			}
		}
	}
	if job == nil {
		http.Error(writer, "job: missing", http.StatusBadRequest)

		return
	}
	id, submitError := hub.SubmitPacked(*job, pushes)
	if submitError != nil {
		http.Error(writer, submitError.Error(), http.StatusBadRequest)

		return
	}
	writeJSON(writer, map[string]string{"id": id})
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func writeResult(writer http.ResponseWriter, problem error) {
	switch {
	case problem == nil:
		{
			writeJSON(writer, map[string]bool{"ok": true})
		}
	case errors.Is(problem, ErrUnknown):
		{
			http.Error(writer, problem.Error(), http.StatusNotFound)
		}
	default:
		{
			http.Error(writer, fmt.Sprint(problem), http.StatusInternalServerError)
		}
	}
}
