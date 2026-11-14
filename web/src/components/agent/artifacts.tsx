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

function reference(agentId: string, item: Artifact) {
  return `Continue editing the bot artifact ${item.name} at /api/agents/${encodeURIComponent(agentId)}/artifacts/${encodeURIComponent(item.name)}. This file is in this bot's artifacts directory, not the workspace source.`;
}

export function Artifacts({ chatId }: { chatId: number }) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [selectedName, setSelectedName] = useState<string>();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");
  const [confirm, setConfirm] = useState(false);
  // The directory is shared by this bot. Poll open panels for external file changes.

  const list = useQuery({
    ...listArtifactsOptions({ path: { agentId } }),
    enabled: open,
    refetchInterval: open ? 3000 : false,
    refetchIntervalInBackground: true,
  });

  const selected = list.data?.artifacts.find((item) => item.name === selectedName);
  const path = (fileName: string) => ({ agentId, artifactName: fileName });
  const refresh = () => queryClient.invalidateQueries({ queryKey: [{ _id: "listArtifacts" }] });

  const rename = useMutation({
    mutationFn: ({ from, to }: { from: string; to: string }) =>
      renameArtifact({ path: path(from), body: { name: to }, throwOnError: true }),
    onSuccess: async ({ data }) => {
      setSelectedName(data.name);
      setEditing(false);
      await refresh();
    },
    onError: () => void refresh(),
  });

  const remove = useMutation({
    mutationFn: (fileName: string) => deleteArtifact({ path: path(fileName), throwOnError: true }),
    onSuccess: async () => {
      setConfirm(false);
      setSelectedName(undefined);
      await refresh();
    },
    onError: () => void refresh(),
  });

  const url = selected
    ? `/api/agents/${encodeURIComponent(agentId)}/artifacts/${encodeURIComponent(selected.name)}`
    : "";

  const markdown = !!selected && /\.(md|mdown|markdown)$/i.test(selected.name);
  const image = !!selected && /\.(png|jpe?g|gif|webp)$/i.test(selected.name);

  const content = useQuery({
    queryKey: ["artifact-content", url, selected?.name, selected?.modifiedAt],
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

      putChatDraft(agentId, target.id, reference(agentId, item));
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
          description="Files in this bot’s artifacts directory. All conversations in this bot show the same files."
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
                  key={item.name}
                  variant={selectedName === item.name ? "secondary" : "outline"}
                  className="h-auto justify-start whitespace-normal text-left [overflow-wrap:anywhere]"
                  onClick={() => {
                    setSelectedName(item.name);
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
                <img
                  className="max-h-96 max-w-full object-contain"
                  src={`${url}?v=${encodeURIComponent(selected.modifiedAt)}`}
                  alt={selected.name}
                />
              ) : (
                <p className="text-sm text-muted-foreground">Download this file to open it.</p>
              )}
            </div>
          ) : null}
          {selected && editing ? (
            <Modal
              title="Rename artifact"
              description="Rename this bot file. Existing file names cannot be replaced."
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
                  rename.mutate({ from: selected.name, to: name });
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
              description={`Delete “${selected.name}” from this bot’s artifacts directory? This action cannot be undone.`}
              confirmLabel="Delete artifact"
              pending={remove.isPending}
              error={remove.error}
              onCancel={() => setConfirm(false)}
              onConfirm={() => remove.mutate(selected.name)}
            />
          ) : null}
        </Modal>
      ) : null}
    </>
  );
}
