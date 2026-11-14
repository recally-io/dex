# 0020: Explicit speech provider credential references

Status: Proposed. Amends the 1Password boundary of [ADR 0013](0013-tool-environment-sources.md) for optional speech requests only.

## Context

Optional Telegram STT and TTS use a selected saved provider. A saved credential can be an `op://` reference. Sending that reference as a bearer token cannot authenticate the request. Copying the resolved key into another setting would create a second credential record.

ADR 0013 keeps ordinary tool environment references under the agent's control. Its rule that dex never runs `op` needs a narrow exception for a configured speech HTTP request.

## Decision

Speech stays off by default. The user selects the provider and audio model and permits data transmission. TTS also needs an explicit voice. The model cannot select an arbitrary credential reference or endpoint in the speech tool call.

If the saved provider key starts with `op://`, dex runs the configured `op read --no-newline` with that exact reference. It uses the existing base tool environment and saved 1Password mode. It does not load a bot's project, extensions, or MCP configuration for this operation. The request time budget covers credential resolution and HTTP.

The resolved key exists only in process memory and the selected endpoint's authorization header. dex does not replace the stored reference. It does not put the key in tool output, status, logs, or a second setting. Credential errors use a fixed public message. A missing CLI or failed read stops speech; dex does not select another credential or provider.

This exception does not resolve ordinary `.env` references, change chat-provider authentication, or add credential discovery, a secret API, or a new secret store. Existing agent commands and MCP authentication still follow ADR 0013.

## Consequences

- The speech HTTP boundary can hold a resolved provider key. Review this exception with the speech feature before merge.
- The saved 1Password account must have access only to the required agent vaults.
- A local synthetic CLI and HTTP endpoint test reference resolution without reading a real secret or calling a paid provider.
- Real 1Password and provider verification needs separate authorization. The deterministic tests do not prove that a deployed provider supports the selected audio model.
