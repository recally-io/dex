import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import { createAgentMutation, listAgentsQueryKey } from "../api/generated/@tanstack/react-query.gen";
import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { queryClient } from "../lib/queries";
import { Modal } from "./dialog";
import { Field, Input } from "./form";
import { Button } from "./ui/button";
import { Failure, Loading } from "./feedback";
import { ModelPicker } from "./model-picker";
import type { Agent, Model } from "../api/generated/types.gen";

export function AgentCreateDialog({ onClose }: { onClose: () => void }) {
  const agentId = useAgentId();
  const settings = useAgentSettings(agentId);

  return settings.data ? (
    <CreateForm initial={settings.data.agent} onClose={onClose} />
  ) : (
    <Modal title="Create bot" onClose={onClose} footer={<Button onClick={onClose}>Cancel</Button>}>
      {settings.isError ? <Failure error={settings.error} /> : <Loading />}
    </Modal>
  );
}

function CreateForm({ initial, onClose }: { initial: Agent; onClose: () => void }) {
  const [name, setName] = useState("");
  const [model, setModel] = useState({ provider: initial.provider, modelId: initial.modelId });
  const [modelName, setModelName] = useState(initial.modelId || "Choose model");
  const navigate = useNavigate();

  const create = useMutation({
    ...createAgentMutation(),
    onSuccess: async (agent) => {
      await queryClient.invalidateQueries({ queryKey: listAgentsQueryKey() });
      onClose();
      await navigate({ to: "/a/$agentId", params: { agentId: agent.id! } });
    },
  });

  const trimmed = name.trim();

  return (
    <Modal
      title="Create bot"
      description="Each bot has its own timeline, instructions and model."
      pending={create.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={create.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="create-bot"
            disabled={!trimmed || Array.from(trimmed).length > 40 || create.isPending}
          >
            {create.isPending ? "Creating…" : "Create bot"}
          </Button>
        </>
      }
    >
      <form
        id="create-bot"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();

          if (trimmed && Array.from(trimmed).length <= 40 && !create.isPending)
            create.mutate({ body: { name: trimmed, ...model, thinkingLevel: initial.thinkingLevel } });
        }}
      >
        <Field label="Name">
          {({ id }) => (
            <Input
              id={id}
              autoFocus
              required
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          )}
        </Field>
        <Field label="Model">
          {({ id }) => (
            <ModelPicker
              override={{
                value: model,
                defaultName: modelName,
                allowDefault: false,
                save: async (selected: Model | null) => {
                  if (selected) {
                    setModel({ provider: selected.provider, modelId: selected.id });
                    setModelName(selected.name);
                  }
                },
              }}
              trigger={
                <Button id={id} variant="secondary" className="max-w-full truncate">
                  {modelName}
                </Button>
              }
            />
          )}
        </Field>
        {create.isError ? <Failure title="Could not create bot" error={create.error} /> : null}
      </form>
    </Modal>
  );
}
