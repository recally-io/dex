/* oxlint-disable no-await-in-loop -- Poll real process state with a deadline. */
/* oxlint-disable typescript/no-unsafe-type-assertion -- Preserve Bun spawn overloads around process scheduling and fault hooks. */
import { expect, test } from "bun:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { TaskExecutionScope } from "../src/task-execution";

const realSpawn = Bun.spawn;
const realKill = process.kill.bind(process);

function state(pid: number): string {
	return new TextDecoder().decode(Bun.spawnSync(["/bin/ps", "-p", String(pid), "-o", "stat="]).stdout).trim();
}

async function poll(check: () => boolean | Promise<boolean>, label: string): Promise<void> {
	const deadline = Date.now() + 4000;
	while (!(await check())) {
		if (Date.now() >= deadline) throw new Error(`Timed out: ${label}`);
		await Bun.sleep(10);
	}
}

function kill(pid: number | undefined): void {
	if (pid === undefined) return;
	try {
		realKill(pid, "SIGKILL");
	} catch (error) {
		if (!(error instanceof Error) || !("code" in error) || error.code !== "ESRCH") throw error;
	}
}

// Hold the CONT query. On the old path, deliver the queued CONT before root KILL.
// All process queries, commands, and delivered signals use real subprocesses.
test("user cancellation during registration stops the foreground child before confirmation", async () => {
	// Given a paused registration query and a foreground child in another process group.
	const dir = await mkdtemp(join(tmpdir(), "dex-runtime-race-"));
	const scope = new TaskExecutionScope();
	const blocked = Promise.withResolvers<void>();
	const release = Promise.withResolvers<void>();
	let queries = 0;
	let root: number | undefined;
	let foreground: number | undefined;
	let resumed = false;
	let stopping: Promise<void> | undefined;
	Bun.spawn = ((...args: Parameters<typeof Bun.spawn>) => {
		const argv = args[0];
		const child = realSpawn(...args);
		if (Array.isArray(argv) && argv[0] === "/bin/ps" && argv[1] === "-p") {
			queries++;
			root ??= Number(argv[2]);
			if (queries === 2) {
				const exited = child.exited;
				Object.defineProperty(child, "exited", {
					value: exited.then(async (code) => {
						blocked.resolve();
						await release.promise;
						return code;
					}),
				});
			}
		}
		return child;
	}) as typeof Bun.spawn;
	process.kill = (pid, signal) => {
		if (pid === root && (signal === "SIGCONT" || signal === "SIGKILL") && !resumed) {
			resumed = true;
			realKill(pid, "SIGCONT");
			// Wait for a real child before allowing registration/stop to continue.
			const deadline = Date.now() + 4000;
			while (!Bun.spawnSync(["/bin/test", "-s", join(dir, "foreground.pid")]).success) {
				if (Date.now() >= deadline) throw new Error("Foreground child did not start");
				Bun.sleepSync(5);
			}
			return signal === "SIGKILL" ? realKill(pid, signal) : true;
		}
		return realKill(pid, signal);
	};
	const pending = scope
		.env(dir)
		.exec(
			"set -m; bash -c 'echo $$ > foreground.pid; while [ ! -f release ]; do sleep 0.02; done; echo LATE > late'; echo done",
			{},
			BACKGROUND_CONTEXT,
		);
	try {
		await blocked.promise;
		// When cancellation starts before the delayed registration CONT query returns.
		stopping = scope.stop();
		// Dispatch the ready continuation. The old path starts its real STOP query;
		// the fixed path still waits for registration and cannot query the root yet.
		await Promise.resolve();
		if (queries > 2) await stopping;
		release.resolve();
		await stopping;
		await pending;
		foreground = Number(await readFile(join(dir, "foreground.pid"), "utf8"));
		await writeFile(join(dir, "release"), "release after stop");
		await poll(
			() => state(foreground!) === "" || Bun.file(join(dir, "late")).exists(),
			"child exit or late write",
		);
		console.log(
			JSON.stringify({
				case: "registration cancellation",
				root,
				foreground,
				state: state(foreground),
				lateWrite: await Bun.file(join(dir, "late")).exists(),
				joined: await scope.join(),
			}),
		);
		// Then confirmation means that the real child exited and cannot write later.
		expect(await scope.join()).toBe(true);
		expect(await Bun.file(join(dir, "late")).exists()).toBe(false);
		expect(state(foreground)).toBe("");
	} finally {
		release.resolve();
		Bun.spawn = realSpawn;
		process.kill = realKill;
		foreground ??= Number(await readFile(join(dir, "foreground.pid"), "utf8").catch(() => "0")) || undefined;
		kill(foreground);
		kill(root);
		await pending;
		await stopping;
		await poll(
			() =>
				(root === undefined || state(root) === "") && (foreground === undefined || state(foreground) === ""),
			"race probe cleanup",
		);
		await rm(dir, { recursive: true, force: true });
		console.log(
			JSON.stringify({
				cleanup: "registration cancellation",
				rootState: root === undefined ? "not started" : state(root),
				foregroundState: foreground === undefined ? "not started" : state(foreground),
				workspaceRemoved: !(await readdir(tmpdir())).includes(basename(dir)),
			}),
		);
	}
}, 15000);

for (const retry of [false, true])
	test(`user command registration failure cleans the paused shell${retry ? " after query recovery" : ""}`, async () => {
		// Given a real shell whose PID file was read, fail only its first identity query.
		const dir = await mkdtemp(join(tmpdir(), "dex-runtime-registration-"));
		const scope = new TaskExecutionScope();
		let root: number | undefined;
		let failQueries = true;
		Bun.spawn = ((...args: Parameters<typeof Bun.spawn>) => {
			const argv = args[0];
			if (failQueries && Array.isArray(argv) && argv[0] === "/bin/ps" && argv[1] === "-p") {
				root ??= Number(argv[2]);
				if (!retry) failQueries = false;
				return realSpawn(["/bin/sh", "-c", "exit 2"], args[1]);
			}
			return realSpawn(...args);
		}) as typeof Bun.spawn;
		try {
			// When registration fails, cleanup must not release user code.
			const result = await scope.env(dir).exec("echo USER_CODE > late", {}, BACKGROUND_CONTEXT);
			expect(result.ok).toBe(false);
			if (!result.ok) expect(result.error.message).toContain("process identity query failed: 2");
			console.log(
				JSON.stringify({ case: "identity failure", root, state: state(root!), joined: await scope.join() }),
			);
			// A query outage keeps confirmation false and retains the candidate for retry.
			if (retry) {
				expect(await scope.join()).toBe(false);
				expect(state(root!)).toContain("T");
				expect(await Bun.file(join(dir, "late")).exists()).toBe(false);
				failQueries = false;
			} else expect(state(root!)).toBe("");
			await scope.stop();
			await poll(() => scope.join(), "scope cleanup confirmation");
			expect(state(root!)).toBe("");
			expect(await Bun.file(join(dir, "late")).exists()).toBe(false);
		} finally {
			Bun.spawn = realSpawn;
			kill(root);
			await poll(() => root === undefined || state(root) === "", "probe shell cleanup");
			await rm(dir, { recursive: true, force: true });
			console.log(
				JSON.stringify({
					cleanup: "identity failure",
					rootState: root === undefined ? "not started" : state(root),
					workspaceRemoved: !(await readdir(tmpdir())).includes(basename(dir)),
				}),
			);
		}
	});
