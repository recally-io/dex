import { useAgentId } from "../lib/agent-scope";
import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";

/** The link back to the list that a detail page opens from. */
export function Crumb({
  to,
  children,
}: {
  to: "/a/$agentId/channels" | "/a/$agentId/automations";
  children: ReactNode;
}) {
  const agentId = useAgentId();

  return (
    <Link to={to} params={{ agentId }} className="hover:text-ink hover:underline">
      {children}
    </Link>
  );
}
