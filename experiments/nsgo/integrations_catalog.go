package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type integrationsCatalogPage[T any] struct {
	rows []T
	next string
}

func integrationsCatalogSearch[T any](cx *core.Scope, ref *core.Ref, id, label, placeholder, more string,
	fetch func(context.Context, string, string) (integrationsCatalogPage[T], error), row func(*core.Scope, T) core.Node) core.Node {
	text, term := core.NewSignal(cx, ""), core.NewSignal(cx, "")
	busy, problem, cursor := core.NewSignal(cx, false), core.NewSignal(cx, ""), core.NewSignal(cx, "")
	rows := core.NewSignalFunc(cx, []T(nil), nil)
	load := func(query, page string) {
		if query == "" || busy.Get() {
			return
		}
		if page == "" {
			term.Set(query)
			rows.Set(nil)
			cursor.Set("")
		}
		busy.Set(true)
		problem.Set("")
		work(cx, func(ctx context.Context) (integrationsCatalogPage[T], error) { return fetch(ctx, query, page) }, func(result integrationsCatalogPage[T], err error) {
			busy.Set(false)
			if err != nil {
				problem.Set(err.Error())
				return
			}
			rows.Set(append(rows.Get()[:len(rows.Get()):len(rows.Get())], result.rows...))
			cursor.Set(result.next)
		})
	}
	retry := func() { load(term.Get(), cursor.Get()) }
	search := func() { load(strings.TrimSpace(text.Get()), "") }
	return core.View{Style: core.Style{Gap: 10}, Children: []core.Node{
		core.View{Children: []core.Node{
			core.Text{Content: label, Style: core.Style{FontSize: 14, FontWeight: ui.Medium, LineHeight: 21}},
			core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
				ui.Input{Ref: ref, TestID: id + "-search", Label: label, Placeholder: placeholder, Role: core.RoleSearchField,
					Value: text.Get, OnChange: text.Set, MaxLength: 200, Style: core.Style{FlexGrow: 1, MinWidth: core.Pt(0), Height: core.Pt(36)}, OnSubmit: func(string) { search() }},
				core.Show{When: func() bool { return strings.TrimSpace(text.Get()) != "" }, Then: func(*core.Scope) core.Node {
					return ui.Button{Text: "Search", TestID: id + "-submit", Variant: ui.ButtonOutline, Disabled: busy.Get,
						Style: core.Style{Height: core.Pt(36)}, OnPress: search}
				}},
			}},
		}},
		core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node { return integrationsStatus(cx, "Searching…") }},
		core.Show{When: func() bool { return problem.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsFailure(cx, id+"-error", "Could not search the catalog", problem.Get, retry)
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			if term.Get() == "" {
				return core.Group{}
			}
			items := rows.Get()
			if len(items) == 0 {
				if busy.Get() || problem.Get() != "" {
					return core.Group{}
				}
				return integrationsMuted(cx, "No results.")
			}
			children := make([]core.Node, 0, len(items))
			for _, item := range items {
				children = append(children, row(cx, item))
			}
			return integrationsPanel(cx, children)
		}),
		core.Show{When: func() bool { return cursor.Get() != "" && problem.Get() == "" }, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: more, TestID: id + "-more", Variant: ui.ButtonOutline, Busy: busy.Get, OnPress: retry, Style: core.Style{AlignSelf: core.AlignStart}}
		}},
	}}
}

func integrationsSearchSkills(ctx context.Context, q, page string) (integrationsCatalogPage[integrationsSkillHit], error) {
	params := url.Values{"q": {q}}
	if page != "" {
		params.Set("offset", page)
	}
	var result struct {
		Skills []integrationsSkillHit `json:"skills"`
		Next   *int                   `json:"nextOffset"`
	}
	err := requestJSON(ctx, http.MethodGet, "/api/integrations/catalog/skills?"+params.Encode(), nil, &result)
	p := integrationsCatalogPage[integrationsSkillHit]{rows: result.Skills}
	if result.Next != nil {
		p.next = strconv.Itoa(*result.Next)
	}
	return p, err
}

type integrationsRegistryHit struct {
	Name        string                       `json:"name"`
	Title       string                       `json:"title"`
	Description string                       `json:"description"`
	Reason      string                       `json:"reason"`
	Addable     bool                         `json:"addable"`
	Remotes     []integrationsRegistryRemote `json:"remotes"`
}

type integrationsRegistryRemote struct {
	URL     string `json:"url"`
	Addable bool   `json:"addable"`
	Headers []struct {
		Name        string `json:"name"`
		Value       string `json:"value"`
		Description string `json:"description"`
		Required    bool   `json:"isRequired"`
		Secret      bool   `json:"isSecret"`
	} `json:"headers"`
}

func integrationsSearchRegistry(ctx context.Context, q, page string) (integrationsCatalogPage[integrationsRegistryHit], error) {
	params := url.Values{"q": {q}}
	if page != "" {
		params.Set("cursor", page)
	}
	var result struct {
		Servers []integrationsRegistryHit `json:"servers"`
		Next    string                    `json:"nextCursor"`
	}
	err := requestJSON(ctx, http.MethodGet, "/api/integrations/catalog/mcp?"+params.Encode(), nil, &result)
	return integrationsCatalogPage[integrationsRegistryHit]{result.Servers, result.Next}, err
}

func integrationsRegistryDraft(entry integrationsRegistryHit, remote integrationsRegistryRemote) integrationsMCPDraft {
	name := entry.Name
	if _, part, ok := strings.CutLast(name, "/"); ok {
		name = part
	}
	name = strings.Trim(integrationsRegistryName.ReplaceAllString(strings.ToLower(name), "-"), "-")
	d := integrationsMCPDraft{Name: cmp.Or(name[:min(len(name), 32)], "mcp-server"), URL: remote.URL, Auth: "none"}
	for _, h := range remote.Headers {
		if !h.Required {
			continue
		}
		secret := h.Secret || integrationsCredentialHeader(h.Name)
		value := h.Value
		if secret {
			value = ""
		}
		d.Headers = append(d.Headers, integrationsHeader{h.Name, value, h.Description, secret, true})
		d.Auth = "header"
	}
	return d
}

func integrationsSkillRow(cx *core.Scope, skill integrationsSkillHit, install func(integrationsSkillHit)) core.Node {
	rows := []core.Node{integrationsLabel(skill.Name), integrationsMuted(cx, skill.Description)}
	if integrationsSupportedSkill(skill.Source) {
		source := "View source"
		if skill.Author != "" {
			source = "Source by " + skill.Author
		}
		rows = append(rows, core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
			ui.Button{Text: source, Variant: ui.ButtonGhost, OnPress: func() { cx.OpenURL(skill.Source, nil) }},
			ui.Button{Text: "Install", Label: "Install " + skill.Name, TestID: "integrations-install-" + skill.ID, Variant: ui.ButtonOutline, OnPress: func() { install(skill) }},
		}})
	} else {
		rows = append(rows, integrationsMuted(cx, "No supported source link."))
	}
	return integrationsItem(rows...)
}

func integrationsRegistryRow(cx *core.Scope, entry integrationsRegistryHit, add func(integrationsMCPDraft)) core.Node {
	title := cmp.Or(entry.Title, entry.Name)
	rows := []core.Node{integrationsLabel(title), integrationsMuted(cx, entry.Description)}
	if !entry.Addable {
		return integrationsItem(append(rows, integrationsMuted(cx, entry.Reason))...)
	}
	for i, r := range entry.Remotes {
		if !r.Addable || !integrationsValidURL(r.URL, true) {
			continue
		}
		rows = append(rows, core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
			core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}, Children: []core.Node{integrationsMuted(cx, r.URL)}},
			ui.Button{Text: "Add", Label: "Add " + title, TestID: fmt.Sprintf("integrations-registry-%s-%d", entry.Name, i), Variant: ui.ButtonOutline, OnPress: func() { add(integrationsRegistryDraft(entry, r)) }},
		}})
	}
	return integrationsItem(rows...)
}
