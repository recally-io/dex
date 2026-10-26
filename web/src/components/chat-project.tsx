import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { setChatProjectMutation } from "../api/generated/@tanstack/react-query.gen";
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
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState(cwd);

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

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);

        if (nextOpen) {
          setPath(cwd);
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
        <span className="truncate">{cwd ? cwd.split("/").filter(Boolean).pop() || "/" : "No project"}</span>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Project directory</DialogTitle>
          <DialogDescription>
            Choose an existing absolute directory. Changes apply from the next message.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            submit(path);
          }}
        >
          <label className="grid gap-2">
            Absolute path
            <Input
              value={path}
              onChange={(event) => setPath(event.target.value)}
              placeholder="/path/to/project"
              autoFocus
            />
          </label>
          {save.isError ? (
            <p role="alert" className="text-destructive">
              {errorText(save.error)}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={save.isPending} onClick={() => submit("")}>
              Clear project
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
