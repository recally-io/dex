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
            "--machine-listen",
            "0.0.0.0:0",
            "--data-dir",
            "web/.e2e/data",
          ],
          cwd: "..",
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
