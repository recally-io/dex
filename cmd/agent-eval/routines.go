//go:build agenteval

package main

import (
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/automation"
)

type routineExpectation struct {
	Name            string              `json:"name"`
	Schedule        automation.Schedule `json:"schedule"`
	Enabled         bool                `json:"enabled"`
	Outcome         string              `json:"outcome"`
	SummaryContains string              `json:"summaryContains"`
}

type routineState struct {
	Routine automation.Automation `json:"routine"`
	Runs    []automation.Run      `json:"runs"`
}

const automationsPath = "/api/agents/default/automations"

func (c client) routines() ([]routineState, error) {
	var list struct {
		Automations []automation.Automation `json:"automations"`
	}
	if err := c.request("GET", automationsPath, nil, &list); err != nil {
		return nil, err
	}
	states := make([]routineState, 0, len(list.Automations))
	for _, row := range list.Automations {
		path := automationsPath + "/" + row.ID
		var state routineState
		if err := c.request("GET", path, nil, &state.Routine); err != nil {
			return states, err
		}
		var history struct {
			Runs []automation.Run `json:"runs"`
		}
		if err := c.request("GET", path+"/runs?limit=100", nil, &history); err != nil {
			return states, err
		}
		state.Runs = history.Runs
		states = append(states, state)
	}
	return states, nil
}

// The eval owns a fresh agent, so every routine run belongs to this scenario.
func (c client) waitForWork(parent int64, includeRoutines bool) (ids []int64, states []routineState, err error) {
	ids = []int64{parent}
	last := "no work observed"
	for {
		var all struct {
			Chats []chat `json:"chats"`
		}
		if err = c.request("GET", "/api/agents/default/chats", nil, &all); err != nil {
			return ids, states, fmt.Errorf("wait for scenario work (%s): %w", last, err)
		}
		busy := false
		var observed []string
		for _, ch := range all.Chats {
			if ch.ID == parent || (ch.Task != nil && ch.Task.Parent == parent) {
				if !slices.Contains(ids, ch.ID) {
					ids = append(ids, ch.ID)
				}
				active := ch.Busy || (ch.Task != nil && (ch.Task.Status == "queued" || ch.Task.Status == "running"))
				busy = busy || active
				observed = append(observed, fmt.Sprintf("chat %d busy=%t", ch.ID, active))
			}
		}
		if includeRoutines {
			var saved []routineState
			saved, err = c.routines()
			if err != nil {
				return ids, states, fmt.Errorf("read scenario routines (%s): %w", last, err)
			}
			states = saved
			for _, state := range states {
				observed = append(observed, fmt.Sprintf("routine %s enabled=%t runs=%d", state.Routine.ID, state.Routine.Enabled, len(state.Runs)))
				for _, run := range state.Runs {
					busy = busy || run.Status == "running" || run.Status == "started"
					observed = append(observed, fmt.Sprintf("run %s status=%s outcome=%s reason=%s", run.ID, run.Status, run.Outcome, run.Reason))
					if run.ChatID != 0 && !slices.Contains(ids, run.ChatID) {
						ids = append(ids, run.ChatID)
					}
				}
			}
		}
		last = strings.Join(observed, "; ")
		if !busy {
			return ids, states, nil
		}
		select {
		case <-c.ctx.Done():
			return ids, states, fmt.Errorf("wait for scenario work (%s): %w", last, c.ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c client) checkRoutine(want routineExpectation, states []routineState, project, answer string) ([]check, error) {
	checks := []check{{Name: "exactly one saved routine", Pass: len(states) == 1, Detail: strconv.Itoa(len(states))}}
	if len(states) != 1 {
		return checks, nil
	}
	state := states[0]
	row := state.Routine
	encoded, err := json.Marshal(row.Schedule)
	if err != nil {
		return checks, err
	}
	schedule := string(encoded)
	checks = append(checks,
		check{Name: "saved routine name", Pass: row.Name == want.Name, Detail: row.Name},
		check{Name: "saved routine enabled", Pass: row.Enabled == want.Enabled, Detail: strconv.FormatBool(row.Enabled)},
		check{Name: "saved routine schedule", Pass: reflect.DeepEqual(row.Schedule, want.Schedule), Detail: schedule},
		check{Name: "routine inherited project", Pass: row.Cwd == project, Detail: row.Cwd},
		check{Name: "exactly one routine run", Pass: len(state.Runs) == 1, Detail: strconv.Itoa(len(state.Runs))},
	)
	if len(state.Runs) != 1 {
		return checks, nil
	}
	run := state.Runs[0]
	checks = append(checks,
		check{Name: "routine run completed", Pass: run.Status == "done", Detail: run.Status + ": " + run.Reason},
		check{Name: "routine run manual", Pass: run.DueAt == 0},
		check{Name: "routine run reported outcome", Pass: run.Outcome == want.Outcome, Detail: run.Outcome},
		check{Name: "routine run reported summary", Pass: want.SummaryContains != "" && strings.Contains(run.Summary, want.SummaryContains), Detail: run.Summary},
		check{Name: "answer references saved routine", Pass: row.ID != "" && strings.Contains(answer, row.ID)},
		check{Name: "answer references saved run", Pass: (run.ID != "" && strings.Contains(answer, run.ID)) || (run.ChatID != 0 && strings.Contains(answer, fmt.Sprintf("/a/default/c/%d", run.ChatID)))},
		check{Name: "answer reports concrete result", Pass: want.SummaryContains != "" && strings.Contains(answer, want.SummaryContains) && strings.Contains(strings.ToLower(answer), want.Outcome)},
	)
	if run.ChatID == 0 {
		return append(checks, check{Name: "routine run transcript reference", Pass: false}), nil
	}
	var saved chat
	if err := c.request("GET", "/api/agents/default/chats/"+strconv.FormatInt(run.ChatID, 10), nil, &saved); err != nil {
		return checks, err
	}
	checks = append(checks, check{Name: "routine run inherited project", Pass: saved.Cwd == project, Detail: saved.Cwd})
	return checks, nil
}
