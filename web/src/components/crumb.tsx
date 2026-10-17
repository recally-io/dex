import type { ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";

/** The link back to the list that a detail page opens from. */
export function Crumb({ to, children }: { to: LinkProps["to"]; children: ReactNode }) {
  return (
    <Link to={to} className="hover:text-ink hover:underline">
      {children}
    </Link>
  );
}
