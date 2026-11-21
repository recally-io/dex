import type { ReactNode } from "react";
import type { WorkTaskLink, WorkTaskWait } from "../api/generated/types.gen";
import { Section } from "./page";

function waitState(wait: WorkTaskWait, open: boolean) {
  if (wait.resolved) return "Received";

  return open ? "Waiting" : "Paused";
}

// A link shows its source identifiers as they are, so links stay readable without their source.
// children are the link forms of the enabled plugins.
export function TaskLinks({
  links,
  open,
  children,
}: {
  links: WorkTaskLink[];
  open: boolean;
  children?: ReactNode;
}) {
  return (
    <Section title="Links">
      {links.length ? (
        <ul className="divide-y divide-border">
          {links.map((link) => (
            <li key={`${link.namespace}:${link.type}:${link.key}`} className="space-y-2 py-3">
              <p className="break-words">
                {link.type} {link.key} · <span className="text-muted-foreground">{link.namespace}</span>
              </p>
              {link.waits.length ? (
                <ul className="space-y-1 text-sm text-muted-foreground">
                  {link.waits.map((wait) => (
                    <li key={wait.id} className="break-words">
                      <span>
                        {waitState(wait, open)} {wait.event}
                      </span>
                      {" · "}
                      {wait.revision ? <code>{wait.revision}</code> : "Any revision"}
                      {" · After "}
                      <span>{new Date(wait.afterAt).toLocaleString()}</span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No links.</p>
      )}
      {children}
    </Section>
  );
}
