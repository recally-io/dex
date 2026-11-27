package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type automationsTarget struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Main  bool   `json:"main"`
}

type automationsDraft struct {
	automationsInput
	Interval, Once string
}

const automationsDateTime = "2006-01-02T15:04"

func automationsDraftFrom(current *automationsEntry, zone string) automationsDraft {
	draft := automationsDraft{automationsInput: automationsInput{
		Schedule: automationsSchedule{Kind: "daily", Time: "09:00", Days: []int{1, 2, 3, 4, 5}},
		TimeZone: zone, Session: "new", Enabled: true, Notification: &automationsNotification{Mode: "default"},
	}, Interval: "30"}
	if current == nil {
		return draft
	}
	draft.automationsInput = current.automationsInput
	if draft.Schedule.Time == "" {
		draft.Schedule.Time = "09:00"
	}
	if len(draft.Schedule.Days) == 0 {
		draft.Schedule.Days = []int{1, 2, 3, 4, 5}
	}
	if draft.Schedule.Minutes > 0 {
		draft.Interval = strconv.Itoa(draft.Schedule.Minutes)
	}
	if draft.Schedule.At > 0 {
		loc, err := time.LoadLocation(draft.TimeZone)
		if err != nil {
			loc = time.UTC
		}
		draft.Once = time.UnixMilli(draft.Schedule.At).In(loc).Format(automationsDateTime)
	}
	if draft.Notification == nil {
		draft.Notification = &automationsNotification{Mode: "none"}
	}
	return draft
}

func automationsFormSchedule(d automationsDraft) (automationsSchedule, error) {
	loc, err := time.LoadLocation(d.TimeZone)
	if err != nil || d.TimeZone == "" || d.TimeZone == "Local" {
		return automationsSchedule{}, errors.New("Choose a valid time zone.")
	}
	r := automationsSchedule{Kind: d.Schedule.Kind}
	switch r.Kind {
	case "every":
		v, err := strconv.Atoi(strings.TrimSpace(d.Interval))
		if err != nil || v < 5 || v > 10080 {
			return r, errors.New("Enter an interval from 5 to 10080 minutes.")
		}
		r.Minutes = v
	case "daily", "weekly":
		v, err := time.Parse("15:04", strings.TrimSpace(d.Schedule.Time))
		if err != nil {
			return r, errors.New("Enter a time in HH:MM format.")
		}
		r.Time = v.Format("15:04")
		if r.Kind == "weekly" {
			if len(d.Schedule.Days) == 0 {
				return r, errors.New("Choose at least one day.")
			}
			r.Days = slices.Sorted(slices.Values(d.Schedule.Days))
		}
	case "once":
		v, err := time.ParseInLocation(automationsDateTime, strings.TrimSpace(d.Once), loc)
		if err != nil || v.UnixMilli() <= 0 || v.Format(automationsDateTime) != strings.TrimSpace(d.Once) {
			return r, errors.New("Enter a valid date and time in the selected time zone.")
		}
		r.At = v.UnixMilli()
	default:
		return r, errors.New("Choose a repeat schedule.")
	}
	return r, nil
}

func automationsFormInput(d automationsDraft, targets []automationsTarget) (automationsInput, error) {
	v := d.automationsInput
	v.Name, v.Prompt, v.Cwd = strings.TrimSpace(v.Name), strings.TrimSpace(v.Prompt), strings.TrimSpace(v.Cwd)
	if n := utf8.RuneCountInString(v.Name); n < 1 || n > 100 {
		return v, errors.New("Name must have 1 to 100 characters.")
	}
	if n := utf8.RuneCountInString(v.Prompt); n < 1 || n > 32000 {
		return v, errors.New("Prompt must have 1 to 32000 characters.")
	}
	switch v.Session {
	case "session":
		if v.TargetSessionID <= 0 || !slices.ContainsFunc(targets, func(t automationsTarget) bool { return t.ID == v.TargetSessionID }) {
			return v, errors.New("Choose an available target session.")
		}
		v.Cwd, v.RunnerID = "", ""
	case "new", "reuse":
		v.TargetSessionID = 0
	default:
		return v, errors.New("Choose how each run opens a session.")
	}
	schedule, err := automationsFormSchedule(d)
	v.Schedule = schedule
	return v, err
}

type automationsRunnerOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

type automationsRecipient struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Peer     string `json:"peer"`
}

type automationsFormOptions struct {
	Targets                               []automationsTarget
	Runners                               []automationsRunnerOption
	Notifications                         []automationsRecipient
	TargetErr, RunnerErr, NotificationErr error
}

func automationsLoadFormOptions(ctx context.Context, bot string) automationsFormOptions {
	var v automationsFormOptions
	var requests sync.WaitGroup
	requests.Go(func() {
		var r struct {
			Targets []automationsTarget `json:"targets"`
		}
		v.TargetErr = requestJSON(ctx, http.MethodGet, automationsPath(bot, "")+"/targets", nil, &r)
		v.Targets = r.Targets
	})
	requests.Go(func() {
		var r struct {
			Runners []automationsRunnerOption `json:"runners"`
		}
		v.RunnerErr = requestJSON(ctx, http.MethodGet, botPath(bot)+"/runners", nil, &r)
		v.Runners = r.Runners
	})
	requests.Go(func() {
		var r struct {
			Targets []automationsRecipient `json:"targets"`
		}
		v.NotificationErr = requestJSON(ctx, http.MethodGet, botPath(bot)+"/notification-targets", nil, &r)
		v.Notifications = r.Targets
	})
	requests.Wait()
	return v
}

func automationsForm(cx *core.Scope, bot string, existing *automationsEntry, close func(), saved func(automationsEntry)) core.Node {
	zone := time.Local.String()
	if zone == "Local" || zone == "" {
		zone = "UTC"
	}
	draft := core.NewSignalFunc(cx, automationsDraftFrom(existing, zone), nil)
	kind := core.NewMemo(cx, func() string { return draft.Get().Schedule.Kind })
	options := core.NewSignal(cx, (*automationsFormOptions)(nil))
	loading := core.NewSignal(cx, false)
	saving := core.NewSignal(cx, false)
	composing := core.NewSignal(cx, false)
	saveError := core.NewSignal(cx, "")
	edit := func(f func(*automationsDraft)) {
		if !saving.Get() {
			d := draft.Get()
			f(&d)
			draft.Set(d)
			saveError.Set("")
		}
	}
	load := func() {
		if loading.Get() {
			return
		}
		loading.Set(true)
		automationsWork(cx, func(ctx context.Context) (automationsFormOptions, error) {
			return automationsLoadFormOptions(ctx, bot), nil
		}, func(v automationsFormOptions, _ error) { options.Set(new(v)); loading.Set(false) })
	}
	cx.Post(load)
	targets := func() []automationsTarget {
		if v := options.Get(); v != nil && v.TargetErr == nil {
			return v.Targets
		}
		return nil
	}
	valid := func() bool { _, err := automationsFormInput(draft.Get(), targets()); return err == nil }
	submit := func() {
		if saving.Get() || composing.Get() {
			return
		}
		input, err := automationsFormInput(draft.Get(), targets())
		if err != nil {
			saveError.Set(err.Error())
			return
		}
		method, path := http.MethodPost, automationsPath(bot, "")
		if existing != nil {
			method, path = http.MethodPut, automationsPath(bot, existing.ID)
		}
		saving.Set(true)
		saveError.Set("")
		automationsWork(cx, func(ctx context.Context) (automationsEntry, error) {
			var result automationsEntry
			err := requestJSON(ctx, method, path, input, &result)
			return result, err
		}, func(result automationsEntry, err error) {
			saving.Set(false)
			if err != nil {
				saveError.Set("Could not save automation: " + err.Error())
				return
			}
			saved(result)
		})
	}
	colors := ui.ColorsOf(cx)
	width := core.Style{Width: core.Pct(100)}
	field := func(label, hint string, item core.Node) core.Node {
		children := []core.Node{core.Text{Content: label, Style: core.Style{FontSize: 12.5, FontWeight: core.FontWeightMedium}}, item}
		if hint != "" {
			children = append(children, automationsQuiet(colors, hint))
		}
		return core.View{Style: core.Style{Gap: 5}, Children: children}
	}
	input := func(id, label string, value func(automationsDraft) string, change func(*automationsDraft, string), multi bool, kb core.KeyboardType) core.Node {
		s := width
		if multi {
			s.Height = core.Pt(88)
		}
		return ui.Input{TestID: id, Label: label, Style: s, Value: func() string { return value(draft.Get()) }, ReadonlyFunc: saving.Get, Multiline: multi, KeyboardType: kb,
			OnCompositionChange: func(v core.Composition) { composing.Set(v.Composing) }, OnChange: func(v string) { edit(func(d *automationsDraft) { change(d, v) }) }}
	}
	pick := func(id, label string, opts []ui.Option[string], current func(automationsDraft) string, change func(*automationsDraft, string)) ui.Select[string] {
		return ui.Select[string]{TestID: id, Label: label, Style: width, Options: opts, Value: func() string { return current(draft.Get()) }, Disabled: saving.Get, OnChange: func(v string) { edit(func(d *automationsDraft) { change(d, v) }) }}
	}
	chooseRunner := pick("automations-runner", "Runner", nil, func(d automationsDraft) string { return d.RunnerID }, func(d *automationsDraft, id string) {
		if d.RunnerID != id {
			d.RunnerID, d.Cwd = id, ""
		}
	})
	chooseRunner.OptionsFunc = func() []ui.Option[string] {
		items := []ui.Option[string]{{Label: "Follow bot (default)"}}
		current := draft.Get().RunnerID
		found := current == ""
		kept := current + " (removed)"
		if data := options.Get(); data != nil {
			for _, r := range data.Runners {
				if r.ID == current {
					kept = r.Name
				}
				if r.Default {
					items[0].Label = "Follow bot (" + r.Name + ")"
					continue
				}
				if r.Kind == "ssh" || r.Kind == "local" {
					found = found || r.ID == current
					items = append(items, ui.Option[string]{Value: r.ID, Label: r.Name})
				}
			}
		}
		if !found {
			items = append(items, ui.Option[string]{Value: current, Label: kept})
		}
		return items
	}
	notify := pick("automations-notification", "Send result to", nil, func(d automationsDraft) string {
		if d.Notification.Mode == "target" {
			return "target:" + d.Notification.TargetID
		}
		return d.Notification.Mode
	}, func(d *automationsDraft, key string) {
		if id, ok := strings.CutPrefix(key, "target:"); ok {
			d.Notification = &automationsNotification{Mode: "target", TargetID: id}
		} else {
			d.Notification = &automationsNotification{Mode: key}
		}
	})
	notify.OptionsFunc = func() []ui.Option[string] {
		items := []ui.Option[string]{{Value: "none", Label: "No notification"}, {Value: "default", Label: "Primary channel"}}
		current := draft.Get().Notification
		found := current.Mode != "target"
		if data := options.Get(); data != nil {
			for _, dest := range data.Notifications {
				found = found || dest.ID == current.TargetID
				items = append(items, ui.Option[string]{Value: "target:" + dest.ID, Label: dest.Platform + ": " + dest.Peer})
			}
		}
		if !found {
			items = append(items, ui.Option[string]{Value: "target:" + current.TargetID, Label: "Saved recipient (unavailable)", Disabled: func() bool { return true }})
		}
		return items
	}
	chooseZone := pick("automations-time-zone", "Time zone", nil, func(d automationsDraft) string { return d.TimeZone }, func(d *automationsDraft, v string) { d.TimeZone = v })
	zones := slices.Compact(slices.Sorted(slices.Values([]string{zone, draft.GetUntracked().TimeZone, "UTC", "Asia/Shanghai", "Asia/Tokyo", "Europe/London", "Europe/Berlin", "America/New_York", "America/Los_Angeles"})))
	for _, z := range zones {
		chooseZone.Options = append(chooseZone.Options, ui.Option[string]{Value: z, Label: z})
	}
	title := "New automation"
	if existing != nil {
		title = "Edit automation"
	}
	cancel := func() {
		if !saving.Get() {
			close()
		}
	}
	return ui.Dialog{TestID: "automations-form", Title: title, Description: "Set a prompt and a schedule, and choose where the automation runs.", Open: func() bool { return true },
		Inset: 8, StyleFunc: func() core.Style { return core.Style{Width: core.Pt(min(500, cx.Env().Window().Width))} }, OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				cancel()
			}
		},
		Children: []core.Node{core.Scroll{Style: core.Style{FlexShrink: 1, MinHeight: core.Pt(0), Gap: 14}, StyleFunc: func() core.Style {
			return core.Style{MaxHeight: core.Pt(max(100, cx.Env().Window().Height-cx.Env().KeyboardHeight()-200))}
		}, Children: []core.Node{
			field("Name", "", input("automations-name", "Name", func(d automationsDraft) string { return d.Name }, func(d *automationsDraft, v string) { d.Name = v }, false, core.KeyboardDefault)),
			field("Prompt", "Sent to the bot as a user message on each run.", input("automations-prompt", "Prompt", func(d automationsDraft) string { return d.Prompt }, func(d *automationsDraft, v string) { d.Prompt = v }, true, core.KeyboardDefault)),
			core.Show{When: func() bool { return draft.Get().Session != "session" && len(chooseRunner.OptionsFunc()) > 1 }, Then: func(*core.Scope) core.Node {
				return field("Runner", "A run is skipped when its Runner is removed or unreachable.", chooseRunner)
			}},
			core.Show{When: func() bool { return draft.Get().Session != "session" }, Then: func(*core.Scope) core.Node {
				return field("Project directory", "Use an absolute directory on the automation's Runner. Leave empty to use its workspace.", input("automations-project", "Project directory", func(d automationsDraft) string { return d.Cwd }, func(d *automationsDraft, v string) { d.Cwd = v }, false, core.KeyboardDefault))
			}},
			field("Send result to", "Notifications go only to an authorized recipient.", notify),
			field("Repeat", "", pick("automations-repeat", "Repeat", []ui.Option[string]{{Value: "every", Label: "Interval"}, {Value: "daily", Label: "Daily"}, {Value: "weekly", Label: "Weekly"}, {Value: "once", Label: "Once"}}, func(d automationsDraft) string { return d.Schedule.Kind }, func(d *automationsDraft, v string) { d.Schedule.Kind = v })),
			core.Dynamic(func(*core.Scope) core.Node {
				switch kind.Get() {
				case "every":
					return field("Minutes between runs", "Minimum: 5 minutes.", input("automations-interval", "Minutes between runs", func(d automationsDraft) string { return d.Interval }, func(d *automationsDraft, v string) { d.Interval = v }, false, core.KeyboardNumber))
				case "once":
					return field("Date and time", "Use YYYY-MM-DDTHH:MM in the selected time zone.", input("automations-once", "Date and time", func(d automationsDraft) string { return d.Once }, func(d *automationsDraft, v string) { d.Once = v }, false, core.KeyboardDefault))
				default:
					return field("Time", "Use HH:MM (24-hour).", input("automations-time", "Time", func(d automationsDraft) string { return d.Schedule.Time }, func(d *automationsDraft, v string) { d.Schedule.Time = v }, false, core.KeyboardDefault))
				}
			}),
			core.Show{When: func() bool { return draft.Get().Schedule.Kind == "weekly" }, Then: func(*core.Scope) core.Node {
				var nodes []core.Node
				for i, day := range automationsWeekdays {
					n := i + 1
					nodes = append(nodes, ui.Button{Text: day, TestID: "automations-weekday-" + strconv.Itoa(n), Size: ui.SizeSmall, Variant: ui.ButtonOutline, Disabled: saving.Get, TextFunc: func() string {
						if slices.Contains(draft.Get().Schedule.Days, n) {
							return day + " ✓"
						}
						return day
					}, OnPress: func() {
						edit(func(d *automationsDraft) {
							days := slices.Clone(d.Schedule.Days)
							if j := slices.Index(days, n); j >= 0 {
								days = slices.Delete(days, j, j+1)
							} else {
								days = append(days, n)
							}
							d.Schedule.Days = days
						})
					}})
				}
				return field("Days of the week", "", core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 6}, Children: nodes})
			}},
			field("Time zone", "", chooseZone),
			field("Session for each run", "", pick("automations-session-mode", "Session for each run", []ui.Option[string]{{Value: "new", Label: "New session each run"}, {Value: "reuse", Label: "One session for this automation"}, {Value: "session", Label: "Existing session"}}, func(d automationsDraft) string { return d.Session }, func(d *automationsDraft, v string) { d.Session = v })),
			core.Show{When: func() bool { return draft.Get().Session == "session" }, Then: func(*core.Scope) core.Node {
				return field("Target session", "Only writable, unarchived sessions of this bot are listed.", ui.Select[int64]{TestID: "automations-target", Label: "Target session", Style: width, Value: func() int64 { return draft.Get().TargetSessionID }, Disabled: saving.Get, OnChange: func(id int64) { edit(func(d *automationsDraft) { d.TargetSessionID = id }) }, OptionsFunc: func() []ui.Option[int64] {
					label := "Choose a session"
					if options.Get() == nil {
						label = "Loading sessions…"
					}
					items := []ui.Option[int64]{{Label: label}}
					current := draft.Get().TargetSessionID
					found := current == 0
					for _, t := range targets() {
						label := "Main"
						if !t.Main {
							label = cmp.Or(t.Title, "Untitled session") + " · #" + strconv.FormatInt(t.ID, 10)
						}
						found = found || current == t.ID
						items = append(items, ui.Option[int64]{Value: t.ID, Label: label})
					}
					if !found {
						items = append(items, ui.Option[int64]{Value: current, Label: "Saved target is unavailable; choose a session", Disabled: func() bool { return true }})
					}
					return items
				}})
			}},
			core.Show{When: func() bool {
				v := options.Get()
				return v != nil && (v.NotificationErr != nil || (draft.Get().Session == "session" && v.TargetErr != nil) || (draft.Get().Session != "session" && v.RunnerErr != nil))
			}, Then: func(*core.Scope) core.Node {
				return automationsFailure("Could not load execution options", func() error {
					v := options.Get()
					var err error
					if draft.Get().Session == "session" {
						err = v.TargetErr
					} else {
						err = v.RunnerErr
					}
					return errors.Join(v.NotificationErr, err)
				}, load)
			}},
			core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifySpaceBetween}, Children: []core.Node{core.Text{Content: "Enabled"}, ui.Switch{TestID: "automations-enabled", Label: "Enabled", Size: ui.SizeSmall, Checked: func() bool { return draft.Get().Enabled }, OnChange: func(v bool) { edit(func(d *automationsDraft) { d.Enabled = v }) }, Disabled: saving.Get}}},
			automationsPreview(cx, draft.Get),
			automationsStatus("automations-form-status", saveError.Get),
		}}}, Footer: []core.Node{
			ui.Button{Text: "Cancel", TestID: "automations-cancel", Variant: ui.ButtonSecondary, Disabled: saving.Get, OnPress: cancel},
			ui.Button{Text: "Save automation", TestID: "automations-submit", Busy: saving.Get, BusyLabel: "Saving…", Disabled: func() bool { return !valid() || composing.Get() }, OnPress: submit},
		}}
}

func automationsPreview(cx *core.Scope, draft func() automationsDraft) core.Node {
	type scheduleKey struct {
		Kind, Time, Interval, Once, Zone string
		Days                             uint8
	}
	key := core.NewMemo(cx, func() scheduleKey {
		d := draft()
		var days uint8
		for _, day := range d.Schedule.Days {
			days |= 1 << uint(day-1)
		}
		return scheduleKey{d.Schedule.Kind, d.Schedule.Time, d.Interval, d.Once, d.TimeZone, days}
	})
	status := core.NewSignal(cx, "")
	var generation int
	cx.Effect(func() {
		k := key.Get()
		generation++
		attempt := generation
		d := core.Untracked(cx, draft)
		schedule, err := automationsFormSchedule(d)
		if err != nil {
			status.Set("Complete the schedule to see the next runs. " + err.Error())
			return
		}
		status.Set("Loading preview…")
		cx.AfterFunc(350*time.Millisecond, func() {
			automationsWork(cx, func(ctx context.Context) ([]int64, error) {
				var result struct {
					NextRuns []int64 `json:"nextRuns"`
				}
				err := requestJSON(ctx, http.MethodPost, "/api/automations/preview", struct {
					Schedule automationsSchedule `json:"schedule"`
					TimeZone string              `json:"timeZone"`
				}{schedule, k.Zone}, &result)
				return result.NextRuns, err
			}, func(times []int64, err error) {
				if attempt != generation {
					return
				}
				if err != nil {
					status.Set("Could not preview schedule: " + err.Error())
					return
				}
				if len(times) == 0 {
					status.Set("No future runs.")
					return
				}
				var display []string
				for _, at := range times {
					display = append(display, automationsTime(at, k.Zone))
				}
				status.Set(strings.Join(display, "\n"))
			})
		})
	})
	colors := ui.ColorsOf(cx)
	return core.View{Style: core.Style{Gap: 6, Padding: core.All(12), CornerRadius: 6}, StyleFunc: ui.Filled(colors.Inset), Children: []core.Node{
		core.Text{ContentFunc: func() string { return "Next runs · " + draft().TimeZone }, Style: core.Style{FontWeight: core.FontWeightMedium}},
		core.Text{ContentFunc: status.Get, TestID: "automations-preview", Role: core.RoleStatus, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
		core.Show{When: func() bool { return !draft().Enabled }, Then: func(*core.Scope) core.Node {
			return automationsQuiet(colors, "This automation is paused. Enable it to run on this schedule.")
		}},
	}}
}
