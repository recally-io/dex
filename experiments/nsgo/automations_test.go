package main

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestUserCreatesPausedIntervalAutomation(t *testing.T) {
	t.Parallel()

	// Given a name, prompt and interval from the creation form.
	input, err := automationsNewInput("  检查文档  ", "  汇总变更  ", " 30 ")
	if err != nil {
		t.Fatal(err)
	}

	// When the form prepares the create request, it uses the server's fields.
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name     string `json:"name"`
		Prompt   string `json:"prompt"`
		TimeZone string `json:"timeZone"`
		Session  string `json:"session"`
		Enabled  *bool  `json:"enabled"`
		Schedule struct {
			Kind    string `json:"kind"`
			Minutes int    `json:"minutes"`
		} `json:"schedule"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}

	// Then the bot only runs on demand until the user enables the schedule.
	if got.Name != "检查文档" || got.Prompt != "汇总变更" || got.TimeZone != "UTC" || got.Session != "new" ||
		got.Enabled == nil || *got.Enabled || got.Schedule.Kind != "every" || got.Schedule.Minutes != 30 {
		t.Fatalf("unexpected create request: %s", body)
	}
}

func TestUserCannotCreateAutomationWithInvalidFields(t *testing.T) {
	t.Parallel()

	// Given incomplete fields or an interval outside the server's range.
	for _, fields := range [][3]string{{"", "检查", "30"}, {"报告", " ", "30"}, {"报告", "检查", "4"}, {"报告", "检查", "10081"}, {"报告", "检查", "每小时"}} {
		// When the user submits, the form must report an error before sending.
		if _, err := automationsNewInput(fields[0], fields[1], fields[2]); err == nil {
			t.Errorf("invalid form accepted: %q", fields)
		}
	}
}

func TestUserTogglePreservesExistingAutomationSettings(t *testing.T) {
	t.Parallel()

	// Given an existing weekly task on a specific runner and session.
	const existing = `{"id":"job","name":"报告","prompt":"检查","cwd":"/work","schedule":{"kind":"weekly","days":[1,5],"time":"09:30"},"timeZone":"Asia/Shanghai","session":"session","targetSessionId":45,"enabled":true,"runnerId":"project-runner","notification":{"mode":"target","targetId":"channel"}}`
	var current automationsEntry
	if err := json.Unmarshal([]byte(existing), &current); err != nil {
		t.Fatal(err)
	}

	// When the user only pauses the schedule, the update must keep other fields.
	input := current.automationsInput
	input.Enabled = false
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(existing), &want); err != nil {
		t.Fatal(err)
	}
	delete(want, "id")
	delete(want, "notification") // Dex retains the route when the field is absent.
	want["enabled"] = false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pause changed settings: %s", body)
	}
}
