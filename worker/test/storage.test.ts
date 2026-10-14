// pi-durable tests its SQLite adapter on Node only. The worker runs it on Bun's node:sqlite, so
// this runs the shared storage conformance suite on Bun.
import { describe, expect, it } from "bun:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { registerStorageConformance } from "@earendil-works/pi-durable/testing";

registerStorageConformance({ describe, expect, it }, "node:sqlite on Bun", async (use) => {
	const dir = await mkdtemp(join(tmpdir(), "dex-storage-"));
	const storage = await openNodeSqliteStorage(join(dir, "state.sqlite"));
	try {
		await use(storage);
	} finally {
		await storage.close(BACKGROUND_CONTEXT);
		await rm(dir, { recursive: true, force: true });
	}
});
