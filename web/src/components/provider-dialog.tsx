import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import type { Provider, Settings } from "../api/generated/types.gen";
import { getSettingsQueryKey, setProviderMutation } from "../api/generated/@tanstack/react-query.gen";
import { errorText, queryClient, refreshSettings } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { Button } from "./ui/button";
import { Modal } from "./dialog";
import { Failure, Spinner } from "./feedback";
import { Field, Input, RadioSegments, SavedSecret, Select, Textarea } from "./form";

// The same rule as customID in internal/settings.
const providerId = /^[a-z0-9][a-z0-9-]{0,39}$/;

const examples = new Map([
  ["openai-completions", "http://127.0.0.1:1234/v1"],
  ["openai-responses", "https://api.openai.com/v1"],
  ["anthropic-messages", "https://api.anthropic.com"],
]);

const notes = new Map([
  ["openai-completions", "Chat Completions. Works with local servers such as LM Studio or Ollama."],
  ["openai-responses", "OpenAI Responses API."],
  ["anthropic-messages", "Anthropic Messages API."],
]);

const kinds = [
  { value: "builtin", label: "Built-in service" },
  { value: "custom", label: "Custom endpoint" },
] as const;

function slug(name: string) {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 40);
}

function urlError(raw: string) {
  try {
    const url = new URL(raw);

    if (url.protocol !== "http:" && url.protocol !== "https:") return "Use an http or https URL.";

    if (url.username || url.password)
      return "Remove the user name from the URL. Put the credential in the key.";

    return "";
  } catch {
    return "Enter a full URL, for example http://127.0.0.1:1234/v1.";
  }
}

export function ProviderDialog({
  settings,
  existing: initial,
  onClose,
}: {
  settings: Settings;
  existing?: Provider;
  onClose: () => void;
}) {
  const { notify } = useApp();
  const apis = Object.keys(settings.apis);
  const free = settings.builtins.filter((b) => !settings.providers.some((p) => p.id === b.id));
  // After a save with a listing error, the dialog stays open and edits the saved provider.
  const [savedId, setSavedId] = useState(initial?.id ?? "");
  const existing = settings.providers.find((p) => p.id === savedId) ?? initial;
  const [kind, setKind] = useState<"builtin" | "custom">(initial?.api ? "custom" : "builtin");
  const [builtin, setBuiltin] = useState(initial && !initial.api ? initial.id : (free[0]?.id ?? ""));
  const [name, setName] = useState(initial?.api ? initial.name : "");
  const [id, setId] = useState(initial?.api ? initial.id : "");
  const [idEdited, setIdEdited] = useState(!!initial);
  const [api, setApi] = useState(initial?.api || apis[0] || "");
  const [url, setUrl] = useState(initial?.baseUrl ?? "");
  const [key, setKey] = useState("");
  const [replace, setReplace] = useState(!initial?.keySet);
  const [models, setModels] = useState(initial?.models.join("\n") ?? "");
  const [tried, setTried] = useState(false);
  const [listingError, setListingError] = useState("");
  const targetId = kind === "builtin" ? builtin : id;

  const save = useMutation({
    ...setProviderMutation(),
    onSuccess: async (result) => {
      setKey("");
      setReplace(false);
      setSavedId(result.id);
      await refreshSettings();

      if (result.error) setListingError(result.error);
      else {
        onClose();
        notify(
          result.modelCount === undefined
            ? `Saved ${result.name}.`
            : `Saved ${result.name}. ${result.modelCount} models are available.`,
        );
      }
    },
    onError: async (_, variables) => {
      // The save can fail after the provider was stored, so read the settings again. The typed key
      // is dropped; enter it again to retry.
      setKey("");
      await refreshSettings();
      const fresh = queryClient.getQueryData<Settings>(getSettingsQueryKey());

      if (fresh?.providers.some((p) => p.id === variables.path.providerId))
        setSavedId(variables.path.providerId);
    },
  });

  const errors: Partial<Record<"id" | "name" | "url" | "key", string>> = {};

  if (kind === "builtin") {
    if (!builtin) errors.id = "Every built-in provider is already added.";

    if (!existing?.keySet && !key.trim()) errors.key = "A built-in provider needs an API key.";
  } else {
    if (!existing) {
      if (!providerId.test(id))
        errors.id = "Use 1 to 40 lowercase letters, digits, or hyphens. Start with a letter or digit.";
      else if (settings.builtins.some((b) => b.id === id))
        errors.id = `${id} is a built-in provider; choose another ID.`;
      else if (settings.providers.some((p) => p.id === id))
        errors.id = `A provider with the ID ${id} already exists.`;
    }

    if (!name.trim()) errors.name = "Enter a name.";

    const problem = urlError(url.trim());

    if (problem) errors.url = problem;
  }

  const valid = Object.keys(errors).length === 0;
  const show = (field: keyof typeof errors) => (tried ? errors[field] : undefined);
  const builtinName = settings.builtins.find((b) => b.id === builtin)?.name ?? builtin;

  function submit() {
    setTried(true);
    setListingError("");

    if (!valid || save.isPending) return;
    save.mutate({
      path: { providerId: targetId },
      body:
        kind === "builtin"
          ? { api: "", apiKey: key.trim() }
          : {
              api,
              name: name.trim(),
              baseUrl: url.trim(),
              apiKey: key.trim(),
              models: models
                .split(/[\n,]/)
                .map((m) => m.trim())
                .filter(Boolean),
            },
    });
  }

  const failure = save.error ? errorText(save.error) : "";

  return (
    <Modal
      title={existing ? `Edit ${existing.name}` : "Add provider"}
      description={
        existing
          ? "The provider ID cannot change. Remove the provider and add it again to use another ID."
          : "dex saves the provider, then lists its models."
      }
      pending={save.isPending}
      onClose={onClose}
      footer={
        <>
          <Button size="sm" variant="secondary" onClick={onClose} disabled={save.isPending}>
            {listingError ? "Close" : "Cancel"}
          </Button>
          <Button size="sm" onClick={submit} disabled={save.isPending}>
            {save.isPending ? <Spinner className="border-canvas/40 border-t-canvas" /> : null}
            {save.isPending ? "Saving and listing models…" : existing ? "Save changes" : "Save provider"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {!existing ? (
          <div className="flex flex-col gap-1.5">
            <RadioSegments
              label="Type"
              options={kinds}
              value={kind}
              onChange={(value) => {
                setKind(value);
                setKey("");
                setTried(false);
              }}
            />
            <p className="text-[12px] text-ink-3">
              {kind === "builtin"
                ? "Anthropic, OpenAI, OpenRouter and others. Needs a key."
                : "Any OpenAI- or Anthropic-compatible URL, such as LM Studio on this computer."}
            </p>
          </div>
        ) : null}

        {kind === "builtin" ? (
          existing ? (
            <p className="text-[12.5px] text-ink-2">
              <span className="font-medium text-ink">{builtinName}</span> is built in. Only the key can
              change.
            </p>
          ) : (
            <Field label="Service" error={show("id")}>
              {({ id: field, describedBy, invalid }) => (
                <Select
                  id={field}
                  aria-describedby={describedBy}
                  aria-invalid={invalid}
                  value={builtin}
                  disabled={!free.length}
                  onChange={(e) => setBuiltin(e.target.value)}
                >
                  {free.map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )
        ) : (
          <>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Name" error={show("name")}>
                {({ id: field, describedBy, invalid }) => (
                  <Input
                    id={field}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    value={name}
                    placeholder="LM Studio"
                    onChange={(e) => {
                      setName(e.target.value);

                      if (!idEdited) setId(slug(e.target.value));
                    }}
                  />
                )}
              </Field>
              <Field
                label="Provider ID"
                hint={
                  existing
                    ? "Fixed after the first save. Past answers keep this ID in their model label."
                    : "The model prefix. Fixed after save. Lowercase letters, digits, and hyphens."
                }
                error={show("id")}
              >
                {({ id: field, describedBy, invalid }) => (
                  <Input
                    id={field}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    value={id}
                    maxLength={40}
                    disabled={!!existing}
                    onChange={(e) => {
                      setIdEdited(true);
                      setId(e.target.value);
                    }}
                  />
                )}
              </Field>
            </div>
            <Field label="API" hint={notes.get(api)}>
              {({ id: field, describedBy }) => (
                <Select
                  id={field}
                  aria-describedby={describedBy}
                  value={api}
                  onChange={(e) => setApi(e.target.value)}
                >
                  {apis.map((value) => (
                    <option key={value} value={value}>
                      {settings.apis[value]}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label="Base URL" error={show("url")}>
              {({ id: field, describedBy, invalid }) => (
                <Input
                  id={field}
                  type="url"
                  aria-describedby={describedBy}
                  aria-invalid={invalid}
                  className="font-mono"
                  placeholder={examples.get(api)}
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                />
              )}
            </Field>
          </>
        )}

        {existing?.keySet && !replace ? (
          <SavedSecret
            label="API key"
            text="Saved key ••••"
            hint="dex never shows a saved key."
            action="Replace key"
            onReplace={() => setReplace(true)}
          />
        ) : (
          <Field
            label={kind === "custom" ? "API key (optional)" : "API key"}
            hint={
              existing?.keySet
                ? "Write-only. An empty field keeps the saved key."
                : kind === "custom"
                  ? "Write-only. Without a key, dex sends “none”, for local servers."
                  : "Write-only. dex stores the key on this computer and never shows it again."
            }
            error={show("key")}
          >
            {({ id: field, describedBy, invalid }) => (
              <Input
                id={field}
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
            )}
          </Field>
        )}

        {kind === "custom" ? (
          <Field
            label="Model IDs (optional)"
            hint="One per line. When the list is empty, dex asks the endpoint for its models."
          >
            {({ id: field, describedBy }) => (
              <Textarea
                id={field}
                aria-describedby={describedBy}
                className="font-mono"
                rows={3}
                value={models}
                onChange={(e) => setModels(e.target.value)}
              />
            )}
          </Field>
        ) : null}
        <button type="submit" hidden />
      </form>
      {listingError ? (
        <Failure title="Saved. Model listing failed.">
          <p className="font-mono text-[11.5px]">{listingError}</p>
          <p className="mt-1">
            The provider stays saved. Check the base URL and the key, or enter model IDs.
          </p>
        </Failure>
      ) : null}
      {failure ? (
        <Failure
          title={failure.startsWith("saved,") ? "Saved, but not applied" : "Provider not saved"}
          error={save.variables?.body.apiKey ? `${failure} Enter the API key again to retry.` : failure}
        />
      ) : null}
    </Modal>
  );
}
