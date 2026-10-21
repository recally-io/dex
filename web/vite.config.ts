import { defineConfig, type PluginOption } from "vite-plus";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  // SAFETY: react and tailwindcss each return a plugin array, which the config flattens.
  // vite-plus 1.3.0 types the nested array too deeply for TypeScript 6 to compare.
  plugins: [react(), tailwindcss()] as PluginOption[],
  // The registry uses cn; the project supplies it with clsx and tailwind-merge.
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      cn: fileURLToPath(new URL("./src/lib/utils.ts", import.meta.url)),
    },
  },
  server: { host: "127.0.0.1", proxy: { "/api": process.env.DEX_API_URL ?? "http://127.0.0.1:8787" } },
  lint: {
    ignorePatterns: [
      "dist/**",
      "src/api/generated/**",
      "src/components/atoms/**",
      "src/components/primitives/**",
      "tools/oxlint/anti-slop/**",
    ],
    categories: { correctness: "error", suspicious: "error", perf: "error" },
    plugins: ["react", "typescript"],
    jsPlugins: [{ name: "anti-slop", specifier: "./tools/oxlint/anti-slop/index.ts" }],
    rules: {
      "oxc/no-accumulating-spread": "error",
      "anti-slop/no-array-filter-map": "error",
      "anti-slop/no-reduce-accumulator-copy": "error",
      "anti-slop/no-chained-type-assertions": "error",
      "anti-slop/no-conditional-empty-object-spread": "error",
      "anti-slop/no-known-value-widening": "error",
      "anti-slop/no-module-mocking": "error",
      "anti-slop/no-object-parameters": "error",
      "anti-slop/no-reflect-apply": "error",
      "anti-slop/no-reflect-get": "error",
      "anti-slop/no-runtime-typeof": "error",
      "anti-slop/no-shape-in-symbol-names": "error",
      "anti-slop/no-unknown-parameters": "error",
      "anti-slop/no-unknown-returns": "error",
      "anti-slop/no-unknown-type-aliases": "error",
      "anti-slop/no-unsafe-dictionary-type": "error",
      "anti-slop/no-widen-then-assert": "error",
      "anti-slop/require-readable-spacing": "error",
      "anti-slop/require-safety-comment-for-type-assertion": "error",

      "react/rules-of-hooks": "error",
      "react/react-in-jsx-scope": "off",
    },
  },
  fmt: {
    ignorePatterns: [
      "dist/**",
      "src/api/generated/**",
      "src/components/atoms/**",
      "src/components/primitives/**",
      "tools/oxlint/anti-slop/**",
      "bun.lock",
    ],
    printWidth: 110,
  },
  test: { include: ["src/**/*.test.ts"] },
});
