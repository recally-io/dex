import { defineConfig } from "vite-plus";

// The worker runs on Bun and tests with `bun test`; Vite+ only lints and formats it.
export default defineConfig({
	lint: {
		ignorePatterns: ["node_modules/**"],
		categories: { correctness: "error", suspicious: "error", perf: "error" },
		options: { typeAware: true, typeCheck: true },
	},
	fmt: {
		useTabs: true,
		printWidth: 110,
	},
});
