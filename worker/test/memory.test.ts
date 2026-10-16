import { expect, test } from "bun:test";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);

test("user memory commits keep an outer Git index unchanged", async () => {
	// Given an outer repository and the index variable that a Git hook exports.
	const dir = await mkdtemp(join(tmpdir(), "dex-memory-hook-"));
	const env = { PATH: process.env["PATH"], HOME: dir };
	try {
		await run("git", ["init", "--quiet", dir], { env });
		await writeFile(join(dir, "keep.txt"), "outer change\n");
		await run("git", ["add", "keep.txt"], { cwd: dir, env });
		const index = join(dir, ".git", "index");
		const before = await readFile(index);
		const memory = join(dir, "memory");
		// When the worker opens memory from that hook, it must use its own repository.
		await run(
			process.execPath,
			[
				"--eval",
				'import { MemoryRepo } from "./src/memory.ts"; await new MemoryRepo(process.argv[1]).open();',
				memory,
			],
			{ env: { ...env, GIT_INDEX_FILE: index } },
		);
		// Then memory has its own committed file, and the user's staged change stays intact.
		expect(await readFile(index)).toEqual(before);
		const { stdout } = await run("git", ["show", "HEAD:MEMORY.md"], { cwd: memory, env });
		expect(stdout).toStartWith("# Memory\n");
	} finally {
		await rm(dir, { recursive: true, force: true });
	}
});
