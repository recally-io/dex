// Inputs run in order so every context boundary has committed entries.
/* oxlint-disable no-await-in-loop */
import { expect, test } from "bun:test";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { Harness, createRegistry, watchEvents, type EntryRecord } from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { Catalog } from "../src/models.ts";
import { Agents } from "../src/agents.ts";
import { ChatView, render, USER_ENTRY, type Item } from "../src/view.ts";
import type { ChatStreamEvent } from "../src/protocol.ts";

const ctx = BACKGROUND_CONTEXT;

test("user reads every committed item once after real reset and compaction", async () => {
	// Given a worker-owned SQLite conversation with ordinary inputs and a real tool result.
	const dir = await mkdtemp(join(tmpdir(), "dex-history-"));
	const agentDir = join(dir, "agents", "bot");
	await mkdir(agentDir, { recursive: true });
	const models = new Catalog({ DEX_FAKE_MODEL: "1" }).models;
	const registry = createRegistry();
	registry.install(CodingTools);
	const harness = await Harness.open(
		await openNodeSqliteStorage(join(agentDir, "state.sqlite")),
		{
			models,
			registry,
			env: () => new NodeExecutionEnv({ cwd: dir }),
			settings: { compaction: { enabled: false, keepRecentTokens: 1 } },
		},
		ctx,
	);
	let closed = false;
	try {
		const conversation = await harness.createConversation(
			{ ownership: { kind: "ownerless" }, agent: { model: { provider: "faux", modelId: "echo" } } },
			ctx,
		);
		for (let i = 0; i < 3; i++) {
			await (
				await conversation.submit(
					{
						type: "input",
						content: `before ${i}`,
						requestId: `before-${i}`,
						entry: { kind: USER_ENTRY, data: { requestId: `before-${i}` } },
					},
					ctx,
				)
			).wait(ctx);
		}
		// When the SDK resets and then compacts the context, both heads hide older entries.
		await conversation.configure({ model: { provider: "faux", modelId: "bash" } }, ctx);
		await (await conversation.submit({ type: "input", content: "printf history-tool" }, ctx)).wait(ctx);
		await conversation.configure({ model: { provider: "faux", modelId: "echo" } }, ctx);
		await (
			await conversation.submit(
				{ type: "write", requestId: "reset-1", entry: { kind: "pi.reset", head: "self" } },
				ctx,
			)
		).wait(ctx);
		for (let i = 0; i < 3; i++) {
			await (
				await conversation.submit(
					{
						type: "input",
						content: `after ${i}`,
						requestId: `after-${i}`,
						entry: { kind: USER_ENTRY, data: { requestId: `after-${i}` } },
					},
					ctx,
				)
			).wait(ctx);
		}
		const task = await conversation.compact(undefined, ctx);
		const result = await harness.waitForTask(task, ctx);
		expect(result.state.outcome.status).toBe("completed");
		const records: EntryRecord[] = [];
		let cursor;
		do {
			const page = await conversation.entries({ order: "ascending" }, 100, cursor, ctx);
			records.push(...page.items);
			cursor = page.next;
		} while (cursor !== undefined);
		const expected = render({ entries: records, tools: new Map(), running: false });
		const stream = await watchEvents(harness, conversation.id, ctx);
		const fresh = new ChatView().snapshot(stream.snapshot);
		await stream.stop();
		if (fresh.type !== "snapshot") throw new Error("expected snapshot");
		expect(fresh.items.some((item) => item.kind === "boundary" && item.boundary === "compaction")).toBe(true);
		expect(fresh.items.length).toBeLessThan(expected.length);
		await harness.close(ctx);
		closed = true;

		// Then the real worker history reader fills a fresh snapshot with stable, ordered IDs.
		const events: ChatStreamEvent[] = [];
		const agents = new Agents(
			dir,
			models,
			() => {},
			(event) => events.push(event),
		);
		try {
			await agents.watch({ agentId: "bot", conversationId: conversation.id, watchId: "fresh" });
			const snapshot = events
				.flatMap((event) => ("events" in event ? event.events : []))
				.find((event) => event.type === "snapshot");
			if (snapshot?.type !== "snapshot" || snapshot.historyBefore === undefined)
				throw new Error("missing history cursor");
			const items = new Map<string, Item>(snapshot.items.map((item) => [item.id, item]));
			let before: number | undefined = snapshot.historyBefore;
			let pages = 0;
			while (before !== undefined) {
				const page = await agents.history({
					agentId: "bot",
					conversationId: conversation.id,
					before,
					limit: 1,
				});
				for (const item of page.items) items.set(item.id, item);
				expect(page.nextBefore === undefined || page.nextBefore < before).toBe(true);
				before = page.nextBefore;
				if (++pages > 100) throw new Error("history did not terminate");
			}
			const complete = [...items.values()].toSorted((a, b) => (a.entryId ?? 0) - (b.entryId ?? 0));
			expect(complete).toEqual(expected);
			console.error(
				`history artifact: ${pages} pages, ${complete.length} items, reset and compaction retained`,
			);
		} finally {
			await agents.close();
		}
	} finally {
		if (!closed) await harness.close(ctx);
		await rm(dir, { recursive: true, force: true });
	}
});
