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
  reporter: process.env.CI ? 'dot' : 'list',
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  webServer: {
    command: `cd .. && make css && ADDR=:${PORT} LOTUS_DEV=1 go run ./cmd/lotus serve`,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
