package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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
	ctx, cancel := context.WithCancel(cx.Context())
	mac := cx.Env().OS() == core.OSMacOS
	store := cx.Storage()
	stored, _ := store.GetItem(savedOriginKey)
	if origin, ok := canonicalOrigin(stored, mac); stored != localWorkspaceTarget && (!ok || origin != stored) {
		stored = ""
		store.RemoveItem(savedOriginKey)
	}
	address := stored
	if address == localWorkspaceTarget {
		address = ""
	}
	if address == "" && mac {
		address = macDefaultOrigin
	}
	// Keep the Mac default for users of an existing loopback service.
	initial := stored
	if initial == "" && mac {
		initial = macDefaultOrigin
	}
	storeOwner := core.NewSignal(cx, initial)
	attempt := core.NewSignal(cx, initial)
	phase := core.NewSignal(cx, editAddress)
	draft := core.NewSignal(cx, address)
	message := core.NewSignalFunc(cx, (func(*core.Scope) string)(nil), func(a, b func(*core.Scope) string) bool {
		return a == nil && b == nil
	})
	var confirmAttempt, requestedSettings bool
	var mount uint64
	var local localWorkspace
	var localHandler http.Handler
	appDir, version := cx.DataDir(), core.App().Version
	closeLocal := func() {
		if err := local.close(); err != nil {
			slog.Error("close local workspace", "err", err)
		}
	}
	cx.Cleanup(func() {
		// NSGo cancels the scope context after it runs cleanup functions.
		cancel()
		closeLocal()
	})
	leaveLocal := func(done func()) {
		localHandler = nil
		go func() {
			closeLocal()
			cx.Post(done)
		}()
	}

	showForm := func() {
		if localHandler != nil && storeOwner.Get() != localWorkspaceTarget {
			leaveLocal(func() { phase.Set(editAddress) })
			return
		}
		phase.Set(editAddress)
	}

	clearSignIn := func(done func()) {
		phase.Set(clearingSignIn)
		message.Set(nil)
		clearDexWebView(cx, storeOwner.Get(), func(err error) {
			if err != nil {
				message.Set(locales.ClearFailed)
				showForm()
				return
			}
			done()
		})
	}

	connect := func(target string, confirm bool) {
		attempt.Set(target)
		confirmAttempt = confirm
		requestedSettings = false
		phase.Set(checkingServer)
		message.Set(nil)
		go func() {
			var handler http.Handler
			var err error
			if target == localWorkspaceTarget {
				instance, startErr := local.start(ctx, appDir, version, mac)
				err = startErr
				if err == nil {
					handler = instance.Handler()
				}
			} else {
				err = probeDex(ctx, target, mac, nil)
			}
			cx.Post(func() {
				if handler != nil {
					localHandler = handler
				}
				if requestedSettings {
					requestedSettings = false
					showForm()
					return
				}
				if err != nil {
					if target == localWorkspaceTarget {
						message.Set(func(cx *core.Scope) string { return locales.LocalStartFailed(cx, err.Error()) })
					} else if errors.Is(err, errDexAuthRequired) {
						message.Set(locales.AuthRequired)
					}
					phase.Set(offline)
					return
				}
				open := func() {
					if confirm {
						if err := store.SetItem(savedOriginKey, target); err != nil {
							message.Set(locales.SaveFailed)
							showForm()
							return
						}
					}
					finish := func() {
						storeOwner.Set(target)
						if target != localWorkspaceTarget {
							draft.Set(target)
						}
						if requestedSettings {
							requestedSettings = false
							phase.Set(editAddress)
							return
						}
						mount++
						phase.Set(online)
					}
					if target != localWorkspaceTarget && localHandler != nil {
						phase.Set(checkingServer)
						leaveLocal(finish)
					} else {
						finish()
					}
				}
				if owner := storeOwner.Get(); owner != "" && owner != localWorkspaceTarget && owner != target {
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
			if attempt.Get() != localWorkspaceTarget {
				draft.Set(attempt.Get())
			}
			connect(attempt.Get(), confirmAttempt)
		}
	}
	forget := func() {
		if busy() || storeOwner.Get() == "" {
			return
		}
		reset := func() {
			store.RemoveItem(savedOriginKey)
			storeOwner.Set("")
			attempt.Set("")
			draft.Set("")
			phase.Set(editAddress)
		}
		if storeOwner.Get() == localWorkspaceTarget {
			phase.Set(clearingSignIn)
			leaveLocal(reset)
		} else {
			clearSignIn(reset)
		}
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
				return dexWebView(view, target, localHandler, func(next connectionPhase) {
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
							ui.Button{TextFunc: label(locales.OpenLocal), OnPress: func() {
								if !busy() {
									connect(localWorkspaceTarget, true)
								}
							}, Disabled: busy, Style: buttonSize},
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
							ui.Button{TextFunc: label(locales.Connect), OnPress: submit, Disabled: busy, Variant: ui.ButtonSecondary, Style: buttonSize},
							core.Show{When: func() bool { return phase.Get() == switchWorkspace }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: label(locales.BackToWorkspace), Variant: ui.ButtonSecondary, Style: buttonSize, OnPress: func() {
									if attempt.Get() != localWorkspaceTarget {
										draft.Set(attempt.Get())
									}
									message.Set(nil)
									mount++
									phase.Set(online)
								}}
							}},
							core.Show{When: func() bool { return phase.Get() == offline }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: label(locales.Retry), OnPress: retry, Variant: ui.ButtonSecondary, Style: buttonSize}
							}},
							core.Show{When: func() bool { return storeOwner.Get() != "" && !busy() }, Then: func(*core.Scope) core.Node {
								return ui.Button{TextFunc: func() string {
									if storeOwner.Get() == localWorkspaceTarget {
										return locales.ForgetLocal(cx)
									}
									return locales.Forget(cx)
								}, OnPress: forget, Variant: ui.ButtonOutline, Style: buttonSize}
							}},
							core.Text{ContentFunc: func() string {
								if m := message.Get(); m != nil {
									return m(cx)
								}
								switch phase.Get() {
								case checkingServer:
									if attempt.Get() == localWorkspaceTarget {
										return locales.OpeningLocal(cx)
									}
									return locales.Connecting(cx)
								case clearingSignIn:
									if storeOwner.Get() == localWorkspaceTarget {
										return locales.ClosingLocal(cx)
									}
									return locales.Clearing(cx)
								case offline:
									if attempt.Get() == localWorkspaceTarget {
										return locales.LocalViewFailed(cx)
									}
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
