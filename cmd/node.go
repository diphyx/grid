package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/diphyx/grid/v2/pkg/history"
	"github.com/diphyx/grid/v2/pkg/node"
	"github.com/diphyx/grid/v2/pkg/wire"
)

// runNode is the node daemon.
func runNode(options flags) {
	config, loadConfigError := node.LoadConfig(options.config)
	if loadConfigError != nil {
		fail(loadConfigError)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	self, loadConfigError := node.New(config, log)
	if loadConfigError != nil {
		fail(loadConfigError)
	}
	listenError := self.Listen()
	if listenError != nil {
		fail(listenError)
	}
	go func() {
		serveLocalError := self.ServeLocal()
		if serveLocalError != nil {
			log.Error("local view off", "socket", config.Socket, "err", serveLocalError)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Debug("stopping")
		self.Close()
	}()
	serveError := self.Serve()
	if serveError != nil {
		fail(serveError)
	}
}

// view

func view(command string, options flags) {
	httpClient := client(options.socket)
	if command == "tail" {
		tail(httpClient, options)

		return
	}
	if (command == "job") && len(options.pos) != 1 {
		fail(errors.New("job: needs an id"))
	}
	for {
		var builder strings.Builder
		showError := show(httpClient, command, options, &builder)
		if showError != nil {
			fail(showError)
		}
		if options.watch {
			fmt.Print("\033[H\033[2J")
		}
		fmt.Print(builder.String())
		if !options.watch {
			return
		}
		time.Sleep(time.Second)
	}
}

func show(httpClient *http.Client, command string, options flags, out io.Writer) error {
	var value any
	path := "/" + command
	switch command {
	case "status":
		{
			value = &node.Status{}
		}
	case "jobs":
		{
			value = &[]node.Meta{}
		}
	case "history":
		{
			path, value = "/history?kind="+url.QueryEscape(options.kind), &[]history.Entry{}
		}
	case "job":
		{
			path, value = "/job/"+options.pos[0], &node.JobView{}
		}
	}
	getError := get(httpClient, path, value)
	if getError != nil {
		return getError
	}
	if options.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")

		return enc.Encode(value)
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer writer.Flush()

	switch typed := value.(type) {
	case *node.Status:
		{
			hub := "signal off"
			if typed.Signal {
				hub = typed.Hub + "  " + map[bool]string{true: "connected", false: "not connected"}[typed.Online]
			}
			share := "off"
			if typed.Share.Limit > 0 {
				share = fmt.Sprintf("%d, %s of %s", typed.Share.Shares, wire.Size{Bytes: typed.Share.Used}, wire.Size{Bytes: typed.Share.Limit})
			}
			fmt.Fprintf(writer, "addr\t%s\nkey\t%s\ntags\t%s\njobs\t%d free of %d\nshare\t%s\nup\t%s\nhub\t%s\n",
				typed.Addr, typed.Key, strings.Join(typed.Tags, " "), typed.Free, typed.Limit, share, strings.TrimSuffix(ago(typed.Since), " ago"), hub)
		}
	case *[]node.Meta:
		{
			fmt.Fprintln(writer, "JOB\tTAG\tCMD\tSTATE\tOPENED\tUSED")
			for _, meta := range *typed {
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", meta.ID, meta.Tag, dash(meta.State.Cmd), meta.State.State, ago(meta.Opened), ago(meta.Used))
			}
		}
	case *[]history.Entry:
		{
			writeHistory(writer, *typed)
		}
	case *node.JobView:
		{
			fmt.Fprintf(writer, "job\t%s\ntag\t%s\nstate\t%s\nmessage\t%s\nopened\t%s\nused\t%s\n\n",
				typed.ID, typed.Tag, typed.State.State, dash(typed.State.Message), ago(typed.Opened), ago(typed.Used))
			fmt.Fprintln(writer, "CMD\tSTATE\tSTART\tTOOK\tMESSAGE")
			for _, run := range typed.Runs {
				took := "…"
				if !run.End.IsZero() {
					took = run.End.Sub(run.Start).Round(time.Millisecond).String()
				}
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", run.Cmd, run.State, run.Start.Local().Format("15:04:05"), took, run.Message)
			}
			fmt.Fprintln(writer, "\nFILE\tSIZE\tMODIFIED")
			for _, file := range typed.Files {
				fmt.Fprintf(writer, "%s\t%d\t%s\n", file.Path, file.Size, time.UnixMilli(int64(file.Modified)).Local().Format("2006-01-02 15:04:05"))
			}
		}
	}

	return nil
}
