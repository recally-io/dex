import { join } from "node:path";

/** Agent storage and workspace share this root. */
export function agentHome(dataDir: string, id: string): string {
	return join(dataDir, "agents", id);
}
