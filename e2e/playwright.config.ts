import { defineConfig } from '@playwright/test';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const baseURL = process.env.LOTUS_E2E_BASE_URL ?? `http://localhost:${PORT}`;

// Fase 0 has only the health check and the sample page; the golden-path
// coverage here grows with every phase that adds real routes (auth,
// tenant app, etc.) instead of becoming a second test suite.
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  retries: process.env.CI ? 1 : 0,
  // A shared CI runner is several times slower than a laptop; the longest
  // specs (folders, uploads) ran out of the 30s default there.
  timeout: process.env.CI ? 60_000 : 30_000,
  reporter: process.env.CI ? 'dot' : 'list',
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  webServer: {
    // LOTUS_FEATURE_QA=1: the QA specs drive QA Tools, which is off by
    // default. Its off state is covered by internal/web/feature_qa_test.go;
    // one server cannot run both ways.
    command: `cd .. && make css && ADDR=:${PORT} LOTUS_DEV=1 LOTUS_FEATURE_QA=1 go run ./cmd/lotus serve`,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
