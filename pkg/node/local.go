package node

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// The local view: HTTP over a Unix socket, JSON, read only. The CLI reads it.
//
//	GET /status                 Status
//	GET /jobs                   []Meta
//	GET /history?kind=K&hub=IP  []history.Entry; each filter left out = all
//	GET /job/{id}               JobView
//	GET /tail/{id}?after=N      JSON lines: wire.JSONEvent, until drop

// Status is `grid node status`.
type Status struct {
	Addr   string    `json:"addr"`
	Key    string    `json:"key"`
	Tags   []string  `json:"tags"`
	Limit  int       `json:"limit"` // jobs at once
	Free   uint64    `json:"free"`
	Since  time.Time `json:"since"`
	Signal bool      `json:"signal"`
	Hubs   []HubView `json:"hubs"`
	Share  ShareView `json:"share"`
}

// HubView is a trusted hub in `grid node status`.
type HubView struct {
	IP     string `json:"ip"`
	Addr   string `json:"addr"`   // where it listens, with signal
	Online bool   `json:"online"` // signal connection up
}

// ShareView is the share space in `grid node status`.
type ShareView struct {
	Limit  int64 `json:"limit"` // bytes; 0 = off
	Used   int64 `json:"used"`
	Shares int   `json:"shares"`
}

// JobView is `grid node job`.
type JobView struct {
	Meta

	Files []wire.File `json:"files"`
}

// Status is what `grid node status` shows.
func (node *Node) Status() Status {
	used, count := node.shares.usage()
	hubs := make([]HubView, 0, len(node.hubs))
	for _, link := range node.hubs {
		hubs = append(hubs, HubView{IP: link.ip, Addr: link.addr, Online: link.online.Load()})
	}

	return Status{Addr: node.Addr(), Key: node.key.Public.String(), Tags: node.config.Tags, Limit: node.config.Jobs.Limit, Free: node.free(),
		Since: node.started, Signal: node.config.Signal, Hubs: hubs, Share: ShareView{Limit: node.shares.limit, Used: used, Shares: count}}
}

// ServeLocal serves the view on the config's socket until Close.
func (node *Node) ServeLocal() error {
	path := node.config.Socket
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
	go func() { <-node.stop; listener.Close() }()
	listenError = http.Serve(listener, node.localMux())
	if errors.Is(listenError, net.ErrClosed) {
		return nil
	}
	return listenError
}

func (node *Node) localMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, request *http.Request) { writeJSON(writer, node.Status()) })
	mux.HandleFunc("GET /jobs", func(writer http.ResponseWriter, request *http.Request) { writeJSON(writer, node.Jobs()) })
	mux.HandleFunc("GET /history", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, node.history.Read(request.URL.Query().Get("kind"), hubQuery(request)))
	})
	mux.HandleFunc("GET /job/{id}", func(writer http.ResponseWriter, request *http.Request) {
		job := node.job(request.PathValue("id"))
		if job == nil {
			http.Error(writer, "no such job", http.StatusNotFound)

			return
		}
		job.mutex.Lock()
		view := JobView{Meta: job.meta}
		shared := map[string]bool{}
		for _, linked := range job.meta.Shares {
			shared[linked.Path] = true
		}
		entries, _ := wire.CollectSkip(job.space, ".", 0, shared)
		job.mutex.Unlock()
		view.Files = []wire.File{}
		for _, entry := range entries {
			if !entry.Dir && entry.Link == "" {
				view.Files = append(view.Files, wire.File{Path: entry.Path, Size: uint64(entry.Size), Modified: uint64(entry.ModTime.UnixMilli())})
			}
		}
		writeJSON(writer, view)
	})
	mux.HandleFunc("GET /tail/{id}", func(writer http.ResponseWriter, request *http.Request) {
		job := node.job(request.PathValue("id"))
		if job == nil {
			http.Error(writer, "no such job", http.StatusNotFound)

			return
		}
		next, _ := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
		writer.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(writer)
		flusher, _ := writer.(http.Flusher)
		for {
			job.mutex.Lock()
			events, eventsError := job.events(next, 256)
			dropped, changed := job.dropped, job.changed
			job.mutex.Unlock()
			if eventsError != nil {
				return
			}
			for _, event := range events {
				if enc.Encode(event.ToJSON()) != nil {
					return
				}
				next = event.Seq
			}
			if flusher != nil {
				flusher.Flush()
			}
			if len(events) == 256 {
				continue
			}
			if dropped {
				return
			}
			select {
			case <-changed:
			case <-request.Context().Done():
				return
			case <-node.stop:
				return
			}
		}
	})

	return mux
}

// hubQuery is the ?hub= filter, written the way Meta.Hub is; "" = all.
func hubQuery(request *http.Request) string {
	hub := request.URL.Query().Get("hub")
	if hub == "" {
		return ""
	}

	return wire.NormIP(hub)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
