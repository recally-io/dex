import type { E2EConfig } from "e2e";
import { web } from "@e2e-dev/web";

export default {
  targets: [
    {
      engine: web(),
      app: {
        url: process.env.APP_URL ?? "http://127.0.0.1:0",
        command: {
          executable: "go",
          args: [
            "run",
            "-tags",
            "dex_e2e",
            "./cmd/dex",
            "serve",
            "--host",
            "127.0.0.1",
            "--port",
            "{port}",
            "--data-dir",
            process.env.DEX_E2E_DATA_DIR || "apps/web/.e2e/data",
          ],
          cwd: "../..",
          env: {
            DEX_FAKE_MODEL: "1",
          },
          startupTimeout: 60000,
          log: ".e2e/logs/app.log",
        },
      },
    },
  ],
} satisfies E2EConfig;
