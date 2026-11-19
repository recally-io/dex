# 0024: Separate the service PATH from the tool environment

Status: accepted. Amends the PATH decision of
[ADR 0006](0006-service-and-update.md).

## Context

Service install copies the install shell's complete PATH. Definitions store it
both as the runtime environment and in the configuration record. Tool managers
can expand this value into many version-specific directories.

[ADR 0013](0013-tool-environment-sources.md) gives agent tools the interactive
login shell environment. A second shell at service startup, or a complete PATH
snapshot in the service definition, is not needed for tool discovery.

## Decision

Keep direct execution of the absolute dex path. Do not add a service shell.
Use `/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin` as the service PATH. On macOS,
put `/opt/homebrew/bin` first.

When automatic updates are enabled, find `gh` on the install shell PATH. Put its
directory first in the service PATH, without a duplicate directory. Keep the
existing stored-credential and release-access checks. Disabling automatic
updates removes this extra directory on reinstall.

Keep the configuration record as the source of installed boot options. It
supports partial reinstall and reads of older templates. Reading an old
definition does not change its PATH. Reinstall replaces the old PATH with the
new service PATH and retains omitted boot and update options.

Generate macOS definitions from an embedded, indented XML template. Escape all
runtime string values as XML. Keep systemd quoting, service lifecycle, token
selection, and readiness checks unchanged.

## Consequences

Service definitions do not store unrelated tool directories. Agent tools still
use the existing login shell capture and reload mechanism. If shell capture
fails before any successful capture, the tool fallback PATH is the small
service PATH.

Commands that dex starts directly, such as Git for memory commits, must be in
the service PATH. If the selected `gh` installation moves or is removed, the
user must reinstall the service before automatic updates can use its new path.
A stable package-manager path avoids this requirement during normal upgrades.

The configuration record still duplicates a small PATH. This keeps the same
boot-option format and does not require a second platform-specific parser.
No live service is changed until the user installs the new template.
