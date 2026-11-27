import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listVaultOptions } from "../api/generated/@tanstack/react-query.gen";
import { Failure } from "../components/feedback";
import { Field, Input, RadioSegments, Select, Textarea } from "../components/form";
import { Button } from "../components/ui/button";
import type { GoogleMcpAuth } from "../lib/mcp-declaration";

type ClientSecret = { kind: "paste"; value: string } | { kind: "reuse"; name: string; botId?: string };

export type GoogleClientForm = { id: string; scope: string; secret: ClientSecret };

export function reusedClientSecret(secret: ClientSecret, botId?: string) {
  return secret.kind === "reuse" && secret.botId === botId ? secret.name : "";
}

const secretOptions = [
  { value: "paste", label: "Paste client secret" },
  { value: "reuse", label: "Use existing vault secret" },
] as const;

export function GoogleMcpClientFields({
  preset,
  botId,
  targetValid,
  pending,
  value,
  onChange,
}: {
  preset: GoogleMcpAuth & { url: string };
  botId?: string;
  targetValid: boolean;
  pending: boolean;
  value: GoogleClientForm;
  onChange: (next: GoogleClientForm) => void;
}) {
  const { secret } = value;

  const vault = useQuery({
    ...listVaultOptions({ path: { botId: botId ?? "default" } }),
    enabled: targetValid && secret.kind === "reuse",
  });

  const vaultScope = botId ? "agent" : "dex";
  const names = vault.data?.flatMap((entry) => (entry.scope === vaultScope ? [entry.name] : [])) ?? [];

  return (
    <>
      <GoogleMcpSetup preset={preset} />
      <Field label="Client ID">
        {({ id }) => (
          <Input
            id={id}
            value={value.id}
            onChange={(event) => onChange({ ...value, id: event.target.value })}
            autoComplete="off"
            disabled={pending}
            required
          />
        )}
      </Field>
      <RadioSegments
        label="Client secret source"
        options={secretOptions}
        value={secret.kind}
        onChange={(kind) => {
          if (!pending)
            onChange({
              ...value,
              secret: kind === "paste" ? { kind, value: "" } : { kind, name: "", botId },
            });
        }}
        className="[&_button]:px-2 [&_button]:text-xs"
      />
      {secret.kind === "paste" ? (
        <Field label="Client secret" hint="Dex stores the secret in the vault for the selected target.">
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              type="password"
              autoComplete="new-password"
              value={secret.value}
              onChange={(event) =>
                onChange({ ...value, secret: { kind: "paste", value: event.target.value } })
              }
              disabled={pending}
              required
            />
          )}
        </Field>
      ) : (
        <>
          <Field
            label="Client secret vault name"
            hint={botId ? "Use a secret in this Bot's vault." : "Use a secret in the Shared vault."}
          >
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                value={reusedClientSecret(secret, botId)}
                onChange={(event) =>
                  onChange({ ...value, secret: { kind: "reuse", name: event.target.value, botId } })
                }
                disabled={pending || !targetValid || !names.length}
                required
              >
                <option value="" disabled>
                  {!targetValid
                    ? "Choose a target first"
                    : vault.isPending
                      ? "Loading vault…"
                      : names.length
                        ? "Choose a secret"
                        : "No secrets at this level"}
                </option>
                {names.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {vault.isError ? (
            <Failure
              title="Could not load vault names"
              error={vault.error}
              retry={() => void vault.refetch()}
            />
          ) : null}
        </>
      )}
      <Field label="Scope" hint="Keep only the scopes that you need. Add them to the consent screen.">
        {({ id, describedBy }) => (
          <Textarea
            id={id}
            aria-describedby={describedBy}
            value={value.scope}
            onChange={(event) => onChange({ ...value, scope: event.target.value })}
            rows={3}
            autoComplete="off"
            spellCheck={false}
            disabled={pending}
            required
          />
        )}
      </Field>
    </>
  );
}

function GoogleMcpSetup({ preset }: { preset: GoogleMcpAuth & { url: string } }) {
  const redirect = `${window.location.origin}/mcp/oauth/callback`;
  const [copyState, setCopyState] = useState<"copied" | "failed">();

  async function copyRedirect() {
    try {
      await navigator.clipboard.writeText(redirect);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
  }

  return (
    <div className="space-y-2 rounded-md border border-border p-3.5 text-xs text-muted-foreground">
      <p className="font-medium text-foreground">Set up a Google OAuth client</p>
      <p>
        Use one registered client for all Google Workspace presets. Follow the{" "}
        <a
          href="https://developers.google.com/workspace/guides/configure-mcp-servers"
          target="_blank"
          rel="noreferrer"
          className="underline"
        >
          Google Workspace setup guide
        </a>
        .
      </p>
      <ol className="list-decimal space-y-1 pl-5">
        <li>Create or select a Google Cloud project.</li>
        <li>
          Enable{" "}
          {preset.productApi ? (
            <>
              <code>{preset.productApi}</code> and{" "}
            </>
          ) : null}
          <code>{new URL(preset.url).hostname}</code>.
        </li>
        <li>
          Set up the OAuth consent screen and add the scopes below. If the audience is External, add test
          users.
        </li>
        <li>
          Create a <strong>Web application</strong> OAuth client. Select{" "}
          <strong>Use this client for an AI-powered agent</strong> and add this authorized redirect URI:
        </li>
      </ol>
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 select-all rounded bg-muted px-2 py-1 text-foreground [overflow-wrap:anywhere]">
          {redirect}
        </code>
        <Button type="button" size="xs" variant="outline" onClick={() => void copyRedirect()}>
          Copy redirect URI
        </Button>
      </div>
      {copyState ? (
        <p role="status">{copyState === "copied" ? "Redirect URI copied." : "Select the URI to copy it."}</p>
      ) : null}
      {preset.productApi === "chat.googleapis.com" ? (
        <p>To send messages or make other changes, also configure a Chat app.</p>
      ) : null}
    </div>
  );
}
