/* oxlint-disable no-await-in-loop -- Keep each move and its checks in one sequence. */
import { lstat, mkdir, readdir, readlink } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";

/** Leave links to files outside the moved tree in place: their meaning would change. */
async function changesRelativeLink(path: string, root: string): Promise<boolean> {
	const entry = await lstat(path);
	if (entry.isSymbolicLink()) {
		const target = await readlink(path);
		if (isAbsolute(target)) return false;
		const rel = relative(root, resolve(dirname(path), target));
		return path === root || rel === ".." || rel.startsWith("../");
	}
	if (!entry.isDirectory()) return false;
	for (const name of await readdir(path)) {
		if (await changesRelativeLink(join(path, name), root)) return true;
	}
	return false;
}

/** Each move can complete independently. Existing targets always win. */
export async function upgradeExtensions(agentDir: string): Promise<string[]> {
	const warnings: string[] = [];
	for (const name of ["skills", "mcp.json"]) {
		const old = join(agentDir, name);
		const target = join(agentDir, ".agents", name);
		try {
			await lstat(old);
			const exists = await lstat(target).catch((error: unknown) => {
				if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT")
					return undefined;
				throw error;
			});
			if (exists !== undefined) {
				warnings.push(`${old}: both old and new entries exist; using ${target}`);
				continue;
			}
			if (name === "skills" && (await changesRelativeLink(old, old))) {
				warnings.push(`${old}: relative symlink would change its target; move the tree by hand`);
				continue;
			}
			await mkdir(dirname(target), { recursive: true });
			// Node rename can replace a target. POSIX mv -n keeps a target created during the scan.
			const move = Bun.spawn(["/bin/mv", "-n", old, dirname(target)], { stdout: "pipe", stderr: "pipe" });
			if ((await move.exited) !== 0) throw new Error("move failed");
			if (
				await lstat(old).catch((error: unknown) => {
					if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT")
						return undefined;
					throw error;
				})
			)
				warnings.push(`${old}: both old and new entries exist; using ${target}`);
		} catch (error) {
			if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") continue;
			warnings.push(`${old}: upgrade failed; check file access and move the entry by hand`);
		}
	}
	return warnings;
}
