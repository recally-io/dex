package main

import (
	"errors"
	"slices"
	"strings"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/recally-io/dex/apps/native/locales"
)

const savedOriginKey = "dex.origin"
const workspaceSettingsURL = "dex-workspace://settings"

type connectionPhase uint8

const (
	editAddress connectionPhase = iota
	switchWorkspace
	checkingServer
	clearingSignIn
	offline
	online
)

func main() { nsgo.Run(root, dexWebViewOptions()...) }

func root(cx *core.Scope) core.Node {
	ctx := cx.Context()
	mac := cx.Env().OS() == core.OSMacOS
	store := cx.Storage()
	stored, _ := store.GetItem(savedOriginKey)
	origin, valid := canonicalOrigin(stored, mac)
	if !valid || origin != stored {
		origin = ""
		store.RemoveItem(savedOriginKey)
	}

	initial := origin
	if initial == "" && mac {
		initial = macDefaultOrigin
	}
	// An unsaved Mac default may still have cookies from a previous launch.
	storeOwner := core.NewSignal(cx, initial)
	attempt := core.NewSignal(cx, initial)
	phase := core.NewSignal(cx, editAddress)
	draft := core.NewSignal(cx, initial)
	message := core.NewSignalFunc(cx, (func(*core.Scope) string)(nil), func(a, b func(*core.Scope) string) bool {
		return a == nil && b == nil
	})
	var confirmAttempt, requestedSettings bool
	var mount uint64

	clearSignIn := func(done func()) {
		phase.Set(clearingSignIn)
		message.Set(nil)
		clearDexWebView(cx, storeOwner.Get(), func(err error) {
			if err != nil {
				message.Set(locales.ClearFailed)
				phase.Set(editAddress)
				return
			}
			done()
		})
	}

	connect := func(target string, confirm bool) {
		attempt.Set(target)
		confirmAttempt = confirm
		phase.Set(checkingServer)
		message.Set(nil)
		go func() {
			err := probeDex(ctx, target, mac, nil)
			cx.Post(func() {
				if requestedSettings {
					requestedSettings = false
					phase.Set(editAddress)
					return
				}
				if err != nil {
					if errors.Is(err, errDexAuthRequired) {
						message.Set(locales.AuthRequired)
					}
					phase.Set(offline)
					return
				}
				open := func() {
					if confirm {
						if err := store.SetItem(savedOriginKey, target); err != nil {
							message.Set(locales.SaveFailed)
							phase.Set(editAddress)
							return
						}
					}
					storeOwner.Set(target)
					draft.Set(target)
					mount++
					phase.Set(online)
				}
				if owner := storeOwner.Get(); owner != "" && owner != target {
					clearSignIn(open)
					return
				}
				open()
			})
		}()
	}

	busy := func() bool { return phase.Get() == checkingServer || phase.Get() == clearingSignIn }
	submit := func() {
		if busy() {
			return
		}
		target, ok := canonicalOrigin(draft.Get(), mac)
		if !ok {
			message.Set(locales.InvalidAddress)
			return
		}
		draft.Set(target)
		connect(target, true)
	}
	retry := func() {
		if !busy() && attempt.Get() != "" {
			draft.Set(attempt.Get())
			connect(attempt.Get(), confirmAttempt)
		}
	}
	forget := func() {
		if busy() || storeOwner.Get() == "" {
			return
		}
		clearSignIn(func() {
			store.RemoveItem(savedOriginKey)
			storeOwner.Set("")
			attempt.Set("")
			draft.Set("")
			phase.Set(editAddress)
		})
	}
	if initial != "" {
		connect(initial, false)
	}

	// A system link also opens settings when an older server has no settings link.
	cx.OnOpenLinks(func(links []string) {
		if !slices.Contains(links, workspaceSettingsURL) {
			return
		}
		switch phase.Get() {
		case checkingServer:
			requestedSettings = true
		case online:
			phase.Set(switchWorkspace)
		}
	})

	label := func(text func(*core.Scope) string) func() string { return func() string { return text(cx) } }
	buttonSize := core.Style{MinHeight: core.Pt(44)}
	return core.View{
		Style:     core.Style{FlexGrow: 1, FlexShrink: 1, PaddingEnv: core.EnvAll(core.EnvSafeArea)},
		StyleFunc: func() core.Style { return core.Style{Background: cx.Palette().Background} },
		Children: []core.Node{core.Show{
			When: func() bool { return phase.Get() == online },
			Then: func(view *core.Scope) core.Node {
				target, id := attempt.Get(), mount
				return dexWebView(view, target, func(next connectionPhase) {
					if phase.Get() == online && id == mount {
						phase.Set(next)
					}
				})
			},
			Else: func(*core.Scope) core.Node {
				return core.Scroll{
					Style: core.Style{FlexGrow: 1, FlexShrink: 1, PaddingEnv: core.EnvEdges{Bottom: core.EnvKeyboard}},
					Children: []core.Node{core.View{
						Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(520), AlignSelf: core.AlignCenter, Padding: core.All(20), Gap: 12},
						Children: []core.Node{
							core.Text{ContentFunc: label(locales.Connection), Style: ui.TypeHeading},
							core.Text{ContentFunc: func() string {
								if mac {
									return locales.MacHelp(cx)
								}
								return locales.RemoteHelp(cx)
							}},
							core.Text{ContentFunc: label(locales.Address)},
							ui.Input{Value: draft.Get, Disabled: busy, OnChange: func(value string) {
								draft.Set(value)
								message.Set(nil)
							}, OnSubmit: func(value string) {
								if !busy() {
									draft.Set(value)
									submit()
								}
							}, PlaceholderFunc: label(locales.AddressPlaceholder), LabelFunc: label(locales.Address),
								KeyboardType: core.KeyboardURL, AutoCapitalize: core.AutoCapitalizeNone, NoAutoCorrect: true, ReturnKeyType: core.ReturnKeyGo,
								Style: core.Style{Height: core.Pt(44), FontSize: 16}, TestID: "server-address"},
							ui.Button{TextFunc: label(locales.Connect), OnPress: submit, Disabled: busy, Style: buttonSize},
							core.Show{When: func() bool { return phase.Get() == switchWorkspace }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: label(locales.BackToWorkspace), Variant: ui.ButtonSecondary, Style: buttonSize, OnPress: func() {
									draft.Set(attempt.Get())
									message.Set(nil)
									mount++
									phase.Set(online)
								}}
							}},
							core.Show{When: func() bool { return phase.Get() == offline }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: label(locales.Retry), OnPress: retry, Variant: ui.ButtonSecondary, Style: buttonSize}
							}},
							core.Show{When: func() bool { return storeOwner.Get() != "" && !busy() }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: label(locales.Forget), OnPress: forget, Variant: ui.ButtonOutline, Style: buttonSize}
							}},
							core.Text{ContentFunc: func() string {
								if m := message.Get(); m != nil {
									return m(cx)
								}
								switch phase.Get() {
								case checkingServer:
									return locales.Connecting(cx)
								case clearingSignIn:
									return locales.Clearing(cx)
								case offline:
									if strings.HasPrefix(attempt.Get(), "http://") {
										return locales.Offline(cx) + " " + locales.LocalOffline(cx)
									}
									return locales.Offline(cx)
								}
								return ""
							}, TestID: "connection-status"},
						},
					}},
				}
			},
		}},
	}
}
