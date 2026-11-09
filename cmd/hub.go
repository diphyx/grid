package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/diphyx/grid/v2/pkg/history"
	"github.com/diphyx/grid/v2/pkg/hub"
	"github.com/diphyx/grid/v2/pkg/wire"
)

// hubMain is `grid hub <command>`.
func hubMain(command string, options flags) int {
	switch command {
	case "run":
		{
			runHub(options)
		}
	case "submit":
		{
			return submit(options)
		}
	case "status", "nodes", "jobs", "job", "history":
		{
			hubView(command, options)
		}
	case "tail":
		{
			tail(client(options.socket), options)
		}
	case "pull":
		{
			pullFiles(options)
		}
	case "kill", "drop":
		{
			if len(options.pos) != 1 {
				fail(fmt.Errorf("%s: needs an id", command))
			}
			post(client(options.socket), "/"+command+"/"+options.pos[0])
		}
	default:
		{
			fmt.Fprint(os.Stderr, USAGE)

			return 2
		}
	}

	return 0
}

// runHub is the hub daemon.
func runHub(options flags) {
	config, loadError := hub.LoadConfig(options.config)
	if loadError != nil {
		fail(loadError)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	daemon, newError := hub.New(config, log)
	if newError != nil {
		fail(newError)
	}
	startError := daemon.Start()
	if startError != nil {
		fail(startError)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Debug("stopping")
		_ = daemon.Close()
	}()
	serveError := daemon.ServeLocal()
	if serveError != nil {
		fail(serveError)
	}
}

// submit sends a job file; push paths are relative to the file's folder and packed here,
// unless the hub has them already, by their manifest; --repack packs them all again.
func submit(options flags) int {
	if len(options.pos) != 1 {
		fail(errors.New("submit: needs a job file"))
	}
	content, readError := os.ReadFile(options.pos[0])
	if readError != nil {
		fail(readError)
	}
	job, parseError := hub.ParseJob(content)
	if parseError != nil {
		fail(fmt.Errorf("%s: %w", options.pos[0], parseError))
	}
	base := filepath.Dir(options.pos[0])
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, createError := writer.CreateFormField("job")
	if createError != nil {
		fail(createError)
	}
	encodeError := json.NewEncoder(part).Encode(job)
	if encodeError != nil {
		fail(encodeError)
	}
	httpClient := client(options.socket)
	for i, push := range job.Push {
		from := push.From
		if !filepath.IsAbs(from) {
			from = filepath.Join(base, from)
		}
		manifest, manifestError := hub.Manifest(from)
		if manifestError != nil {
			fail(fmt.Errorf("push[%d] %s: %w", i, push.From, manifestError))
		}
		if !options.repack && isStored(httpClient, manifest) {
			storedError := writer.WriteField("stored", manifest)
			if storedError != nil {
				fail(storedError)
			}

			continue
		}
		pushPart, pushError := writer.CreateFormFile("push", manifest)
		if pushError != nil {
			fail(pushError)
		}
		packError := hub.Pack(pushPart, from)
		if packError != nil {
			fail(fmt.Errorf("push[%d] %s: %w", i, push.From, packError))
		}
	}
	closeError := writer.Close()
	if closeError != nil {
		fail(closeError)
	}
	response, postError := httpClient.Post("http://grid/jobs", writer.FormDataContentType(), &body)
	if postError != nil {
		fail(fmt.Errorf("daemon not reachable: %w", postError))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		fail(errors.New(strings.TrimSpace(string(message))))
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeError := json.NewDecoder(response.Body).Decode(&created)
	if decodeError != nil {
		fail(decodeError)
	}
	if !options.wait {
		fmt.Println(created.ID)

		return 0
	}
	fmt.Fprintln(os.Stderr, created.ID)
	if follow(httpClient, created.ID, "", options.json) == hub.STATE_DONE {
		return 0
	}

	return 1
}

// isStored asks the hub whether it has a tar for a source with that manifest.
func isStored(httpClient *http.Client, manifest string) bool {
	var answer struct {
		Stored bool `json:"stored"`
	}

	return get(httpClient, "/stored/"+manifest, &answer) == nil && answer.Stored
}

// post calls kill or drop.
func post(httpClient *http.Client, path string) {
	response, postError := httpClient.Post("http://grid"+path, "application/json", nil)
	if postError != nil {
		fail(fmt.Errorf("daemon not reachable: %w", postError))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		fail(errors.New(strings.TrimSpace(string(message))))
	}
}

// pullFiles copies a job's pulled files into a folder.
func pullFiles(options flags) {
	if len(options.pos) != 2 {
		fail(errors.New("pull: needs an id and a folder"))
	}
	response, getError := client(options.socket).Get("http://grid/files/" + options.pos[0])
	if getError != nil {
		fail(fmt.Errorf("daemon not reachable: %w", getError))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		fail(errors.New(strings.TrimSpace(string(message))))
	}
	temporary, createError := os.CreateTemp("", "grid-pull-*.tar")
	if createError != nil {
		fail(createError)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()

	_, copyError := io.Copy(temporary, response.Body)
	if copyError != nil {
		fail(copyError)
	}
	mkdirError := os.MkdirAll(options.pos[1], 0o755)
	if mkdirError != nil {
		fail(mkdirError)
	}
	root, openError := os.OpenRoot(options.pos[1])
	if openError != nil {
		fail(openError)
	}
	defer func() { _ = root.Close() }()

	_, extractError := wire.Extract(temporary, root, ".")
	if extractError != nil {
		fail(extractError)
	}
}

// hubView is `grid hub status | nodes | jobs | job`.
func hubView(command string, options flags) {
	httpClient := client(options.socket)
	if command == "job" && len(options.pos) != 1 {
		fail(errors.New("job: needs an id"))
	}
	for {
		var out strings.Builder
		showError := showHub(httpClient, command, options, &out)
		if showError != nil {
			fail(showError)
		}
		if options.watch {
			fmt.Print("\033[H\033[2J")
		}
		fmt.Print(out.String())
		if !options.watch {
			return
		}
		time.Sleep(time.Second)
	}
}

func showHub(httpClient *http.Client, command string, options flags, out io.Writer) error {
	var value any
	path := "/" + command
	switch command {
	case "status":
		{
			value = &hub.Status{}
		}
	case "nodes":
		{
			value = &[]hub.NodeView{}
		}
	case "jobs":
		{
			value = &[]hub.Record{}
		}
	case "job":
		{
			path, value = "/job/"+options.pos[0], &hub.JobView{}
		}
	case "history":
		{
			if options.hub != "" {
				return errors.New("--hub: grid node history only")
			}
			path, value = "/history?kind="+url.QueryEscape(options.kind), &[]history.Entry{}
		}
	}
	getError := get(httpClient, path, value)
	if getError != nil {
		return getError
	}
	if options.json {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")

		return encoder.Encode(value)
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer func() { _ = writer.Flush() }()

	switch typed := value.(type) {
	case *hub.Status:
		{
			signalText := "off"
			if typed.Signal {
				signalText = fmt.Sprintf("on, port %d", typed.Port)
			}
			fmt.Fprintf(writer, "key\t%s\nsignal\t%s\nup\t%s\nnodes\t%d online of %d\njobs\t%d running, %d queued, %d finished\n",
				typed.Key, signalText, strings.TrimSuffix(ago(typed.Since), " ago"), typed.Online, typed.Nodes, typed.Running, typed.Queued, typed.Done)
		}
	case *[]hub.NodeView:
		{
			fmt.Fprintln(writer, "NODE\tTAGS\tSTATE\tSHARE\tFREE\tRUNNING\tLAST SEEN")
			for _, view := range *typed {
				state := "offline"
				switch {
				case view.Lost:
					{
						state = "lost"
					}
				case view.Online:
					{
						state = "online"
					}
				}
				free := "-"
				if view.Free != nil {
					free = fmt.Sprint(*view.Free)
				}
				share := map[bool]string{true: "on", false: "off"}[view.Share]
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", view.Addr, strings.Join(view.Tags, " "), state, share, free, view.Running, ago(view.LastSeen))
			}
		}
	case *[]hub.Record:
		{
			fmt.Fprintln(writer, "JOB\tTAG\tSTATE\tSTEP\tNODE\tSUBMITTED\tMESSAGE")
			for _, record := range *typed {
				step := fmt.Sprintf("%d/%d", min(record.Step+1, len(record.Steps)), len(record.Steps))
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", record.ID, record.Job.Tag, record.State, step, dash(record.Node), ago(record.Submitted), record.Message)
			}
		}
	case *[]history.Entry:
		{
			writeHistory(writer, *typed, false)
		}
	case *hub.JobView:
		{
			fmt.Fprintf(writer, "job\t%s\ntag\t%s\nstate\t%s\nmessage\t%s\nnode\t%s\nattempt\t%d of %d\nsubmitted\t%s\n\n",
				typed.ID, typed.Job.Tag, typed.State, dash(typed.Message), dash(typed.Node), typed.Attempt, typed.Job.Retries+1, ago(typed.Submitted))
			fmt.Fprintln(writer, "STEP\tCMD\tSTATE\tTOOK\tMESSAGE")
			for i, run := range typed.Steps {
				took := "-"
				if !run.Start.IsZero() && !run.End.IsZero() {
					took = run.End.Sub(run.Start).Round(time.Millisecond).String()
				}
				fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\n", i+1, run.Cmd, run.State, took, run.Message)
			}
			fmt.Fprintln(writer, "\nFILE\tSIZE\tMODIFIED")
			for _, file := range typed.Files {
				fmt.Fprintf(writer, "%s\t%d\t%s\n", file.Path, file.Size, time.UnixMilli(int64(file.Modified)).Local().Format("2006-01-02 15:04:05"))
			}
		}
	}

	return nil
}
