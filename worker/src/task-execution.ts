/* oxlint-disable no-await-in-loop -- Process freeze and identity checks must stay ordered. */
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { ExecutionError, err } from "@earendil-works/pi-durable/env";
import { withoutAbortSignal } from "@earendil-works/chord/context";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

type Identity = { pid: number; started: string } | { pid: number; command: string };
type ProcessState = { pid: number; started: string; state: string; parent: number };

const quote = (text: string) => `'${text.replaceAll("'", "'\\''")}'`;
const pause = () => new Promise<void>((resolve) => setTimeout(resolve, 10));

/** Query only recorded processes and the direct children of a stopped, owned parent. */
async function inspect(pid: number, command?: string): Promise<ProcessState | undefined> {
	const fields = `pid=,ppid=,stat=,lstart=${command === undefined ? "" : ",args="}`;
	const child = Bun.spawn(["/bin/ps", "-p", String(pid), "-o", fields], {
		stdout: "pipe",
		stderr: "pipe",
	});
	const [output, errors, code] = await Promise.all([
		new Response(child.stdout).text(),
		new Response(child.stderr).text(),
		child.exited,
	]);
	if (code === 1 && output.trim() === "" && errors.trim() === "") return undefined;
	if (code !== 0) throw new Error(`process identity query failed: ${code}`);
	const match = output
		.trim()
		.match(/^(\d+)\s+(\d+)\s+(\S+)\s+(\S+\s+\S+\s+\S+\s+\S+\s+\S+)(?:\s+([\s\S]*))?$/);
	if (match === null) throw new Error("invalid process identity response");
	// A pending exec can outlive child reaping. Verify ownership before using a raw PID.
	if (command !== undefined && (Number(match[2]) !== process.pid || !match[5]?.includes(`-c ${command}`)))
		throw new Error("command ownership could not be verified");
	return { pid: Number(match[1]), parent: Number(match[2]), state: match[3]!, started: match[4]! };
}

async function current(identity: Identity): Promise<ProcessState | undefined> {
	const state = await inspect(identity.pid, "command" in identity ? identity.command : undefined);
	return state !== undefined &&
		("command" in identity || state.started === identity.started) &&
		!state.state.startsWith("Z")
		? state
		: undefined;
}

async function signal(identity: Identity, value: NodeJS.Signals): Promise<void> {
	if ((await current(identity)) === undefined) return;
	try {
		process.kill(identity.pid, value);
	} catch (error) {
		if (!(error instanceof Error) || !("code" in error) || error.code !== "ESRCH") throw error;
	}
}

class CommandScope {
	readonly known = new Map<number, Identity>();
	readonly ready = Promise.withResolvers<Identity | undefined>();
	stopping: Promise<void> | undefined;
	finished = false;
	failure: unknown;
	registrationFailed = false;
	stopRequested = false;

	stop(): Promise<void> {
		this.stopRequested = true;
		return (this.stopping ??= this.#stop()
			.catch((error: unknown) => {
				this.failure = error;
			})
			.finally(() => {
				this.stopping = undefined;
			}));
	}

	async #stop(): Promise<void> {
		const root = await this.ready.promise;
		if (root !== undefined && !this.finished) this.known.set(root.pid, root);
		// A stopped parent cannot fork or reap. Discover its children only after observing T.
		// This retains foreground children in other process groups without killing those groups.
		const visited = new Set<number>();
		const visit = async (identity: Identity): Promise<void> => {
			if (visited.has(identity.pid)) return;
			visited.add(identity.pid);
			await signal(identity, "SIGSTOP");
			const deadline = Date.now() + 1000;
			let state;
			while ((state = await current(identity)) !== undefined && !state.state.includes("T")) {
				if (Date.now() >= deadline) throw new Error(`process ${identity.pid} did not stop`);
				await pause();
			}
			if (state === undefined) return;
			const child = Bun.spawn(["/usr/bin/pgrep", "-P", String(identity.pid)], {
				stdout: "pipe",
				stderr: "pipe",
			});
			const [output, errors, code] = await Promise.all([
				new Response(child.stdout).text(),
				new Response(child.stderr).text(),
				child.exited,
			]);
			if (code !== 0 && !(code === 1 && errors.trim() === "")) throw new Error(`child query failed: ${code}`);
			for (const pid of output.trim().split(/\s+/).filter(Boolean).map(Number)) {
				const descendant = await inspect(pid);
				if (descendant === undefined || descendant.parent !== identity.pid) continue;
				this.known.set(pid, descendant);
				await visit(descendant);
			}
		};
		this.failure = undefined;
		try {
			for (const identity of this.known.values()) await visit(identity);
		} catch (error) {
			// Keep the ancestry intact for retry if discovery could not complete.
			for (const identity of this.known.values()) {
				if ("started" in identity) await signal(identity, "SIGCONT");
			}
			throw error;
		}
		for (const identity of [...this.known.values()].toReversed()) await signal(identity, "SIGKILL");
		const deadline = Date.now() + 1000;
		while (this.known.size > 0) {
			for (const [pid, identity] of this.known) {
				if ((await current(identity)) === undefined) this.known.delete(pid);
			}
			if (this.known.size === 0) return;
			if (Date.now() >= deadline) throw new Error("controlled processes have not exited");
			await pause();
		}
	}
}

/** Foreground local commands only. Detached daemons and remote work remain outside this scope. */
export class TaskExecutionScope {
	readonly #commands = new Set<CommandScope>();
	readonly #pending = new Set<Promise<unknown>>();

	env(cwd: string): NodeExecutionEnv {
		const env = new NodeExecutionEnv({ cwd });
		const exec = env.exec.bind(env);
		env.exec = (...args) => {
			const command = new CommandScope();
			this.#commands.add(command);
			const pending = this.#exec(exec, command, ...args);
			this.#pending.add(pending);
			void pending.finally(() => this.#pending.delete(pending)).catch(() => {});
			return pending;
		};
		// Explicit env cleanup uses this owner. Harness shutdown calls scope.stop directly.
		env.cleanup = async () => {
			await this.stop();
		};
		return env;
	}

	async #exec(
		exec: NodeExecutionEnv["exec"],
		scope: CommandScope,
		command: Parameters<NodeExecutionEnv["exec"]>[0],
		options: Parameters<NodeExecutionEnv["exec"]>[1],
		context: Parameters<NodeExecutionEnv["exec"]>[2],
	): ReturnType<NodeExecutionEnv["exec"]> {
		if (
			(options?.timeout !== undefined &&
				(!Number.isFinite(options.timeout) ||
					options.timeout <= 0 ||
					options.timeout * 1000 > 2_147_483_647)) ||
			(Array.isArray(command) && command.length === 0)
		) {
			scope.ready.resolve(undefined);
			this.#commands.delete(scope);
			return exec(command, options, context);
		}
		if (context.abortSignal?.aborted) {
			scope.ready.resolve(undefined);
			this.#commands.delete(scope);
			return err(new ExecutionError("aborted", "aborted"));
		}
		let dir;
		try {
			dir = await mkdtemp(join(tmpdir(), "dex-exec-"));
		} catch (error) {
			scope.ready.resolve(undefined);
			this.#commands.delete(scope);
			return err(new ExecutionError("spawn_error", String(error)));
		}
		const pidPath = join(dir, "pid");
		let timedOut = false;
		const stop = () => {
			void scope.stop();
		};
		context.abortSignal?.addEventListener("abort", stop, { once: true });
		const timeout = options?.timeout;
		const timer =
			timeout !== undefined && Number.isFinite(timeout) && timeout > 0
				? setTimeout(() => {
						timedOut = true;
						stop();
					}, timeout * 1000)
				: undefined;
		const script = typeof command === "string" ? command : `exec ${command.map(quote).join(" ")}`;
		// The shell stops before user code. Register its identity before allowing it to fork.
		const gate = `printf '%s' "$$" > ${quote(pidPath)}; kill -STOP "$$"; `;
		const wrapped = `${gate}${script}`;
		let finished = false;
		let callbackError: unknown;
		const pending = exec(
			wrapped,
			{
				...options,
				timeout: undefined,
				onOutput: (text, outputContext, info) => {
					try {
						options?.onOutput?.(text, outputContext, info);
					} catch (error) {
						callbackError = error;
						stop();
					}
				},
			},
			withoutAbortSignal(context),
		);
		void pending
			.finally(() => {
				finished = true;
				scope.finished = true;
			})
			.catch(() => {});
		try {
			const deadline = Date.now() + 5000;
			// The dependency changes finished when its child exits.
			// oxlint-disable-next-line no-unmodified-loop-condition
			while (!finished) {
				let pid;
				try {
					pid = Number(await readFile(pidPath, "utf8"));
				} catch (error) {
					if (!(error instanceof Error) || !("code" in error) || error.code !== "ENOENT") throw error;
				}
				if (pid !== undefined && Number.isSafeInteger(pid) && pid > 0) {
					// Keep the unique wrapper for safe cleanup if the first identity query fails.
					scope.known.set(pid, { pid, command: gate });
					const root = await inspect(pid);
					if (root === undefined) throw new Error("command exited before identity registration");
					if (!root.state.includes("T")) {
						if (Date.now() >= deadline) throw new Error("command did not enter registration stop");
						await pause();
						continue;
					}
					if (!scope.stopRequested && context.abortSignal?.aborted !== true) await signal(root, "SIGCONT");
					scope.known.set(pid, root);
					// Stop must not take its child snapshot before the registration CONT completes.
					scope.ready.resolve(root);
					break;
				}
				if (Date.now() >= deadline) throw new Error("command identity registration timed out");
				await pause();
			}
			scope.ready.resolve(undefined);
			if (context.abortSignal?.aborted) stop();
			const result = await pending;
			scope.finished = true;
			if (scope.stopRequested) await scope.stop();
			else scope.known.clear(); // Normal foreground exit; detached daemons are outside the contract.
			if (scope.known.size === 0 && scope.failure === undefined) this.#commands.delete(scope);
			if (callbackError !== undefined)
				return err(
					new ExecutionError(
						"callback_error",
						callbackError instanceof Error ? callbackError.message : "output callback failed",
					),
				);
			if (!scope.stopRequested) return result;
			const failure = new ExecutionError(
				timedOut ? "timeout" : "aborted",
				timedOut ? `timeout:${timeout}` : "controlled command stopped",
			);
			const spillPath = result.ok ? result.value.spillPath : result.error.spillPath;
			if (spillPath !== undefined) failure.spillPath = spillPath;
			return err(failure);
		} catch (error) {
			scope.failure = error;
			scope.registrationFailed = true;
			scope.ready.resolve(undefined);
			const hadProcess = scope.known.size > 0;
			await scope.stop();
			if (hadProcess && scope.known.size === 0 && scope.failure === undefined) await pending;
			return err(new ExecutionError("spawn_error", String(error)));
		} finally {
			clearTimeout(timer);
			context.abortSignal?.removeEventListener("abort", stop);
			scope.ready.resolve(undefined);
			await rm(dir, { recursive: true, force: true });
		}
	}

	async stop(): Promise<void> {
		await Promise.all([...this.#commands].map((command) => command.stop()));
	}

	async join(): Promise<boolean> {
		for (const command of this.#commands) {
			if (command.failure !== undefined || command.stopping !== undefined) await command.stop();
			// A failed registration never releases user code. Its direct exit is sufficient.
			if (
				(command.registrationFailed && !command.finished) ||
				command.failure !== undefined ||
				command.known.size > 0
			)
				return false;
		}
		return this.#pending.size === 0;
	}
}
