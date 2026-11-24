import type { TelegramOptions, TelegramSpeechOptions } from "../api/generated/types.gen";
import { Field, Input, Select } from "./form";
import { Note } from "./page";

export function TelegramOptionsFields({
  value,
  onChange,
}: {
  value: TelegramOptions;
  onChange: (next: TelegramOptions) => void;
}) {
  return (
    <>
      <details>
        <summary className="cursor-pointer text-[13px] text-muted-foreground">Group input</summary>
        <div className="mt-3 flex flex-col gap-4">
          <Field
            label="Require group mention"
            hint="Off by default. Only paired senders can trigger work. Group resources stay isolated in either mode."
          >
            {({ id }) => (
              <Select
                id={id}
                value={value.requireMention ? "on" : "off"}
                onChange={(e) => onChange({ ...value, requireMention: e.target.value === "on" })}
              >
                <option value="off">Off, respond to paired senders</option>
                <option value="on">On, require a mention or reply</option>
              </Select>
            )}
          </Field>
        </div>
      </details>
      <details>
        <summary className="cursor-pointer text-[13px] text-muted-foreground">Delivery and previews</summary>
        <div className="mt-3 flex flex-col gap-4">
          <Field label="Silent delivery" hint="Send new bot messages without notification sound.">
            {({ id }) => (
              <Select
                id={id}
                value={value.silent ? "on" : "off"}
                onChange={(e) => onChange({ ...value, silent: e.target.value === "on" })}
              >
                <option value="off">Off</option>
                <option value="on">On</option>
              </Select>
            )}
          </Field>
          <Field label="Answer preview">
            {({ id }) => (
              <Select
                id={id}
                value={value.preview || "auto"}
                onChange={(e) => {
                  // SAFETY: This select contains only the preview values in the API contract.
                  onChange({ ...value, preview: e.target.value as TelegramOptions["preview"] });
                }}
              >
                <option value="auto">Draft, with edit fallback</option>
                <option value="draft">Prefer drafts, with edit fallback</option>
                <option value="edit">Edit one preview message</option>
                <option value="off">Off, including typing</option>
              </Select>
            )}
          </Field>
          <Field label="Private chat status reactions" hint="Show the status on your incoming message.">
            {({ id }) => (
              <Select
                id={id}
                value={value.reactions === false ? "off" : "on"}
                onChange={(e) => onChange({ ...value, reactions: e.target.value === "on" })}
              >
                <option value="off">Off</option>
                <option value="on">On</option>
              </Select>
            )}
          </Field>
          <Field label="Group status reactions" hint="Show status only for authorized group messages.">
            {({ id }) => (
              <Select
                id={id}
                value={value.groupReactions ? "on" : "off"}
                onChange={(e) => onChange({ ...value, groupReactions: e.target.value === "on" })}
              >
                <option value="off">Off</option>
                <option value="on">On</option>
              </Select>
            )}
          </Field>
        </div>
      </details>
      <details>
        <summary className="cursor-pointer text-[13px] text-muted-foreground">Optional speech</summary>
        <div className="mt-3 flex flex-col gap-4">
          <Note tone="warn">
            Speech is off by default. Transcription sends audio to the selected provider. This can incur
            charges. dex does not choose another provider or retry an unknown request.
          </Note>
          <TranscriptionFields value={value.stt ?? {}} onChange={(stt) => onChange({ ...value, stt })} />
          <p className="text-[12px] text-muted-foreground">
            Default limits: 30 seconds per request, 20 MiB, and 300 seconds of reported input audio. Original
            incoming audio stays attached.
          </p>
        </div>
      </details>
    </>
  );
}

function TranscriptionFields({
  value,
  onChange,
}: {
  value: TelegramSpeechOptions;
  onChange: (next: TelegramSpeechOptions) => void;
}) {
  const title = "Transcription (STT)";

  return (
    <fieldset className="flex flex-col gap-4">
      <legend className="text-[13px] font-medium">{title}</legend>
      <Field label={`${title} enabled`}>
        {({ id }) => (
          <Select
            id={id}
            value={value.enabled ? "on" : "off"}
            onChange={(e) => onChange({ ...value, enabled: e.target.value === "on" })}
          >
            <option value="off">Off</option>
            <option value="on">On</option>
          </Select>
        )}
      </Field>
      {value.enabled ? (
        <>
          <Field
            label={`${title} provider ID`}
            hint="Use a saved custom provider with an explicit base URL and OpenAI-compatible audio support. A chat model does not prove speech support."
          >
            {({ id, describedBy }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                value={value.provider ?? ""}
                onChange={(e) => onChange({ ...value, provider: e.target.value })}
              />
            )}
          </Field>
          <Field label={`${title} model`}>
            {({ id }) => (
              <Input
                id={id}
                value={value.model ?? ""}
                onChange={(e) => onChange({ ...value, model: e.target.value })}
              />
            )}
          </Field>
          <Field label="STT language" hint="Optional provider language code.">
            {({ id }) => (
              <Input
                id={id}
                value={value.language ?? ""}
                onChange={(e) => onChange({ ...value, language: e.target.value })}
              />
            )}
          </Field>
          <Field
            label="Allow sending audio to this provider"
            hint="Explicit consent is required before speech can start."
          >
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                value={value.allowSending ? "yes" : "no"}
                onChange={(e) => onChange({ ...value, allowSending: e.target.value === "yes" })}
              >
                <option value="no">No</option>
                <option value="yes">Yes, send to the selected provider</option>
              </Select>
            )}
          </Field>
        </>
      ) : null}
    </fieldset>
  );
}
