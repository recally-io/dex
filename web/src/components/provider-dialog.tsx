import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Check, Search, ChevronLeft } from "lucide-react";
import type { Provider, Settings } from "../api/generated/types.gen";
import { setProviderMutation } from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Button } from "./ui/button";
import { Failure } from "./feedback";

const examples = new Map([
  ["openai-completions", "https://api.example.com/v1"],
  ["openai-responses", "https://api.openai.com/v1"],
  ["anthropic-messages", "https://api.anthropic.com"],
]);

const descriptions = new Map([
  ["openai-completions", "Chat Completions API. Works with local servers."],
  ["openai-responses", "OpenAI Responses API."],
  ["anthropic-messages", "Anthropic Messages API."],
]);

export function ProviderDialog({
  settings,
  existing,
  onClose,
}: {
  settings: Settings;
  existing?: Provider;
  onClose: () => void;
}) {
  const [kind, setKind] = useState(existing ? existing.api || existing.id : "");
  const [search, setSearch] = useState("");
  const [name, setName] = useState(existing?.name ?? "");
  const [id, setId] = useState(existing?.id ?? "");
  const [idEdited, setIdEdited] = useState(!!existing);
  const [url, setUrl] = useState(existing?.baseUrl ?? "");
  const [key, setKey] = useState("");
  const [replace, setReplace] = useState(!existing?.keySet);
  const [models, setModels] = useState(existing?.models.join("\n") ?? "");
  const [savedError, setSavedError] = useState("");

  const save = useMutation({
    ...setProviderMutation(),
    onSuccess: async (result) => {
      await refreshSettings();

      if (result.error) setSavedError(`Saved, but listing the models failed: ${result.error}`);
      else onClose();
    },
  });

  const custom = Object.hasOwn(settings.apis, kind);
  const builtin = settings.builtins.find((p) => p.id === kind);
  const valid = kind && (custom ? /^[a-zA-Z0-9_-]+$/.test(id) && name.trim() && url.trim() : builtin);

  const types = [
    ...settings.builtins.map((p) => ({ id: p.id, name: p.name, description: "Built-in provider" })),
    ...Object.entries(settings.apis).map(([apiId, apiName]) => ({
      id: apiId,
      name: apiName,
      description: descriptions.get(apiId),
    })),
  ].filter((p) => `${p.name} ${p.id}`.toLowerCase().includes(search.toLowerCase()));

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {existing ? "Edit provider" : kind ? "Provider details" : "Add a provider"}
          </DialogTitle>
          <DialogDescription>
            {kind ? "Your key stays on this computer." : "Choose a built-in provider or a custom API."}
          </DialogDescription>
        </DialogHeader>
        {!kind ? (
          <>
            <div className="relative">
              <Search className="absolute left-3 top-2.5 size-4 text-ink-3" />
              <Input
                className="pl-9"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search providers…"
                aria-label="Search providers"
              />
            </div>
            <div className="max-h-80 overflow-auto">
              {types.length ? (
                types.map((p) => (
                  <button
                    key={p.id}
                    className="flex w-full flex-col gap-1 rounded-lg px-3 py-3 text-left hover:bg-hover"
                    onClick={() => {
                      setKind(p.id);

                      if (!Object.hasOwn(settings.apis, p.id)) {
                        setName(p.name);
                        setId(p.id);
                      }
                    }}
                  >
                    <span className="font-medium">{p.name}</span>
                    <span className="text-xs text-ink-2">{p.description}</span>
                  </button>
                ))
              ) : (
                <p className="p-4 text-ink-2">No providers match your search.</p>
              )}
            </div>
          </>
        ) : (
          <form
            className="flex flex-col gap-5"
            onSubmit={(e) => {
              e.preventDefault();
              setSavedError("");
              save.mutate({
                path: { providerId: id },
                body: {
                  api: custom ? kind : "",
                  name,
                  baseUrl: url,
                  apiKey: key,
                  models: models
                    .split(/[\n,]/)
                    .map((s) => s.trim())
                    .filter(Boolean),
                },
              });
            }}
          >
            {!existing ? (
              <Button
                variant="ghost"
                size="sm"
                className="self-start"
                onClick={() => {
                  setKind("");
                  setId("");
                  setName("");
                  setIdEdited(false);
                }}
              >
                <ChevronLeft />
                Change type
              </Button>
            ) : null}
            <div className="rounded-lg bg-inset p-3 text-sm font-medium">
              {custom ? settings.apis[kind] : builtin?.name}
            </div>
            {custom ? (
              <>
                <label className="flex flex-col gap-2">
                  Name
                  <Input
                    required
                    value={name}
                    onChange={(e) => {
                      setName(e.target.value);

                      if (!idEdited)
                        setId(
                          e.target.value
                            .toLowerCase()
                            .replace(/[^a-z0-9]+/g, "-")
                            .replace(/^-|-$/g, ""),
                        );
                    }}
                    placeholder="My provider"
                  />
                </label>
                <label className="flex flex-col gap-2">
                  Provider ID
                  <Input
                    required
                    pattern="[a-zA-Z0-9_-]+"
                    disabled={!!existing}
                    value={id}
                    onChange={(e) => {
                      setIdEdited(true);
                      setId(e.target.value);
                    }}
                  />
                  <span className="text-xs text-ink-2">
                    Used as the model prefix. Letters, digits, hyphens, and underscores.
                  </span>
                </label>
                <label className="flex flex-col gap-2">
                  Base URL
                  <Input
                    required
                    type="url"
                    placeholder={examples.get(kind)}
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                  />
                </label>
              </>
            ) : null}
            <label className="flex flex-col gap-2">
              API key {custom ? "(optional)" : ""}
              {!replace ? (
                <div className="flex items-center justify-between rounded-lg bg-inset p-3">
                  <span className="text-ink-2">Saved key ••••</span>
                  <Button variant="outline" size="sm" onClick={() => setReplace(true)}>
                    Replace key
                  </Button>
                </div>
              ) : (
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                  placeholder="Enter API key"
                  required={!custom && !existing?.keySet}
                />
              )}
            </label>
            {custom ? (
              <label className="flex flex-col gap-2">
                Model IDs (optional)
                <Textarea
                  value={models}
                  onChange={(e) => setModels(e.target.value)}
                  placeholder="One model ID per line"
                />
                <span className="text-xs text-ink-2">Leave empty to list models from the endpoint.</span>
              </label>
            ) : null}
            {save.isError ? (
              <Failure
                title="Could not save provider"
                error={save.error}
                retry={() => document.getElementById("provider-save")?.click()}
              />
            ) : null}
            {savedError ? <Failure title="Provider needs attention" error={savedError} /> : null}
            <Button id="provider-save" type="submit" disabled={!valid || save.isPending}>
              {save.isPending ? (
                "Saving…"
              ) : (
                <>
                  <Check />
                  Save provider
                </>
              )}
            </Button>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
