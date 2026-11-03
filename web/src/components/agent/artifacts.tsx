import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createChat, getChat, setChatProject } from "../../api/generated/sdk.gen";
import { listArtifactsOptions } from "../../api/generated/@tanstack/react-query.gen";
import type { Artifact } from "../../api/generated/types.gen";
import { useAgentId } from "../../lib/agent-scope";
import { Markdown } from "./transcript";
import { Button } from "../ui/button";

const keyFor = (agentId: string, chatId: number) => `dex-artifact-draft:${agentId}:${chatId}`;

export function takeArtifactDraft(agentId: string, chatId: number) {
  const key = keyFor(agentId, chatId);
  const value = sessionStorage.getItem(key);

  sessionStorage.removeItem(key);

  return value;
}

function reference(item: Artifact) {
  return `Continue modifying delivered file ${item.name} (artifact ID ${item.id}, source conversation ${item.conversationId}). The delivered file is a snapshot and does not change with the source file.`;
}

export function Artifacts({ chatId, onContinue }: { chatId: number; onContinue?: (text: string) => void }) {
  const agentId = useAgentId();
  const navigate = useNavigate();

  const list = useQuery({ ...listArtifactsOptions({ path: { agentId, chatId } }), refetchInterval: 3000 });

  const [selected, setSelected] = useState<Artifact>();

  const url = selected
    ? `/api/agents/${encodeURIComponent(agentId)}/chats/${chatId}/artifacts/${encodeURIComponent(selected.id)}`
    : "";

  const markdown = !!selected && /\.(md|mdown|markdown)$/i.test(selected.name);

  const image = !!selected && /\.(png|jpe?g|gif|webp)$/i.test(selected.name);

  const content = useQuery({
    queryKey: ["artifact-content", url],
    enabled: markdown,
    queryFn: async () => {
      const response = await fetch(url);

      if (!response.ok) throw new Error("Could not load file");

      return response.text();
    },
  });

  const continuation = useMutation({
    mutationFn: async (item: Artifact) => {
      const source = await getChat({
        path: { agentId, chatId: item.displayConversationId },
        throwOnError: true,
      });

      let target = source.data;

      if (target.readOnly) {
        const chat = await createChat({ path: { agentId }, throwOnError: true });
        target = chat.data;
        await setChatProject({
          path: { agentId, chatId: target.id },
          body: { cwd: source.data.cwd ?? "" },
          throwOnError: true,
        });
      }

      sessionStorage.setItem(keyFor(agentId, target.id), reference(item));
      window.dispatchEvent(new Event("dex-artifact-draft"));

      if (target.main) await navigate({ to: "/a/$agentId", params: { agentId } });
      else await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(target.id) } });
    },
  });

  if (list.isPending) return null;

  if (list.isError) return <p role="alert">Could not load delivered files.</p>;

  if (!list.data.artifacts.length) return null;

  return (
    <section className="flex flex-col gap-3 rounded-md border border-border p-3" aria-label="Delivered files">
      <h2 className="text-sm font-medium">Delivered files</h2>
      <p className="text-xs text-muted-foreground">Snapshots do not change when the source files change.</p>
      <div className="flex flex-wrap gap-2">
        {list.data.artifacts.map((item) => (
          <Button key={item.id} variant="outline" size="sm" onClick={() => setSelected(item)}>
            {item.name}
          </Button>
        ))}
      </div>
      {selected ? (
        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex flex-wrap items-center gap-3">
            <strong className="text-sm">{selected.name}</strong>
            <a className="text-sm text-primary underline" href={url} download={selected.name}>
              Download
            </a>
            {onContinue ? (
              <Button size="xs" variant="secondary" onClick={() => onContinue(reference(selected))}>
                Continue editing
              </Button>
            ) : (
              <Button
                size="xs"
                variant="secondary"
                disabled={continuation.isPending}
                onClick={() => continuation.mutate(selected)}
              >
                Continue editing
              </Button>
            )}
          </div>
          {continuation.isError ? <p role="alert">Could not open a writable chat. Try again.</p> : null}
          {markdown ? (
            content.isPending ? (
              <p>Loading file…</p>
            ) : content.isError ? (
              <p role="alert">Could not load file.</p>
            ) : (
              <div className="max-h-96 overflow-auto text-sm">
                <Markdown text={content.data} />
              </div>
            )
          ) : image ? (
            <img className="max-h-96 max-w-full object-contain" src={url} alt={selected.name} />
          ) : (
            <p className="text-sm text-muted-foreground">Download this file to open it.</p>
          )}
        </div>
      ) : null}
    </section>
  );
}
