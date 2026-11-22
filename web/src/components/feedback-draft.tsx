import { useId, useState } from "react";
import { buildFeedbackDraft } from "../api/generated";
import type { BuildFeedbackDraftResponse } from "../api/generated";
import { Modal } from "./dialog";
import { Button, buttonVariants } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";

export function FeedbackDraft({ onClose }: { onClose: () => void }) {
  const id = useId();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [draft, setDraft] = useState<BuildFeedbackDraftResponse>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");

  async function preview() {
    setPending(true);
    setError("");

    try {
      const result = await buildFeedbackDraft({ body: { title, description }, cache: "no-store" });

      if (result.data) setDraft(result.data);
      else
        setError(
          result.response?.status === 400
            ? (result.error?.error ?? "Check the draft fields.")
            : "Could not preview the draft. Try again.",
        );
    } catch {
      setError("Could not preview the draft. Try again.");
    } finally {
      setPending(false);
    }
  }

  async function copy(text: string, field: string) {
    try {
      await navigator.clipboard.writeText(text);
      setStatus(`${field} copied.`);
    } catch {
      setStatus(`Select the ${field.toLowerCase()} to copy it manually.`);
    }
  }

  return (
    <Modal
      title="Feedback"
      description={`${draft?.repository ?? "recally-io/dex"} is public. Remove secrets and private details before posting. Dex does not post on your behalf.`}
      onClose={onClose}
      pending={pending}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose} disabled={pending}>
            Close
          </Button>
          {draft ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() => {
                  setDraft(undefined);
                  setStatus("");
                }}
              >
                Edit draft
              </Button>
              <a href={draft.newIssueUrl} target="_blank" rel="noreferrer" className={buttonVariants()}>
                Open GitHub new issue
              </a>
            </>
          ) : (
            <Button
              type="submit"
              form={`${id}-feedback`}
              disabled={pending || !title.trim() || !description.trim()}
            >
              Preview draft
            </Button>
          )}
        </>
      }
    >
      <form
        id={`${id}-feedback`}
        className="grid gap-4"
        onSubmit={(event) => {
          event.preventDefault();

          if (!draft && !pending) void preview();
        }}
      >
        <div className="grid gap-2">
          <Label htmlFor={`${id}-title`}>Title</Label>
          <Input
            id={`${id}-title`}
            value={draft?.title ?? title}
            onChange={(event) => setTitle(event.target.value)}
            readOnly={!!draft || pending}
            required
            maxLength={120}
          />
          {draft ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="justify-self-start"
              onClick={() => void copy(draft.title, "Title")}
            >
              Copy title
            </Button>
          ) : null}
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${id}-description`}>Description</Label>
          <Textarea
            id={`${id}-description`}
            value={draft?.description ?? description}
            onChange={(event) => setDescription(event.target.value)}
            readOnly={!!draft || pending}
            required
            maxLength={1500}
            rows={6}
          />
          {draft ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="justify-self-start"
              onClick={() => void copy(draft.description, "Description")}
            >
              Copy description
            </Button>
          ) : null}
        </div>
      </form>
      {draft ? (
        <p className="text-xs text-muted-foreground">
          Copy the draft into GitHub and submit it with your own account.
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
      {status ? (
        <p role="status" className="text-xs text-muted-foreground">
          {status}
        </p>
      ) : null}
    </Modal>
  );
}
