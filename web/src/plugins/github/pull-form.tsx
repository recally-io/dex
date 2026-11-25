import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { bindTaskGitHubPull } from "../../api/generated/sdk.gen";
import type { BindTaskGitHubPullData } from "../../api/generated/types.gen";
import { Failure } from "../../components/feedback";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Label } from "../../components/ui/label";
import { invalidateBoard } from "../../lib/board-stream";

function positiveId(value: string) {
  return /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value));
}

// GitHubPullForm links a PR to an open Task. The GitHub plugin owns the API.
export function GitHubPullForm({ path }: { path: BindTaskGitHubPullData["path"] }) {
  const [repository, setRepository] = useState("");
  const [number, setNumber] = useState("");
  const [commit, setCommit] = useState("");
  const client = useQueryClient();

  const bind = useMutation({
    mutationFn: (body: BindTaskGitHubPullData["body"]) =>
      bindTaskGitHubPull({ path, body, throwOnError: true }),
    onSuccess: async () => {
      setRepository("");
      setNumber("");
      setCommit("");
      await invalidateBoard(client);
    },
  });

  return (
    <>
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();

          if (bind.isPending || !positiveId(repository) || !positiveId(number)) return;

          const revision = commit.trim();
          bind.mutate({
            repositoryId: Number(repository),
            pullNumber: Number(number),
            wait: revision ? { revision, afterAt: Date.now() } : undefined,
          });
        }}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1">
            <Label htmlFor="task-github-repository">Repository ID</Label>
            <Input
              id="task-github-repository"
              inputMode="numeric"
              pattern="[1-9][0-9]*"
              required
              disabled={bind.isPending}
              value={repository}
              onChange={(event) => setRepository(event.target.value)}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="task-github-pull">PR number</Label>
            <Input
              id="task-github-pull"
              inputMode="numeric"
              pattern="[1-9][0-9]*"
              required
              disabled={bind.isPending}
              value={number}
              onChange={(event) => setNumber(event.target.value)}
            />
          </div>
        </div>
        <div className="space-y-1">
          <Label htmlFor="task-github-commit">Commit SHA (optional)</Label>
          <Input
            id="task-github-commit"
            aria-describedby="task-github-wait-help"
            maxLength={512}
            disabled={bind.isPending}
            value={commit}
            onChange={(event) => setCommit(event.target.value)}
          />
          <p id="task-github-wait-help" className="text-sm text-muted-foreground">
            Enter a commit SHA to wait for a new review of that commit. Leave it blank to link the PR without
            a wait.
          </p>
        </div>
        <Button
          type="submit"
          variant="secondary"
          disabled={bind.isPending || !positiveId(repository) || !positiveId(number)}
        >
          Link pull request
        </Button>
      </form>
      {bind.error ? <Failure title="Could not link pull request" error={bind.error} /> : null}
    </>
  );
}
