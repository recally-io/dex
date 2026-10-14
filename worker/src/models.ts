import {
	createProvider,
	envApiKeyAuth,
	type Model,
	type MutableModels,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { builtinModels } from "@earendil-works/pi-ai/providers/all";
import {
	type FauxResponseFactory,
	fauxAssistantMessage,
	fauxProvider,
} from "@earendil-works/pi-ai/providers/faux";
import Type from "typebox";
import { Value } from "typebox/value";

/**
 * All built-in providers; each reads its API key from the environment. Two more, when configured:
 *
 * - `openai-compatible`: any OpenAI-compatible gateway at `OPENAI_BASE_URL`, with the models its
 *   `/models` endpoint lists. The built-in `openai` provider ignores `OPENAI_BASE_URL`.
 * - `faux/echo` with `DEX_FAKE_MODEL=1`: answers "echo: <last user text>". Tests use it so they need
 *   no provider credentials.
 */
export async function createModels(env: NodeJS.ProcessEnv): Promise<MutableModels> {
	const models = builtinModels();
	const baseUrl = env["OPENAI_BASE_URL"];
	if (baseUrl !== undefined && baseUrl !== "") {
		models.setProvider(openAICompatibleProvider(baseUrl));
		const refresh = await models.refresh({ providers: [COMPATIBLE], allowNetwork: true });
		const error = refresh.errors.get(COMPATIBLE);
		// The worker still serves the other providers; requests for these models fail until a restart.
		if (error !== undefined) console.error(`listing models at ${baseUrl} failed:`, error);
	}
	if (env["DEX_FAKE_MODEL"] === "1") {
		const faux = fauxProvider({ provider: "faux", models: [{ id: "echo" }] });
		// Each call consumes one scripted response, so the factory queues the next one.
		const echo: FauxResponseFactory = (transcript) => {
			faux.appendResponses([echo]);
			return fauxAssistantMessage(`echo: ${lastUserText(transcript)}`);
		};
		faux.setResponses([echo]);
		models.setProvider(faux.provider);
	}
	return models;
}

const COMPATIBLE = "openai-compatible";

// The fields dex reads from an OpenAI-compatible `/models` response. Gateways add others.
const ModelList = Type.Object({
	data: Type.Array(
		Type.Object({
			id: Type.String(),
			context_window: Type.Optional(Type.Number()),
			max_output_tokens: Type.Optional(Type.Number()),
			reasoning: Type.Optional(Type.Boolean()),
		}),
	),
});

function openAICompatibleProvider(baseUrl: string) {
	return createProvider<"openai-completions">({
		id: COMPATIBLE,
		name: "OpenAI-compatible gateway",
		baseUrl,
		auth: { apiKey: envApiKeyAuth("OpenAI-compatible API key", ["OPENAI_API_KEY"]) },
		models: [],
		api: openAICompletionsApi(),
		fetchModels: async ({ credential, signal }) => {
			const headers: Record<string, string> = {};
			if (credential?.type === "api_key") headers["Authorization"] = `Bearer ${credential.key}`;
			const response = await fetch(`${baseUrl}/models`, { headers, signal });
			if (!response.ok) throw new Error(`GET ${baseUrl}/models: HTTP ${response.status}`);
			const list = Value.Parse(ModelList, await response.json());
			return list.data.map((entry): Model<"openai-completions"> => ({
				id: entry.id,
				name: entry.id,
				api: "openai-completions",
				provider: COMPATIBLE,
				baseUrl,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				reasoning: entry.reasoning ?? false,
				// Fallbacks for gateways that do not report limits.
				contextWindow: entry.context_window ?? 128_000,
				maxTokens: entry.max_output_tokens ?? 16_384,
			}));
		},
	});
}

function lastUserText(transcript: TranscriptContext): string {
	const user = transcript.messages.findLast((message) => message.role === "user");
	if (user === undefined) return "";
	if (typeof user.content === "string") return user.content;
	return user.content.map((block) => (block.type === "text" ? block.text : "")).join("");
}
