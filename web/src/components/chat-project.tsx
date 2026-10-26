import { useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowUp, Folder, GitBranch, House } from "lucide-react";
import {
  listDirectoriesOptions,
  listRecentProjectsOptions,
  setChatProjectMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { errorText, refreshChats } from "../lib/queries";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "./ui/dialog";

export function ChatProject({ chatId, cwd }: { chatId: number; cwd: string }) {
  const agentId = useAgentId();
  const pathInput = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState(cwd || "~");
  const [draft, setDraft] = useState(cwd || "~");

  const listing = useQuery({
    ...listDirectoriesOptions({ query: { path } }),
    enabled: open,
    retry: false,
  });

  const recent = useQuery({
    ...listRecentProjectsOptions({ path: { agentId } }),
    enabled: open,
  });

  const save = useMutation({
    ...setChatProjectMutation(),
    onSuccess: async () => {
      setOpen(false);
      await refreshChats(agentId);
    },
  });

  function submit(directory: string) {
    save.mutate({ path: { agentId, chatId }, body: { cwd: directory } });
  }

  function navigate(directory: string) {
    setPath(directory);
    setDraft(directory);
    pathInput.current?.focus();
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);

        if (nextOpen) {
          navigate(cwd || "~");
          save.reset();
        }
      }}
    >
      <DialogTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="max-w-full"
            title={cwd || "No project"}
            aria-label="Set project directory"
          />
        }
      >
        <span className="truncate">{cwd ? folderName(cwd) : "No project"}</span>
      </DialogTrigger>
      <DialogContent className="flex h-[min(40rem,calc(100dvh-2rem))] flex-col">
        <DialogHeader>
          <DialogTitle>Project directory</DialogTitle>
          <DialogDescription>
            Choose a folder on the dex host. Changes apply from the next message.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-3">
          <section
            aria-label="Recent projects"
            className="grid max-h-40 min-w-0 shrink-0 gap-1 overflow-y-auto"
          >
            <h3 className="mb-1 text-sm font-medium">Recent</h3>
            {recent.isPending ? (
              <p className="text-sm text-muted-foreground">Loading recent projects…</p>
            ) : null}
            {recent.isError ? (
              <p role="alert" className="text-sm text-destructive">
                {errorText(recent.error)}
              </p>
            ) : null}
            {recent.data?.projects.length === 0 ? (
              <p className="text-sm text-muted-foreground">No recent projects</p>
            ) : null}
            {recent.data?.projects.map((project) => (
              <Button
                key={project}
                variant="ghost"
                className="min-w-0 justify-start"
                disabled={save.isPending}
                onClick={() => submit(project)}
                title={project}
              >
                <Folder aria-hidden="true" />
                <span className="shrink-0">{folderName(project)}</span>
                {/* The start of a long path matters least, so the path is cut from the left. */}
                <span className="min-w-0 truncate text-muted-foreground [direction:rtl]">
                  <bdi dir="ltr">{project}</bdi>
                </span>
              </Button>
            ))}
          </section>
          <form
            className="flex shrink-0 gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              navigate(draft);
            }}
          >
            <Input
              ref={pathInput}
              aria-label="Directory path"
              value={draft === path && listing.data ? listing.data.path : draft}
              onChange={(event) => setDraft(event.target.value)}
            />
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label="Up"
              disabled={!listing.data?.parent || listing.isFetching}
              onClick={() => navigate(listing.data!.parent)}
            >
              <ArrowUp />
            </Button>
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label="Home"
              onClick={() => navigate(listing.data?.home || "~")}
            >
              <House />
            </Button>
          </form>
          <div
            className="grid min-h-0 flex-1 content-start gap-1 overflow-y-auto"
            aria-label="Subdirectories"
            aria-busy={listing.isFetching}
          >
            {listing.isFetching ? (
              <p role="status" className="text-sm text-muted-foreground">
                Loading directories…
              </p>
            ) : null}
            {listing.isError ? (
              <p role="alert" className="text-sm text-destructive">
                {errorText(listing.error)}
              </p>
            ) : null}
            {!listing.isFetching && !listing.isError
              ? listing.data?.entries.map((entry) => (
                  <Button
                    key={entry.path}
                    variant="ghost"
                    className="min-w-0 justify-start"
                    onClick={() => navigate(entry.path)}
                  >
                    {entry.git ? <GitBranch aria-label="Git repository" /> : <Folder aria-hidden="true" />}
                    <span className="truncate">{entry.name}</span>
                  </Button>
                ))
              : null}
            {!listing.isFetching && listing.data?.entries.length === 0 ? (
              <p className="text-sm text-muted-foreground">No subdirectories</p>
            ) : null}
            {listing.data?.truncated ? (
              <p className="text-sm text-muted-foreground">
                Showing the first 500 folders. Enter a path to open another folder.
              </p>
            ) : null}
          </div>
        </div>
        {save.isError ? (
          <p role="alert" className="text-sm text-destructive">
            {errorText(save.error)}
          </p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" disabled={save.isPending} onClick={() => submit("")}>
            Clear project
          </Button>
          <Button variant="outline" disabled={save.isPending} onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            disabled={save.isPending || listing.isFetching || listing.isError || !listing.data}
            onClick={() => submit(listing.data!.path)}
          >
            {save.isPending ? "Saving…" : "Use this folder"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function folderName(path: string) {
  return path.split("/").filter(Boolean).pop() || "/";
}
