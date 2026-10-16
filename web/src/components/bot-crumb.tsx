import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";

export function BotCrumb() {
  const settings = useQuery(getSettingsOptions());

  return (
    <Link to="/agent" className="hover:text-ink hover:underline">
      {settings.data?.agent.name ?? "dex"}
    </Link>
  );
}
