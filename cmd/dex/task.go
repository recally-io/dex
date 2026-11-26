package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/api"
)

func taskCommand(ctx context.Context, args []string) error {
	return runTask(ctx, args, os.Stdout)
}

func runTask(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: dex task attach|read|post|heartbeat|finish [options]\n  attach uses DEX_TOKEN and prints a one-time Task token.\n  Other commands use DEX_TASK_TOKEN.")
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	action := args[0]
	f := flags("task " + action)
	server := f.String("server", cmp.Or(os.Getenv("DEX_TASK_SERVER"), "http://127.0.0.1:8787"), "dex server URL")
	var path string
	var body any
	var prepare func() error
	token := os.Getenv("DEX_TASK_TOKEN")
	switch action {
	case "attach":
		token = os.Getenv("DEX_TOKEN")
		task := f.String("task", "", "Task key")
		resume := f.String("resume", "", "Resume this execution ID and replace its Task token")
		harness := f.String("harness", "", "Harness: claude-code, codex, opencode, or custom (default custom on attach)")
		cwd := f.String("cwd", "", "External workspace path (default current directory on attach)")
		branch := f.String("branch", "", "Git branch")
		session := f.String("session", "", "External session ID")
		machine := f.String("machine", "", "Registered Runner ID; omit for a self-reported host")
		seconds := f.Int64("lease-seconds", 0, "Lease duration in seconds (default 120)")
		prepare = func() error {
			if (*task == "") == (*resume == "") {
				return fmt.Errorf("%w: specify exactly one of --task and --resume", errUsage)
			}
			b := api.TaskExecutionAttach{MachineId: taskString(*machine), GitBranch: taskString(*branch), ExternalSessionId: taskString(*session), LeaseSeconds: taskSeconds(*seconds)}
			if *resume != "" {
				path = "/api/tasks/executions/" + url.PathEscape(*resume) + "/resume"
			} else {
				path = "/api/tasks/" + url.PathEscape(*task) + "/executions/attach"
				if *cwd == "" {
					dir, err := os.Getwd()
					if err != nil {
						return err
					}
					*cwd = dir
				}
				if *harness == "" {
					*harness = "custom"
				}
				if host, err := os.Hostname(); err == nil {
					b.HostMeta = &api.TaskExecutionHost{Hostname: taskString(host)}
				}
			}
			b.WorkspacePath = taskString(*cwd)
			if *harness != "" {
				b.Harness = new(api.TaskExecutionAttachHarness(*harness))
			}
			body = b
			return nil
		}
	case "read":
		after := f.Int64("after-seq", 0, "Read after this Timeline sequence")
		limit := f.Int64("limit", 50, "Maximum entries to return (1–100)")
		prepare = func() error {
			path = fmt.Sprintf("/api/tasks/external/task?afterSeq=%d&limit=%d", *after, *limit)
			return nil
		}
	case "post":
		kind := f.String("kind", "note", "Entry kind: note or artifact")
		content := f.String("content", "", "Entry text")
		prepare = func() error {
			path = "/api/tasks/external/entries"
			body = api.TaskEntryCreate{Kind: new(api.TaskEntryCreateKind(*kind)), Content: *content}
			return nil
		}
	case "heartbeat":
		branch := f.String("branch", "", "Update the Git branch")
		session := f.String("session", "", "Update the external session ID")
		seconds := f.Int64("lease-seconds", 0, "Lease duration in seconds (default 120)")
		prepare = func() error {
			path = "/api/tasks/external/heartbeat"
			b := api.TaskExecutionHeartbeat{LeaseSeconds: taskSeconds(*seconds)}
			f.Visit(func(option *flag.Flag) {
				switch option.Name {
				case "branch":
					b.GitBranch = branch
				case "session":
					b.ExternalSessionId = session
				}
			})
			body = b
			return nil
		}
	case "finish":
		outcome := f.String("outcome", "", "Execution outcome: completed or failed")
		summary := f.String("summary", "", "Execution summary")
		prepare = func() error {
			path = "/api/tasks/external/finish"
			body = api.TaskExecutionFinish{Outcome: api.TaskExecutionFinishOutcome(*outcome), Summary: taskString(*summary)}
			return nil
		}
	default:
		return fmt.Errorf("%w: unknown task command", errUsage)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errors.Join(errUsage, err)
	}
	if f.NArg() != 0 {
		return fmt.Errorf("%w: use named options; credentials come from the environment", errUsage)
	}
	if action != "attach" && token == "" {
		return errors.New("set DEX_TASK_TOKEN")
	}
	if err := prepare(); err != nil {
		return err
	}
	target, err := url.Parse(*server)
	if err != nil || target.Host == "" || target.Scheme != "http" && target.Scheme != "https" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return fmt.Errorf("%w: invalid --server URL", errUsage)
	}
	method := http.MethodGet
	var payload []byte
	if body != nil {
		method = http.MethodPost
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	//nolint:gosec // G704: the CLI operator explicitly selects the dex server.
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(target.String(), "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req) //nolint:gosec // G704: use the operator's server without following redirects.
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		var detail api.Error
		if res.StatusCode == http.StatusConflict && json.UnmarshalRead(io.LimitReader(res.Body, 4096), &detail) == nil && detail.Error == "claim_lost" {
			return errors.New("claim_lost: attach --resume to get a new Task token")
		}
		return fmt.Errorf("task request failed: %s", http.StatusText(res.StatusCode))
	}
	_, err = io.Copy(out, res.Body)
	return err
}

func taskString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func taskSeconds(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}
