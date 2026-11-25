import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "../api/openapi.yaml",
  output: "src/api/generated",
  plugins: [
    "@hey-api/client-fetch",
    {
      name: "@tanstack/react-query",
      $hooks: {
        operations: {
          // The Board cursor belongs to EventSource, not to an infinite query.
          isQuery: (operation) => (operation.path === "/api/board/events" ? false : undefined),
        },
      },
    },
  ],
});
