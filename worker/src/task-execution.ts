import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";

/** Local foreground exec calls only. Detached descendants are outside this contract. */
export class TaskExecutionScope {
	readonly #pending = new Set<Promise<unknown>>();
	#failed = false;

	env(cwd: string): NodeExecutionEnv {
		const env = new NodeExecutionEnv({ cwd });
		const exec = env.exec.bind(env);
		env.exec = async (...args: Parameters<NodeExecutionEnv["exec"]>) => {
			const pending = exec(...args);
			this.#pending.add(pending);
			try {
				// NodeExecutionEnv waits for the child to exit, including on cancellation.
				return await pending;
			} catch (error) {
				this.#failed = true;
				throw error;
			} finally {
				this.#pending.delete(pending);
			}
		};
		return env;
	}

	async join(): Promise<boolean> {
		await Promise.allSettled(this.#pending);
		return !this.#failed && this.#pending.size === 0;
	}
}
