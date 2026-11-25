import { useState } from "react";
import type { Environment, SshHostEntry } from "../api/generated/types.gen";
import { createEnvironment } from "../api/generated/sdk.gen";
import { useApp } from "../lib/app-state";
import { formatResolvedSshHost } from "../lib/environments";
import { refreshEnvironments } from "../lib/queries";
import { Modal } from "./dialog";
import { Failure, Spinner } from "./feedback";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function ImportSshConfigDialog({
  hosts,
  existing,
  onClose,
}: {
  hosts: SshHostEntry[];
  existing: Environment[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const existingSshHosts = new Set<string>();

  for (const env of existing) {
    if (env.kind === "pi-env" && env.config.host) existingSshHosts.add(env.config.host);
  }

  const [selected, setSelected] = useState<string[]>([]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const available = hosts.filter((item) => !existingSshHosts.has(item.alias));
  const selectedSet = new Set(selected);

  function toggle(alias: string) {
    setSelected((prev) => (prev.includes(alias) ? prev.filter((item) => item !== alias) : [...prev, alias]));
  }

  async function submit() {
    if (selected.length === 0 || pending) return;

    setPending(true);
    setError(null);

    try {
      for (const alias of selected) {
        // eslint-disable-next-line no-await-in-loop
        await createEnvironment({
          body: { name: alias, kind: "pi-env", config: { host: alias } },
          throwOnError: true,
        });
      }

      await refreshEnvironments();
      onClose();
      notify(
        selected.length === 1 ? `Imported ${selected[0]}.` : `Imported ${selected.length} SSH Machines.`,
      );
    } catch (cause) {
      await refreshEnvironments();
      setError(cause);
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      title="Import from ~/.ssh/config"
      description="Selected aliases stay linked to ~/.ssh/config so ProxyJump, IdentityAgent, and defaults keep working."
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={() => void submit()} disabled={pending || selected.length === 0}>
            {pending ? (
              <Spinner className="border-primary-foreground/40 border-t-primary-foreground" />
            ) : null}
            {pending
              ? "Importing…"
              : selected.length
                ? `Import ${selected.length} host${selected.length === 1 ? "" : "s"}`
                : "Import selected"}
          </Button>
        </>
      }
    >
      <div className="flex items-center justify-between gap-2 text-[12px] text-muted-foreground">
        <span>
          {hosts.length} host{hosts.length === 1 ? "" : "s"} found
        </span>
        {available.length > 0 ? (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            disabled={pending}
            onClick={() =>
              setSelected(selected.length === available.length ? [] : available.map((item) => item.alias))
            }
          >
            {selected.length === available.length ? "Clear selection" : "Select all new"}
          </Button>
        ) : null}
      </div>

      <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
        {hosts.map((entry) => {
          const alreadyAdded = existingSshHosts.has(entry.alias);
          const checked = selectedSet.has(entry.alias);

          return (
            <li key={entry.alias}>
              <label className="flex cursor-pointer items-start gap-3 px-3.5 py-2.5 transition-colors hover:bg-accent/50">
                <input
                  type="checkbox"
                  className="mt-1 size-4 accent-primary"
                  disabled={alreadyAdded || pending}
                  checked={alreadyAdded || checked}
                  onChange={() => toggle(entry.alias)}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px] font-medium text-foreground">{entry.alias}</span>
                    {alreadyAdded ? (
                      <Badge variant="secondary" className="h-5 px-2 text-[11px]">
                        Already added
                      </Badge>
                    ) : null}
                    {entry.source ? (
                      <span className="text-[11px] text-muted-foreground">{entry.source}</span>
                    ) : null}
                  </div>
                  <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                    {formatResolvedSshHost(entry)}
                  </p>
                </div>
              </label>
            </li>
          );
        })}
      </ul>

      {error ? <Failure title="Could not import SSH hosts" error={error} /> : null}
    </Modal>
  );
}
