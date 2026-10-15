package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

func plist(c Config, home, log string) []byte {
	return launchPlist(label, c.args(), c.Path, home, log, c.record(), false)
}
func launchPlist(name string, args []string, path, home, log, record string, scheduled bool) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	if record != "" {
		fmt.Fprintf(&b, "<!-- dex-config: %s -->\n", record)
	}
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>`)
	str := func(value string) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(value))
		b.WriteString("</string>")
	}
	b.WriteString("<key>Label</key>")
	str(name)
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range args {
		str(arg)
	}
	b.WriteString("</array>")
	b.WriteString("<key>WorkingDirectory</key>")
	str(home)
	if scheduled {
		b.WriteString("<key>RunAtLoad</key><false/><key>StartCalendarInterval</key><dict><key>Hour</key><integer>10</integer><key>Minute</key><integer>0</integer></dict>")
	} else {
		b.WriteString("<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ExitTimeOut</key><integer>60</integer>")
	}
	b.WriteString("<key>StandardOutPath</key>")
	str(log)
	b.WriteString("<key>StandardErrorPath</key>")
	str(log)
	b.WriteString("<key>EnvironmentVariables</key><dict><key>PATH</key>")
	str(path)
	b.WriteString("</dict></dict></plist>\n")
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
	return fmt.Appendf(nil, "# dex-config: %s\n[Unit]\nDescription=dex agent host\n\n[Service]\nType=simple\nExecStart=%s\nWorkingDirectory=%%h\nEnvironment=%s\nRestart=on-failure\nTimeoutStopSec=60\n\n[Install]\nWantedBy=default.target\n", c.record(), execLine(c.args()), unitQuote("PATH="+c.Path, false))
}
func updateUnit(c Config) []byte {
	return fmt.Appendf(nil, "[Unit]\nDescription=dex daily update\n\n[Service]\nType=oneshot\nExecStart=%s\nWorkingDirectory=%%h\nEnvironment=%s\nTimeoutStartSec=10min\n", execLine([]string{c.Executable, "update", "--scheduled"}), unitQuote("PATH="+c.Path, false))
}

const updateTimer = "[Unit]\nDescription=dex daily update timer\n\n[Timer]\nOnCalendar=daily\nPersistent=true\nRandomizedDelaySec=1h\n\n[Install]\nWantedBy=timers.target\n"
