package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diphyx/grid/v2/pkg/wire"
)

func client(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(dialContext, "unix", socket)
	}}}
}

func get(httpClient *http.Client, path string, out any) error {
	response, getError := httpClient.Get("http://grid" + path)
	if getError != nil {
		return fmt.Errorf("daemon not reachable: %w", getError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(response.Body)

		return errors.New(strings.TrimSpace(string(content)))
	}

	return json.NewDecoder(response.Body).Decode(out)
}

func tail(httpClient *http.Client, options flags) {
	if len(options.pos) != 1 {
		fail(errors.New("tail: needs an id"))
	}
	follow(httpClient, options.pos[0], options.after, options.json)
}

// follow prints a job's events until the stream ends; it returns the last hub job state seen.
func follow(httpClient *http.Client, id, after string, asJSON bool) string {
	path := "/tail/" + id
	if after != "" {
		path += "?after=" + after
	}
	response, getError := httpClient.Get("http://grid" + path)
	if getError != nil {
		fail(fmt.Errorf("daemon not reachable: %w", getError))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(response.Body)
		fail(errors.New(strings.TrimSpace(string(content))))
	}
	last := ""
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var event wire.JSONEvent
		unmarshalError := json.Unmarshal(scanner.Bytes(), &event)
		if unmarshalError != nil {
			continue
		}
		data, _ := event.Data.(map[string]any)
		if event.Type == "job" {
			last = fmt.Sprint(data["state"])
		}
		if asJSON {
			fmt.Println(scanner.Text())

			continue
		}
		stamp := time.UnixMilli(int64(event.Time)).Local().Format("15:04:05.000")
		prefix, text := "evt", ""
		switch event.Type {
		case "log":
			{
				prefix, text = fmt.Sprint(data["stream"]), fmt.Sprint(data["line"])
			}
		case "state":
			{
				prefix, text = "cmd", fmt.Sprint(data["state"])+" "+fmt.Sprint(data["cmd"])
			}
		case "job":
			{
				prefix, text = "job", fmt.Sprint(data["state"])
			}
		case "note":
			{
				prefix, text = "hub", fmt.Sprint(data["text"])
			}
		default:
			{
				text = eventLine(event)
			}
		}
		message, isSet := data["message"]
		if (event.Type == "state" || event.Type == "job") && isSet && fmt.Sprint(message) != "" {
			text += "  " + fmt.Sprint(message)
		}
		fmt.Printf("%s %5d %s  %s\n", stamp, event.Seq, prefix, text)
	}
	// a stream cut short is an error, not the end of the job
	scanError := scanner.Err()
	if scanError != nil {
		fail(fmt.Errorf("tail: %w", scanError))
	}

	return last
}

// eventLine shows a cmd's own event as it was written: name, then its pairs, sorted by key.
func eventLine(event wire.JSONEvent) string {
	name := strings.TrimPrefix(event.Type, wire.EVENT_PREFIX)
	pairs, isPairs := event.Data.(map[string]any)
	if !isPairs {
		if event.Data == nil {
			return name
		}
		content, _ := json.Marshal(event.Data)

		return name + " " + string(content)
	}
	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{name}
	for _, key := range keys {
		value := fmt.Sprint(pairs[key])
		if value == "" || strings.ContainsAny(value, " \"\\") {
			value = strconv.Quote(value)
		}
		parts = append(parts, key+"="+value)
	}

	return strings.Join(parts, " ")
}
