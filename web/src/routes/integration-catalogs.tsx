import { useEffect, useRef, useState, type ReactNode } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { searchMcpCatalog, searchSkillCatalog } from "../api/generated/sdk.gen";
import type { McpCatalogEntry, McpCatalogRemote, SkillCatalogEntry } from "../api/generated/types.gen";
import { Failure } from "../components/feedback";
import { Input } from "../components/form";
import { Button } from "../components/ui/button";
import { type McpDraft, requiresVault } from "../lib/mcp-declaration";
import { mcpPresets } from "../lib/mcp-presets";
import { skillSource } from "../lib/skill-install";

function useCatalogSearch() {
  const [text, setText] = useState("");
  const [q, setQuery] = useState("");
  const trimmed = text.trim();

  useEffect(() => {
    if (trimmed === q) return;

    const timer = setTimeout(() => setQuery(trimmed), 300);

    return () => clearTimeout(timer);
  }, [trimmed, q]);

  return { text, setText, q, settled: trimmed === q };
}

export function SkillCatalog({
  focus,
  onInstall,
}: {
  focus?: boolean;
  onInstall: (skill: SkillCatalogEntry) => void;
}) {
  const search = useCatalogSearch();
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (focus && input.current) {
      input.current.focus({ preventScroll: true });
      input.current.scrollIntoView({ block: "center" });
    }
  }, [focus]);

  const query = useInfiniteQuery({
    queryKey: ["skillCatalog", search.q],
    enabled: !!search.q && search.settled,
    initialPageParam: 0,
    queryFn: async ({ pageParam, signal }) => {
      const { data } = await searchSkillCatalog({
        query: { q: search.q, offset: pageParam },
        signal,
        throwOnError: true,
      });

      return data;
    },
    getNextPageParam: (page) => page.nextOffset,
  });

  const skills = query.data?.pages.flatMap((page) => page.skills) ?? [];

  return (
    <div className="mt-4 space-y-3">
      <div className="space-y-1.5">
        <label htmlFor="skill-catalog-search" className="text-sm font-medium">
          Search skill catalog
        </label>
        <Input
          ref={input}
          id="skill-catalog-search"
          type="search"
          value={search.text}
          maxLength={200}
          placeholder="Find a skill, such as PDF"
          onChange={(event) => search.setText(event.target.value)}
        />
      </div>
      {search.settled && search.q ? (
        <CatalogResults query={query} empty={!skills.length} more="Load more skills">
          {skills.map((skill) => {
            const source = skillSource(skill.sourceUrl);

            return (
              <li key={skill.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <div className="min-w-48 flex-1 space-y-1 [overflow-wrap:anywhere]">
                  <h3 className="text-sm font-medium">{skill.name}</h3>
                  <p className="text-xs text-muted-foreground">{skill.description}</p>
                  {source ? (
                    <a href={source} target="_blank" rel="noreferrer" className="text-xs underline">
                      {skill.author ? `Source by ${skill.author}` : "View source"}
                    </a>
                  ) : (
                    <p className="text-xs text-muted-foreground">No supported source link.</p>
                  )}
                </div>
                {source ? (
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`Install ${skill.name}`}
                    onClick={() => onInstall(skill)}
                  >
                    Install
                  </Button>
                ) : null}
              </li>
            );
          })}
        </CatalogResults>
      ) : null}
    </div>
  );
}

const presetAuth = { none: "No sign-in", oauth: "Browser OAuth", header: "Token" };

export function McpCatalog({ onAdd }: { onAdd: (draft: McpDraft) => void }) {
  const search = useCatalogSearch();

  const query = useInfiniteQuery({
    queryKey: ["mcpCatalog", search.q],
    enabled: !!search.q && search.settled,
    initialPageParam: "",
    queryFn: async ({ pageParam, signal }) => {
      const { data } = await searchMcpCatalog({
        query: { q: search.q, cursor: pageParam || undefined },
        signal,
        throwOnError: true,
      });

      return data;
    },
    getNextPageParam: (page) => page.nextCursor,
  });

  const entries = query.data?.pages.flatMap((page) => page.servers) ?? [];

  return (
    <div className="mt-5 space-y-5">
      <div className="space-y-3">
        <h3 className="text-sm font-semibold">Official presets</h3>
        {mcpPresets.map(({ group, servers }) => (
          <div key={group} className="space-y-2">
            <h4 className="text-xs font-medium text-muted-foreground">{group}</h4>
            <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              {servers.map((preset) => (
                <li
                  key={preset.name}
                  className="flex items-center justify-between gap-2 rounded-lg border border-border bg-card px-3 py-2"
                >
                  <div>
                    <p className="text-sm font-medium">{preset.title}</p>
                    <p className="text-xs text-muted-foreground">{presetAuth[preset.auth]}</p>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`Add ${preset.title}`}
                    onClick={() => onAdd(preset)}
                  >
                    Add
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      <div className="space-y-3">
        <div className="space-y-1.5">
          <label htmlFor="mcp-catalog-search" className="text-sm font-medium">
            Search MCP Registry
          </label>
          <Input
            id="mcp-catalog-search"
            type="search"
            value={search.text}
            maxLength={200}
            placeholder="Find a Streamable HTTP server"
            onChange={(event) => search.setText(event.target.value)}
          />
        </div>
        {search.settled && search.q ? (
          <CatalogResults query={query} empty={!entries.length} more="Load more MCP servers">
            {entries.map((entry) => (
              <RegistryRow key={`${entry.name}:${entry.version}`} entry={entry} onAdd={onAdd} />
            ))}
          </CatalogResults>
        ) : null}
      </div>
    </div>
  );
}

function registryDraft(entry: McpCatalogEntry, remote: McpCatalogRemote): McpDraft {
  const name = entry.name
    .split("/")
    .at(-1)
    ?.toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "");

  const headers = remote.headers.flatMap((header) => {
    const secret = header.isSecret || requiresVault(header.name);

    return header.isRequired
      ? [
          {
            name: header.name,
            value: secret ? "" : (header.value ?? ""),
            secret,
            description: header.description,
            required: true,
          },
        ]
      : [];
  });

  return {
    name: name?.slice(0, 32) || "mcp-server",
    url: remote.url,
    auth: headers.length ? "header" : "none",
    headers,
  };
}

function RegistryRow({ entry, onAdd }: { entry: McpCatalogEntry; onAdd: (draft: McpDraft) => void }) {
  const title = entry.title || entry.name;
  const remotes = entry.remotes.filter((remote) => remote.addable);

  return (
    <li className="space-y-2 px-4 py-3 [overflow-wrap:anywhere]">
      <h4 className="text-sm font-medium">{title}</h4>
      <p className="text-xs text-muted-foreground">{entry.description}</p>
      {entry.addable ? (
        <ul className="space-y-2">
          {remotes.map((remote) => (
            <li key={remote.url} className="flex flex-wrap items-center gap-3">
              <span className="min-w-0 flex-1 text-xs text-muted-foreground">{remote.url}</span>
              <Button
                size="sm"
                variant="outline"
                aria-label={remotes.length > 1 ? `Add ${title} at ${remote.url}` : `Add ${title}`}
                onClick={() => onAdd(registryDraft(entry, remote))}
              >
                Add
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">{entry.reason}</p>
      )}
    </li>
  );
}

type CatalogStatus = {
  error: Error | null;
  isPending: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  hasNextPage: boolean;
  refetch: () => void;
  fetchNextPage: () => void;
};

function CatalogResults({
  query,
  empty,
  more,
  children,
}: {
  query: CatalogStatus;
  empty: boolean;
  more: string;
  children: ReactNode;
}) {
  return (
    <>
      {query.isPending ? (
        <p role="status" className="text-xs text-muted-foreground">
          Searching…
        </p>
      ) : null}
      {query.error ? (
        <Failure
          title="Could not search the catalog"
          error={query.error}
          retry={() => {
            if (query.isFetchNextPageError) query.fetchNextPage();
            else query.refetch();
          }}
        />
      ) : null}
      {empty && !query.isPending && !query.error ? (
        <p className="text-xs text-muted-foreground">No results.</p>
      ) : null}
      {!empty ? (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">{children}</ul>
      ) : null}
      {query.hasNextPage && !query.isFetchNextPageError ? (
        <Button
          size="sm"
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => query.fetchNextPage()}
        >
          {query.isFetchingNextPage ? "Loading…" : more}
        </Button>
      ) : null}
    </>
  );
}
