import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  createAgentMutation,
  getAgentOptions,
  listAgentsQueryKey,
  listEnvironmentsOptions,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { findModel } from "../lib/agent";
import { environmentOptionLabel } from "../lib/environments";
import { queryClient } from "../lib/queries";
import { Modal } from "./dialog";
import { Field, Input, Select } from "./form";
import { Button } from "./ui/button";
import { Failure, Loading } from "./feedback";
import { ModelPicker } from "./model-picker";
import type { Agent, Environment, Model } from "../api/generated/types.gen";

export function AgentCreateDialog({ onClose }: { onClose: () => void }) {
  // A new bot starts with a copy of the default bot's model, fallback, and environment.
  const template = useQuery(getAgentOptions({ path: { agentId: "default" } }));
  const models = useQuery(listModelsOptions());
  const environments = useQuery(listEnvironmentsOptions());
  const error = template.error ?? environments.error;

  return template.data && environments.data ? (
    <CreateForm
      defaults={template.data}
      models={models.data?.models}
      environments={environments.data.environments}
      onClose={onClose}
    />
  ) : (
    <Modal title="Create bot" onClose={onClose} footer={<Button onClick={onClose}>Cancel</Button>}>
      {error ? <Failure error={error} /> : <Loading />}
    </Modal>
  );
}

function CreateForm({
  defaults,
  models,
  environments,
  onClose,
}: {
  defaults: Agent;
  models: Model[] | undefined;
  environments: Environment[];
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [model, setModel] = useState({ provider: defaults.provider, modelId: defaults.modelId });
  const [modelName, setModelName] = useState<string | null>(null);

  const [fallback, setFallback] = useState({
    provider: defaults.fallbackProvider,
    modelId: defaults.fallbackModelId,
  });

  const [fallbackName, setFallbackName] = useState<string | null>(null);

  const [environmentId, setEnvironmentId] = useState(
    environments.some((env) => env.id === defaults.environmentId) ? defaults.environmentId : "",
  );

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

  const primaryLabel =
    modelName ?? (model.modelId ? findModel(model.provider, model.modelId, models).label : "Choose model");

  const fallbackLabel =
    fallbackName ??
    (fallback.modelId ? findModel(fallback.provider, fallback.modelId, models).label : "None");

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
            create.mutate({
              body: {
                name: trimmed,
                provider: model.provider,
                modelId: model.modelId,
                thinkingLevel: defaults.thinkingLevel,
                fallbackProvider: fallback.provider,
                fallbackModelId: fallback.modelId,
                environmentId,
              },
            });
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
              side="top"
              override={{
                value: model.modelId ? model : null,
                defaultName: "None",
                clearLabel: "No primary model",
                save: async (selected: Model | null) => {
                  if (selected) {
                    setModel({ provider: selected.provider, modelId: selected.id });
                    setModelName(selected.name);
                  } else {
                    setModel({ provider: "", modelId: "" });
                    setModelName(null);
                  }
                },
              }}
              trigger={
                <Button id={id} variant="secondary" className="w-fit max-w-full self-start truncate">
                  {primaryLabel}
                </Button>
              }
            />
          )}
        </Field>
        <Field label="Fallback model">
          {({ id }) => (
            <ModelPicker
              side="top"
              override={{
                value: fallback.modelId ? fallback : null,
                defaultName: "None",
                clearLabel: "No fallback model",
                save: async (selected: Model | null) => {
                  if (selected) {
                    setFallback({ provider: selected.provider, modelId: selected.id });
                    setFallbackName(selected.name);
                  } else {
                    setFallback({ provider: "", modelId: "" });
                    setFallbackName(null);
                  }
                },
              }}
              trigger={
                <Button id={id} variant="secondary" className="w-fit max-w-full self-start truncate">
                  {fallbackLabel}
                </Button>
              }
            />
          )}
        </Field>
        {environments.length > 0 ? (
          <Field label="Execution environment">
            {({ id }) => (
              <Select
                id={id}
                value={environmentId}
                onChange={(event) => setEnvironmentId(event.target.value)}
              >
                <option value="">This computer (local)</option>
                {environments.map((env) => (
                  <option key={env.id} value={env.id}>
                    {environmentOptionLabel(env)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ) : null}
        {create.isError ? <Failure title="Could not create bot" error={create.error} /> : null}
      </form>
    </Modal>
  );
}
