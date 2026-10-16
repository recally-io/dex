import { expect, test } from "bun:test";
import Type from "typebox";
import { Value } from "typebox/value";
import { Catalog } from "../src/models.ts";
import { SpawnTaskArgs } from "../src/tasks.ts";

const ResponsesPayload = Type.Object({
	tools: Type.Array(
		Type.Object({
			name: Type.String(),
			strict: Type.Optional(Type.Boolean()),
			parameters: Type.Object({
				type: Type.Literal("object"),
				properties: Type.Record(Type.String(), Type.Unknown()),
				required: Type.Array(Type.String()),
				additionalProperties: Type.Boolean(),
			}),
		}),
	),
});

for (const discover of [false, true]) {
	test(`custom Responses preserves optional spawn arguments with ${discover ? "discovered" : "explicit"} models`, async () => {
		// Given a real catalog and a local model discovery endpoint.
		const requests: string[] = [];
		const server = Bun.serve({
			hostname: "127.0.0.1",
			port: 0,
			fetch(request) {
				const path = `${request.method} ${new URL(request.url).pathname}`;
				requests.push(path);
				return path === "GET /v1/models"
					? Response.json({ data: [{ id: "test-model" }] })
					: new Response("Unexpected model request", { status: 500 });
			},
		});
		try {
			const catalog = new Catalog({});
			expect(
				await catalog.configure([
					{
						id: "local-test",
						name: "Local test",
						api: "openai-responses",
						baseUrl: `${server.url.origin}/v1`,
						apiKey: "",
						models: discover ? [] : ["test-model"],
					},
				]),
			).toEqual({ errors: {} });
			const model = catalog.models.getModel("local-test", "test-model");
			if (model === undefined) throw new Error("Configured model is missing");
			let payload: unknown;
			// When pi-ai builds the real provider request, stop before network access.
			const result = await catalog.models.complete(
				model,
				{
					messages: [
						{
							role: "system",
							content: "Delegate the goal.",
							toolsAdded: [
								{ name: "spawn_task", description: "Delegate a goal.", parameters: SpawnTaskArgs },
							],
							timestamp: 0,
						},
						{ role: "user", content: "Run the test goal.", timestamp: 0 },
					],
				},
				{
					maxRetries: 0,
					onPayload(value) {
						payload = structuredClone(value);
						throw new Error("Payload captured before network access");
					},
				},
			);
			// Then the wire schema accepts omitted options and explicitly disables strict mode.
			expect(result.errorMessage).toContain("Payload captured before network access");
			expect(requests).toEqual(discover ? ["GET /v1/models"] : []);
			const spawn = Value.Parse(ResponsesPayload, payload).tools.find((tool) => tool.name === "spawn_task");
			if (spawn === undefined) throw new Error("Outgoing spawn_task declaration is missing");
			expect(spawn.parameters.required).toEqual(["title", "goal"]);
			expect(Value.Check(Type.Unsafe(spawn.parameters), { title: "Test", goal: "Run the test goal" })).toBe(
				true,
			);
			expect(spawn.strict).toBe(false);
		} finally {
			await server.stop(true);
		}
	});
}
