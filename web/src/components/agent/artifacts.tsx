import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Files } from "lucide-react";
import {
  createChat,
  deleteArtifact,
  getChat,
  renameArtifact,
  setChatProject,
} from "../../api/generated/sdk.gen";
import { listArtifactsOptions } from "../../api/generated/@tanstack/react-query.gen";
import type { Artifact } from "../../api/generated/types.gen";
import { useAgentId } from "../../lib/agent-scope";
import { putChatDraft } from "../../lib/chat-draft";
import { Markdown } from "./transcript";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { Modal } from "../dialog";
import { Confirm } from "../confirm";
import { Failure } from "../feedback";

function reference(item: Artifact) {
  return `Continue modifying delivered file ${item.name} (artifact ID ${item.id}, source conversation ${item.conversationId}). The delivered file is a snapshot and does not change with the source file.`;
}

export function Artifacts({ chatId }: { chatId: number }) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [selectedId, setSelectedId] = useState<string>();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");
  const [confirm, setConfirm] = useState(false);
  // SSE invalidates deliveries. Poll while open for snapshots removed outside dex.

  const list = useQuery({
    ...listArtifactsOptions({ path: { agentId, chatId } }),
    enabled: open,
    refetchInterval: open ? 3000 : false,
    refetchIntervalInBackground: true,
  });

  const selected = list.data?.artifacts.find((item) => item.id === selectedId);
  const path = (id: string) => ({ agentId, chatId, artifactId: id });
  const refresh = () => queryClient.invalidateQueries({ queryKey: [{ _id: "listArtifacts" }] });

  const rename = useMutation({
    mutationFn: ({ id, name: nextName }: { id: string; name: string }) =>
      renameArtifact({ path: path(id), body: { name: nextName }, throwOnError: true }),
    onSuccess: async () => {
      setEditing(false);
      await refresh();
    },
    onError: () => void refresh(),
  });

  const remove = useMutation({
    mutationFn: (id: string) => deleteArtifact({ path: path(id), throwOnError: true }),
    onSuccess: async () => {
      setConfirm(false);
      setSelectedId(undefined);
      await refresh();
    },
    onError: () => void refresh(),
  });

  const url = selected
    ? `/api/agents/${encodeURIComponent(agentId)}/chats/${chatId}/artifacts/${encodeURIComponent(selected.id)}`
    : "";

  const markdown = !!selected && /\.(md|mdown|markdown)$/i.test(selected.name);
  const image = !!selected && /\.(png|jpe?g|gif|webp)$/i.test(selected.name);

  const content = useQuery({
    queryKey: ["artifact-content", url],
    enabled: open && markdown,
    queryFn: async () => {
      const response = await fetch(url);

      if (!response.ok) throw new Error("Could not load file");

      return response.text();
    },
  });

  const continuation = useMutation({
    mutationFn: async (item: Artifact) => {
      const source = await getChat({
        path: { agentId, chatId },
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

      putChatDraft(agentId, target.id, reference(item));
      setOpen(false);

      if (target.main) await navigate({ to: "/a/$agentId", params: { agentId } });
      else await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(target.id) } });
    },
  });

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          onClick={() => setOpen(true)}
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground"
              aria-label="Artifacts"
              aria-haspopup="dialog"
              aria-expanded={open}
            />
          }
        >
          <Files aria-hidden="true" />
        </TooltipTrigger>
        <TooltipContent side="bottom">Artifacts</TooltipContent>
      </Tooltip>
      {open ? (
        <Modal
          title="Artifacts"
          description="Delivered snapshots are shared with their source conversation. Renaming or deleting a snapshot does not change the source file."
          onClose={() => setOpen(false)}
          footer={
            <Button variant="secondary" onClick={() => setOpen(false)}>
              Close
            </Button>
          }
        >
          {list.isPending ? (
            <p>Loading artifacts…</p>
          ) : list.isError ? (
            <Failure title="Could not load artifacts" error={list.error} retry={() => void list.refetch()} />
          ) : !list.data.artifacts.length ? (
            <p className="text-sm text-muted-foreground">No artifacts yet.</p>
          ) : (
            <div className="flex flex-col gap-2" aria-label="Artifact files">
              {list.data.artifacts.map((item) => (
                <Button
                  key={item.id}
                  variant={selectedId === item.id ? "secondary" : "outline"}
                  className="h-auto justify-start whitespace-normal text-left [overflow-wrap:anywhere]"
                  onClick={() => {
                    setSelectedId(item.id);
                    setEditing(false);
                    continuation.reset();
                  }}
                >
                  {item.name}
                </Button>
              ))}
            </div>
          )}
          {selected && !list.isError ? (
            <div className="flex min-w-0 flex-col gap-3">
              <strong className="text-sm [overflow-wrap:anywhere]">{selected.name}</strong>
              <div className="flex flex-wrap items-center gap-2">
                <a className="text-sm text-primary underline" href={url} download={selected.name}>
                  Download
                </a>
                <Button
                  size="xs"
                  variant="secondary"
                  disabled={continuation.isPending}
                  onClick={() => continuation.mutate(selected)}
                >
                  Continue editing
                </Button>
                <Button
                  size="xs"
                  variant="secondary"
                  onClick={() => {
                    rename.reset();
                    setName(selected.name);
                    setEditing(true);
                  }}
                >
                  Rename
                </Button>
                <Button
                  size="xs"
                  variant="destructive"
                  onClick={() => {
                    remove.reset();
                    setConfirm(true);
                  }}
                >
                  Delete
                </Button>
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
          {selected && editing ? (
            <Modal
              title="Rename artifact"
              description="Change the snapshot name shown in all conversations and downloads."
              pending={rename.isPending}
              onClose={() => setEditing(false)}
              footer={
                <>
                  <Button variant="secondary" disabled={rename.isPending} onClick={() => setEditing(false)}>
                    Cancel
                  </Button>
                  <Button type="submit" form="rename-artifact" disabled={rename.isPending || !name.trim()}>
                    Save
                  </Button>
                </>
              }
            >
              <form
                id="rename-artifact"
                className="flex flex-col gap-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  rename.mutate({ id: selected.id, name });
                }}
              >
                <Label htmlFor="artifact-name">File name</Label>
                <Input
                  id="artifact-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  disabled={rename.isPending}
                  autoFocus
                  required
                  maxLength={255}
                />
                {rename.isError ? <Failure title="Could not rename artifact" error={rename.error} /> : null}
              </form>
            </Modal>
          ) : null}
          {selected && confirm ? (
            <Confirm
              title="Delete artifact?"
              description={`Delete “${selected.name}” from all conversations that share this snapshot? The source file will not change. This action cannot be undone.`}
              confirmLabel="Delete artifact"
              pending={remove.isPending}
              error={remove.error}
              onCancel={() => setConfirm(false)}
              onConfirm={() => remove.mutate(selected.id)}
            />
          ) : null}
        </Modal>
      ) : null}
    </>
  );
}
