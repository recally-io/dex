import { useApp } from "./app-state";
import { useAgentId } from "./agent-scope";
import { useEffect, useRef, useState } from "react";
import type { Attachment } from "../api/generated/types.gen";
import { createUpload } from "../api/generated/sdk.gen";
import { errorText } from "./queries";
import { newId } from "./utils";

// The limits of control: internal/upload.MaxSize and the attachments of MessageInput.
export const maxFiles = 10;
export const maxSize = 20 << 20;

// The image types that GET /api/agents/{agentId}/uploads/{id} serves inline.
const inline = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

export const isImage = (mimeType: string) => inline.has(mimeType);

export function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;

  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;

  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** A file in the composer. It is ready when the upload has its attachment. */
export type DraftFile = {
  key: string;
  name: string;
  size: number;
  preview?: string;
  upload?: Attachment;
  error?: string;
};

export type Uploads = Map<string, { abort: AbortController; preview?: string }>;

export function useAttachments(persist = false) {
  const agentId = useAgentId();
  const app = useApp();
  const [localFiles, setLocalFiles] = useState<DraftFile[]>([]);
  const files = persist ? app.composer.files : localFiles;
  const setFiles = persist ? app.setComposerFiles : setLocalFiles;
  const [notice, setNotice] = useState("");
  const localUploads = useRef<Uploads>(new Map());
  const uploads = persist ? app.composerUploads : localUploads;

  const release = (key: string) => {
    const upload = uploads.current.get(key);

    upload?.abort.abort();

    if (upload?.preview) URL.revokeObjectURL(upload.preview);
    uploads.current.delete(key);
  };

  useEffect(() => {
    if (persist) return;
    const current = localUploads.current;

    return () => {
      for (const upload of current.values()) {
        upload.abort.abort();

        if (upload.preview) URL.revokeObjectURL(upload.preview);
      }
    };
  }, [persist]);

  function update(key: string, change: Partial<DraftFile>) {
    setFiles((previous) => previous.map((f) => (f.key === key ? { ...f, ...change } : f)));
  }

  function add(list: File[]) {
    const room = maxFiles - files.length;
    const taken = list.slice(0, Math.max(0, room));
    const refused = list.length - taken.length;

    setNotice(
      refused
        ? `A message can have at most ${maxFiles} files. ${refused === 1 ? "1 file was" : `${refused} files were`} not added.`
        : "",
    );

    const added = taken.map((file): DraftFile => {
      const key = newId();
      const preview = isImage(file.type) ? URL.createObjectURL(file) : undefined;
      const abort = new AbortController();
      uploads.current.set(key, { abort, preview });

      if (file.size > maxSize)
        return { key, name: file.name, size: file.size, preview, error: "Larger than 20 MB" };

      createUpload({ path: { agentId }, body: { file }, signal: abort.signal, throwOnError: true }).then(
        ({ data }) => update(key, { upload: data }),
        (cause: unknown) => {
          if (!abort.signal.aborted) update(key, { error: errorText(cause) });
        },
      );

      return { key, name: file.name, size: file.size, preview };
    });

    setFiles((previous) => [...previous, ...added]);
  }

  function remove(key: string) {
    release(key);
    setNotice("");
    setFiles((previous) => previous.filter((f) => f.key !== key));
  }

  // Removes the files of a sent message; files added during the send stay.
  function sent(ids: string[]) {
    const gone = files.filter((f) => f.upload && ids.includes(f.upload.id));

    for (const f of gone) release(f.key);
    setFiles((previous) => previous.filter((f) => !gone.some((g) => g.key === f.key)));
  }

  return {
    files,
    notice,
    add,
    remove,
    sent,
    ready: files.flatMap((f) => (f.upload ? [f.upload] : [])),
    uploading: files.some((f) => !f.upload && !f.error),
    failed: files.some((f) => !!f.error),
  };
}

export type Attachments = ReturnType<typeof useAttachments>;
