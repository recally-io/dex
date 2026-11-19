package service

import (
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"strings"
	"text/template"
)

func plist(c Config, home, log string) []byte {
	return launchPlist(label, c.args(), c.Path, home, log, c.record(), false)
}

//go:embed launchd.plist.tmpl
var launchPlistSource string

var launchPlistTemplate = template.Must(template.New("launchd").Funcs(template.FuncMap{
	"xml": func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	},
}).Parse(launchPlistSource))

func launchPlist(name string, args []string, path, home, log, record string, scheduled bool) []byte {
	data := struct {
		Name, Path, Home, Log, Record string
		Args                          []string
		Scheduled                     bool
	}{name, path, home, log, record, args, scheduled}
	var b bytes.Buffer
	// The embedded template has fixed fields and writes only to a bytes.Buffer.
	if err := launchPlistTemplate.Execute(&b, data); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// systemd expands percent specifiers in directives and dollars in command arguments.
func unitQuote(value string, command bool) string {
	value = strings.ReplaceAll(value, "%", "%%")
	if command {
		value = strings.ReplaceAll(value, "$", "$$")
	}
	value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(value)
	return "\"" + value + "\""
}
func execLine(args []string) string {
	// systemd rejects quotes and backslashes in argv[0], even when escaped.
	// env accepts the exact path as an argument and replaces itself with dex.
	var b strings.Builder
	b.WriteString("/usr/bin/env --")
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(unitQuote(arg, true))
	}
	return b.String()
}
func unit(c Config) []byte {
	return fmt.Appendf(nil, "# dex-config: %s\n[Unit]\nDescription=dex bot host\n\n[Service]\nType=simple\nExecStart=%s\nWorkingDirectory=%%h\nEnvironment=%s\nRestart=on-failure\nTimeoutStopSec=60\n\n[Install]\nWantedBy=default.target\n", c.record(), execLine(c.args()), unitQuote("PATH="+c.Path, false))
}
func updateUnit(c Config) []byte {
	return fmt.Appendf(nil, "[Unit]\nDescription=dex daily update\n\n[Service]\nType=oneshot\nExecStart=%s\nWorkingDirectory=%%h\nEnvironment=%s\nTimeoutStartSec=10min\n", execLine([]string{c.Executable, "update", "--scheduled"}), unitQuote("PATH="+c.Path, false))
}

const updateTimer = "[Unit]\nDescription=dex daily update timer\n\n[Timer]\nOnCalendar=daily\nPersistent=true\nRandomizedDelaySec=1h\n\n[Install]\nWantedBy=timers.target\n"
