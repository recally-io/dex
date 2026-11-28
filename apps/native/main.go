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
	"github.com/recally-io/dex/apps/native/internal/webview"
	"github.com/recally-io/dex/apps/native/locales"
)

const savedOriginKey = "dex.origin"
const localWorkspaceTarget = webview.LocalTarget

type connectionPhase uint8

const (
	editAddress connectionPhase = iota
	switchWorkspace
	checkingServer
	clearingSignIn
	offline
	online
)

func main() { nsgo.Run(root, webview.Options()...) }

func root(cx *core.Scope) core.Node {
	cx.SetTheme(dexTheme())
	ctx, cancel := context.WithCancel(cx.Context())
	mac := cx.Env().OS() == core.OSMacOS
	store := cx.Storage()
	stored, _ := store.GetItem(savedOriginKey)
	if origin, ok := webview.CanonicalOrigin(stored, mac); stored != localWorkspaceTarget && (!ok || origin != stored) {
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
		webview.ClearSiteData(cx, storeOwner.Get(), func(err error) {
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
		target, ok := webview.CanonicalOrigin(draft.Get(), mac)
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
		if !slices.Contains(links, webview.SettingsURL) {
			return
		}
		switch phase.Get() {
		case checkingServer:
			requestedSettings = true
		case online:
			phase.Set(switchWorkspace)
		case editAddress, switchWorkspace, clearingSignIn, offline:
		}
	})

	label := func(text func(*core.Scope) string) func() string { return func() string { return text(cx) } }
	colors := ui.ColorsOf(cx)
	muted := ui.Colored(colors.Muted)
	padding, controlHeight, ghostHeight, inputFontSize := core.Points(24), core.Points(42), core.Points(42), core.Points(16)
	if mac {
		padding, controlHeight, ghostHeight, inputFontSize = 32, 36, 28, 14
	}
	buttonStyle := core.Style{AlignSelf: core.AlignStart, Height: core.Pt(controlHeight), CornerRadius: 6}
	buttonText := core.Style{FontWeight: ui.Medium}
	ghostStyle := buttonStyle.Merge(core.Style{Height: core.Pt(ghostHeight)})
	section := func(title, caption func(*core.Scope) string, current func() bool, content ...core.Node) core.Node {
		return core.View{Style: core.Style{Gap: 10}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
				core.View{
					Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8},
					Children: []core.Node{
						core.Text{ContentFunc: label(title), Role: core.RoleHeader, Style: core.Style{FontSize: 14, FontWeight: ui.Semibold, FlexShrink: 1}},
						core.Show{When: current, Then: func(*core.Scope) core.Node {
							return core.View{
								Style: core.Style{Padding: core.Symmetric(2, 6), CornerRadius: 4, BorderWidth: 1},
								StyleFunc: func() core.Style {
									p := cx.Palette()
									return core.Style{Background: p.SurfaceHover, BorderColor: p.Border}
								},
								Children: []core.Node{ui.Textf(label(locales.CurrentWorkspace), 11, ui.Medium, colors.Muted)},
							}
						}},
					},
				},
				core.Text{ContentFunc: label(caption), Style: ui.TypeBody, StyleFunc: muted},
			}},
			core.View{
				Style: core.Style{Padding: core.All(16), Gap: 12, BorderWidth: 1, CornerRadius: 10},
				StyleFunc: func() core.Style {
					p := cx.Palette()
					return core.Style{Background: p.Surface, BorderColor: p.Border}
				},
				Children: content,
			},
		}}
	}
	remoteHelp := locales.RemoteHelp
	if mac {
		remoteHelp = locales.MacHelp
	}
	status := core.NewMemo(cx, func() string {
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
		case editAddress, switchWorkspace, online:
		}
		return ""
	})
	showStatus := func() bool { return status.Get() != "" || phase.Get() == offline }
	showForget := func() bool { return storeOwner.Get() != "" && !busy() }
	return core.View{
		Style:     core.Style{FlexGrow: 1, FlexShrink: 1, PaddingEnv: core.EnvAll(core.EnvSafeArea)},
		StyleFunc: func() core.Style { return core.Style{Background: cx.Palette().Background} },
		Children: []core.Node{core.Show{
			When: func() bool { return phase.Get() == online },
			Then: func(view *core.Scope) core.Node {
				target, id := attempt.Get(), mount
				leave := func(next connectionPhase) {
					if phase.Get() == online && id == mount {
						phase.Set(next)
					}
				}
				return webview.View(view, target, localHandler, func() { leave(offline) }, func() { leave(switchWorkspace) })
			},
			Else: func(*core.Scope) core.Node {
				return core.Scroll{
					Style: core.Style{FlexGrow: 1, FlexShrink: 1, PaddingEnv: core.EnvEdges{Bottom: core.EnvKeyboard}},
					Children: []core.Node{core.View{
						Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(520), AlignSelf: core.AlignCenter, Padding: core.All(padding), Gap: 24},
						Children: []core.Node{
							core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
								core.Show{When: func() bool { return phase.Get() == switchWorkspace }, Then: func(*core.Scope) core.Node {
									return ui.Button{
										TextFunc: label(locales.BackToWorkspace), Variant: ui.ButtonGhost,
										Style: ghostStyle, TextStyle: buttonText,
										OnPress: func() {
											if attempt.Get() != localWorkspaceTarget {
												draft.Set(attempt.Get())
											}
											message.Set(nil)
											mount++
											phase.Set(online)
										},
									}
								}},
								core.Text{Role: core.RoleHeader, Style: ui.TypeHeading, ContentFunc: func() string {
									if phase.Get() == switchWorkspace {
										return locales.SwitchWorkspace(cx)
									}
									return locales.Connection(cx)
								}},
								core.Text{ContentFunc: label(locales.ConnectionSubtitle), Style: ui.TypeBody, StyleFunc: muted},
							}},
							section(locales.LocalWorkspace, locales.LocalHelp, func() bool { return storeOwner.Get() == localWorkspaceTarget },
								ui.Button{TextFunc: label(locales.OpenLocal), OnPress: func() {
									if !busy() {
										connect(localWorkspaceTarget, true)
									}
								}, Disabled: busy, Style: buttonStyle, TextStyle: buttonText}),
							section(locales.RemoteServer, remoteHelp, func() bool {
								owner := storeOwner.Get()
								return owner != "" && owner != localWorkspaceTarget
							},
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
									Style: core.Style{Height: core.Pt(controlHeight), FontSize: inputFontSize, CornerRadius: 6}, TestID: "server-address"},
								ui.Button{TextFunc: label(locales.Connect), OnPress: submit, Disabled: busy, Variant: ui.ButtonSecondary, Style: buttonStyle, TextStyle: buttonText}),
							core.Show{When: func() bool { return showStatus() || showForget() }, Then: func(*core.Scope) core.Node {
								return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
									core.Show{When: showStatus, Then: func(*core.Scope) core.Node {
										return core.View{
											Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12},
											Children: []core.Node{
												core.Text{ContentFunc: status.Get, TestID: "connection-status",
													Style: ui.TypeBody.Merge(core.Style{FlexGrow: 1, FlexShrink: 1}),
													StyleFunc: ui.Colored(func() core.Color {
														if phase.Get() == offline || message.Get() != nil {
															return colors.Danger()
														}
														return colors.Muted()
													})},
												core.Show{When: func() bool { return phase.Get() == offline }, Then: func(*core.Scope) core.Node {
													return ui.Button{TextFunc: label(locales.Retry), OnPress: retry, Variant: ui.ButtonSecondary, Style: buttonStyle, TextStyle: buttonText}
												}},
											},
										}
									}},
									core.Show{When: showForget, Then: func(*core.Scope) core.Node {
										return ui.Button{TextFunc: func() string {
											if storeOwner.Get() == localWorkspaceTarget {
												return locales.ForgetLocal(cx)
											}
											return locales.Forget(cx)
										}, OnPress: forget, Variant: ui.ButtonGhost, TextStyle: buttonText,
											Style: ghostStyle.Merge(core.Style{Opacity: core.Alpha(0.65)})}
									}},
								}}
							}},
						},
					}},
				}
			},
		}},
	}
}
