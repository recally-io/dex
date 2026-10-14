import { useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  Plus,
  KeyRound,
  Check,
  Server,
  Trash2,
  ShieldAlert,
  X,
  SlidersHorizontal,
  Send,
  Info,
} from "lucide-react";
import type { Settings, Provider } from "../api/generated/types.gen";
import {
  getSettingsOptions,
  getHealthOptions,
  setAgentMutation,
  deleteProviderMutation,
  setTelegramMutation,
  deleteTelegramMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Textarea } from "../components/ui/textarea";
import { Alert, AlertTitle, AlertDescription } from "../components/ui/alert";
import { Failure, Loading } from "../components/feedback";
import { ModelPicker } from "../components/model-picker";
import { ProviderDialog } from "../components/provider-dialog";
import { Confirm } from "../components/confirm";
import { cn } from "../lib/utils";

export const sections = [
  { id: "providers", label: "Providers", icon: KeyRound },
  { id: "model", label: "Model", icon: SlidersHorizontal },
  { id: "telegram", label: "Telegram", icon: Send },
  { id: "about", label: "About", icon: Info },
];

export function SettingsPage({ section }: { section: string }) {
  const settings = useQuery(getSettingsOptions());
  const valid = sections.some((s) => s.id === section);

  return (
    <div className="mx-auto w-full max-w-[880px] px-5 py-10 md:px-8 md:py-14">
      <p className="mb-2 text-xs font-medium tracking-widest text-ink-2 uppercase">Make dex yours</p>
      <h1 className="mb-8 text-3xl font-semibold tracking-tight">Settings</h1>
      <div className="flex flex-col gap-8 md:flex-row">
        <nav
          aria-label="Settings sections"
          className="flex gap-1 overflow-x-auto md:w-40 md:shrink-0 md:flex-col"
        >
          {sections.map(({ id, label, icon: Icon }) => (
            <Link
              key={id}
              to="/settings/$section"
              params={{ section: id }}
              className={cn(
                "flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-sm text-ink-2 hover:bg-hover",
                section === id && "bg-hover font-medium text-ink",
              )}
            >
              <Icon className="size-4" />
              {label}
            </Link>
          ))}
        </nav>
        <section className="min-w-0 flex-1">
          {!valid ? (
            <Failure error="This settings section does not exist." />
          ) : section === "about" ? (
            <About />
          ) : settings.isPending ? (
            <Loading />
          ) : settings.isError ? (
            <Failure error={settings.error} retry={() => void settings.refetch()} />
          ) : settings.data ? (
            section === "providers" ? (
              <Providers settings={settings.data} />
            ) : section === "model" ? (
              <Model settings={settings.data} />
            ) : (
              <Telegram settings={settings.data} />
            )
          ) : null}
        </section>
      </div>
    </div>
  );
}

function Heading({ title, description }: { title: string; description: string }) {
  return (
    <div className="mb-6">
      <h2 className="mb-1.5 text-xl font-semibold tracking-tight">{title}</h2>
      <p className="text-sm leading-relaxed text-ink-2">{description}</p>
    </div>
  );
}

function Providers({ settings }: { settings: Settings }) {
  const [dialog, setDialog] = useState<Provider | "new" | null>(null);
  const [remove, setRemove] = useState<Provider | null>(null);

  const deletion = useMutation({
    ...deleteProviderMutation(),
    onSuccess: async () => {
      await refreshSettings();
      setRemove(null);
    },
  });

  return (
    <>
      <div className="flex items-start justify-between gap-3">
        <Heading title="Providers" description="Connect the services that power your agent." />
        <Button size="sm" onClick={() => setDialog("new")}>
          <Plus />
          Add provider
        </Button>
      </div>
      <div className="flex flex-col gap-4">
        {settings.providers.length ? (
          settings.providers.map((p) => (
            <article key={p.id} className="surface p-5">
              <div className="flex items-start gap-3">
                <div className="rounded-lg bg-inset p-2.5">
                  <Server className="size-5 text-ink-2" />
                </div>
                <div className="min-w-0 flex-1">
                  <h3 className="font-semibold">{p.name}</h3>
                  <p className="mt-1 text-xs text-ink-2">
                    {p.api ? (settings.apis[p.api] ?? p.api) : "Built-in"} · {p.id}
                  </p>
                </div>
                <span className={cn("flex items-center gap-1 text-xs", p.error ? "text-red" : "text-green")}>
                  {p.error ? (
                    "Needs attention"
                  ) : (
                    <>
                      <Check className="size-3.5" />
                      Ready
                    </>
                  )}
                </span>
              </div>
              {p.baseUrl ? <p className="mt-4 break-all font-mono text-xs text-ink-2">{p.baseUrl}</p> : null}
              <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-ink-2">
                <span className="flex items-center gap-1.5">
                  <KeyRound className="size-3.5" />
                  {p.keySet ? "Key saved" : "No key"}
                </span>
                <span>
                  {p.modelCount === undefined ? "Model count unavailable" : `${p.modelCount} models`}
                </span>
              </div>
              {p.error ? (
                <div className="mt-4">
                  <Failure title="Could not list models" error={p.error} retry={() => setDialog(p)} />
                </div>
              ) : null}
              <div className="mt-4 flex justify-end gap-2">
                <Button variant="outline" size="sm" onClick={() => setDialog(p)}>
                  Edit
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    deletion.reset();
                    setRemove(p);
                  }}
                >
                  <Trash2 className="size-3.5" />
                  Remove
                </Button>
              </div>
            </article>
          ))
        ) : (
          <div className="surface flex flex-col items-center gap-3 px-6 py-12 text-center">
            <Server className="size-7 text-ink-3" />
            <h3 className="font-medium">No providers yet</h3>
            <p className="text-sm text-ink-2">Add a provider to discover its models.</p>
            <Button onClick={() => setDialog("new")}>
              <Plus />
              Add your first provider
            </Button>
          </div>
        )}
      </div>
      {dialog ? (
        <ProviderDialog
          settings={settings}
          existing={dialog === "new" ? undefined : dialog}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {remove ? (
        <Confirm
          title={`Remove ${remove.name}?`}
          description={
            settings.agent.provider === remove.id
              ? "The current model uses this provider. Choose another model before you send the next message."
              : "This removes the provider and its saved key."
          }
          onCancel={() => setRemove(null)}
          onConfirm={() => deletion.mutate({ path: { providerId: remove.id } })}
          pending={deletion.isPending}
          error={deletion.error}
        />
      ) : null}
    </>
  );
}

function Model({ settings }: { settings: Settings }) {
  const [draft, setDraft] = useState<typeof settings.agent | null>(null);
  const agent = draft ?? settings.agent;

  const save = useMutation({
    ...setAgentMutation(),
    onSuccess: async () => {
      await refreshSettings();
      setDraft(null);
    },
  });

  const dirty = JSON.stringify(agent) !== JSON.stringify(settings.agent);

  return (
    <>
      <Heading title="Model" description="One model for all chats. Changes apply from the next message." />
      <form
        className="surface flex flex-col gap-6 p-6"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ body: agent });
        }}
      >
        <div>
          <label className="mb-2 block text-sm font-medium">Agent model</label>
          <div className="flex items-center justify-between rounded-lg bg-inset p-2">
            <span className="pl-2 text-xs text-ink-2">{agent.provider || "No provider"}</span>
            <ModelPicker
              provider={agent.provider}
              modelId={agent.modelId}
              onSelect={(provider, modelId) => setDraft({ ...agent, provider, modelId })}
            />
          </div>
          {!agent.modelId ? <p className="mt-2 text-xs text-ink-2">Choose a model to start a chat.</p> : null}
        </div>
        <label className="flex flex-col gap-2 font-medium">
          Instructions
          <Textarea
            className="min-h-48 font-normal leading-relaxed"
            placeholder="Tell dex how you want it to work…"
            value={agent.instructions}
            onChange={(e) => setDraft({ ...agent, instructions: e.target.value })}
          />
          <span className="text-right text-xs font-normal text-ink-2">
            {agent.instructions.length} characters
          </span>
        </label>
        {save.isError ? (
          <Failure
            title="Could not save model"
            error={save.error}
            retry={() => save.mutate({ body: agent })}
          />
        ) : null}
        <div className="flex items-center justify-between">
          <p aria-live="polite" className="text-xs text-ink-2">
            {dirty ? "Unsaved changes" : save.isSuccess ? "Saved" : "No changes"}
          </p>
          <Button type="submit" disabled={!dirty || save.isPending}>
            {save.isPending ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </form>
    </>
  );
}

function Telegram({ settings }: { settings: Settings }) {
  const [token, setToken] = useState("");
  const [replace, setReplace] = useState(!settings.telegram.enabled);
  const [users, setUsers] = useState<number[] | null>(null);
  const allowed = users ?? settings.telegram.allowedUsers;
  const [input, setInput] = useState("");
  const [validation, setValidation] = useState("");
  const [off, setOff] = useState(false);

  const save = useMutation({
    ...setTelegramMutation(),
    onSuccess: async () => {
      setToken("");
      setReplace(false);
      await refreshSettings();
      setUsers(null);
    },
  });

  const deletion = useMutation({
    ...deleteTelegramMutation(),
    onSuccess: async () => {
      await refreshSettings();
      setOff(false);
      setReplace(true);
      setUsers(null);
    },
  });

  function addUser() {
    const value = Number(input);

    if (!/^\d+$/.test(input) || !Number.isSafeInteger(value) || value <= 0) {
      setValidation("Enter a positive user ID with no spaces.");

      return;
    }

    setUsers([...new Set([...allowed, value])]);
    setInput("");
    setValidation("");
  }

  return (
    <>
      <Heading title="Telegram" description="Talk to the same agent from Telegram." />
      <form
        className="surface flex flex-col gap-6 p-6"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ body: { token, allowedUsers: allowed } });
        }}
      >
        <div className="flex items-center justify-between">
          <span className="font-medium">Bot connection</span>
          <span
            className={cn(
              "rounded-md bg-inset px-2 py-1 text-xs",
              settings.telegram.enabled ? "text-green" : "text-ink-2",
            )}
          >
            {settings.telegram.enabled ? `On · bot ${settings.telegram.botId ?? "connected"}` : "Off"}
          </span>
        </div>
        <label className="flex flex-col gap-2">
          Bot token
          {!replace ? (
            <div className="flex items-center justify-between rounded-lg bg-inset p-3">
              <span className="text-sm text-ink-2">Saved token ••••</span>
              <Button variant="outline" size="sm" onClick={() => setReplace(true)}>
                Replace token
              </Button>
            </div>
          ) : (
            <Input
              type="password"
              autoComplete="new-password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="Token from @BotFather"
            />
          )}
          <span className="text-xs text-ink-2">
            Create a bot with @BotFather. Saved tokens are never shown.
          </span>
        </label>
        <div className="flex flex-col gap-3">
          <label htmlFor="telegram-user">Allowed user IDs</label>
          <div className="flex flex-wrap gap-2">
            {allowed.length ? (
              allowed.map((id) => (
                <span
                  key={id}
                  className="flex items-center gap-2 rounded-md bg-inset px-2.5 py-1.5 font-mono text-xs"
                >
                  {id}
                  <button
                    type="button"
                    aria-label={`Remove user ${id}`}
                    onClick={() => setUsers(allowed.filter((user) => user !== id))}
                  >
                    <X className="size-3" />
                  </button>
                </span>
              ))
            ) : (
              <p className="text-xs text-ink-2">No users allowed. Add your ID to turn Telegram on.</p>
            )}
          </div>
          <div className="flex gap-2">
            <Input
              id="telegram-user"
              inputMode="numeric"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="Your numeric user ID"
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addUser();
                }
              }}
            />
            <Button variant="outline" onClick={addUser}>
              Add
            </Button>
          </div>
          <p className="text-xs text-ink-2">Message @userinfobot in Telegram to find your user ID.</p>
          {validation ? (
            <p role="alert" className="text-xs text-red">
              {validation}
            </p>
          ) : null}
        </div>
        <Alert>
          <ShieldAlert />
          <AlertTitle>Tools run on this computer</AlertTitle>
          <AlertDescription>
            Allowed users can ask dex to read files and run commands. Add only people you trust.
          </AlertDescription>
        </Alert>
        {save.isError ? (
          <Failure
            title="Could not save Telegram"
            error={save.error}
            retry={() => save.mutate({ body: { token, allowedUsers: allowed } })}
          />
        ) : null}
        <div className="flex justify-between gap-2">
          {settings.telegram.enabled ? (
            <Button
              variant="ghost"
              onClick={() => {
                deletion.reset();
                setOff(true);
              }}
            >
              Turn off
            </Button>
          ) : (
            <span />
          )}
          <Button
            type="submit"
            disabled={save.isPending || !allowed.length || (!settings.telegram.enabled && !token.trim())}
          >
            {save.isPending ? "Saving…" : "Save Telegram"}
          </Button>
        </div>
        {!allowed.length ? (
          <p className="text-xs text-ink-2">At least one allowed user is required.</p>
        ) : null}
        {save.isSuccess ? (
          <p role="status" className="text-xs text-green">
            Telegram settings saved.
          </p>
        ) : null}
      </form>
      {off ? (
        <Confirm
          title="Turn Telegram off?"
          description="dex will stop receiving Telegram messages and forget the saved token."
          onCancel={() => setOff(false)}
          onConfirm={() => deletion.mutate({})}
          pending={deletion.isPending}
          error={deletion.error}
        />
      ) : null}
    </>
  );
}

function About() {
  const health = useQuery(getHealthOptions());

  return (
    <>
      <Heading title="About dex" description="Your personal agent. Your computer. Your data." />
      {health.isPending ? (
        <Loading rows={1} />
      ) : health.isError ? (
        <Failure error={health.error} retry={() => void health.refetch()} />
      ) : health.data ? (
        <div className="surface flex flex-col gap-5 p-6">
          <div className="flex items-center gap-3">
            <div className="flex size-11 items-center justify-center rounded-xl bg-ink text-lg font-semibold text-page">
              d
            </div>
            <div>
              <h3 className="font-semibold">dex</h3>
              <p className="text-xs text-ink-2">Local agent</p>
            </div>
          </div>
          <dl className="flex flex-col gap-5">
            <div>
              <dt className="mb-1 text-xs text-ink-2">Version</dt>
              <dd className="font-mono text-sm">{health.data.version ?? "Unavailable on this server"}</dd>
            </div>
            <div>
              <dt className="mb-1 text-xs text-ink-2">Data directory</dt>
              <dd className="break-all font-mono text-xs">
                {health.data.dataDir ?? "Unavailable on this server"}
              </dd>
            </div>
            <div>
              <dt className="mb-1 text-xs text-ink-2">Server</dt>
              <dd className="text-sm text-green">
                {health.data.status === "ok" ? "Running" : "Unavailable"}
              </dd>
            </div>
          </dl>
        </div>
      ) : null}
    </>
  );
}
