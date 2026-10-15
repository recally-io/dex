import { Link } from "@tanstack/react-router";
import { KeyRound } from "lucide-react";
import { Card, Note, NotAvailable, Page, PageHeader, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { ValuePill } from "../components/atoms/ValuePill";
import { WithSettings } from "./agent";

export function SecretsPage() {
  return (
    <Page>
      <PageHeader
        title="Secrets"
        badge={<NotAvailable />}
        description="Named secrets that the agent could use in tools are not available in this version."
        actions={
          <Button variant="outline" size="sm" disabled aria-describedby="secrets-status">
            <KeyRound aria-hidden className="size-3.5" /> Add secret
          </Button>
        }
      />
      <p id="secrets-status" className="sr-only">
        dex cannot store named secrets in this version.
      </p>
      <WithSettings>
        {(settings) => (
          <Section
            title="Where credentials live now"
            description="dex keeps each credential with the thing that uses it. The API never returns a saved value."
          >
            <Card className="p-0 sm:p-0">
              <ul className="divide-y divide-line">
                {settings.providers.map((p) => (
                  <li
                    key={p.id}
                    className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-5"
                  >
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-ink">{p.name} API key</p>
                      <p className="font-mono text-xs text-ink-3">provider {p.id}</p>
                    </div>
                    <div className="flex items-center gap-3">
                      {p.keySet ? <ValuePill tone="green">Saved</ValuePill> : <ValuePill>None</ValuePill>}
                      <Link to="/providers" className="text-[13px] text-brand underline">
                        Providers
                      </Link>
                    </div>
                  </li>
                ))}
                <li className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-5">
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-ink">Telegram bot token</p>
                    <p className="text-xs text-ink-3">channel telegram</p>
                  </div>
                  <div className="flex items-center gap-3">
                    {settings.telegram.enabled ? (
                      <ValuePill tone="green">Saved</ValuePill>
                    ) : (
                      <ValuePill>None</ValuePill>
                    )}
                    <Link
                      to="/channels/$channelId"
                      params={{ channelId: "telegram" }}
                      className="text-[13px] text-brand underline"
                    >
                      Telegram
                    </Link>
                  </div>
                </li>
              </ul>
            </Card>
          </Section>
        )}
      </WithSettings>
      <Note tone="warn">
        dex stores these values as plain text in control.sqlite in the data folder. Anyone who can read that
        folder can read them.
      </Note>
    </Page>
  );
}
