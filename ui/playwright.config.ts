import {defineConfig, devices} from '@playwright/test';

// The end-to-end checks run the built app (npm run build first) from the real daemon against
// a fake openccu-lite (internal/devserver: go test serves both), so the whole chain is covered:
// the page, the API, the pipeline, a webhook receiver. PORT lets two checkouts run side by side.
const PORT = Number(process.env.PORT || 8795);

export default defineConfig({
    testDir: 'e2e',
    timeout: 30_000,
    fullyParallel: false,
    workers: 1,
    retries: process.env.CI ? 1 : 0,
    reporter: 'list',
    use: {baseURL: `http://127.0.0.1:${PORT}/addons/forwarder/`, trace: 'off'},
    webServer: {
        command: `cd .. && FORWARDER_DEV_PORT=${PORT} go test -count=1 -timeout 0 -run TestServe ./internal/devserver`,
        url: `http://127.0.0.1:${PORT}/addons/forwarder/api/v1/health`,
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
    },
    projects: [
        {name: 'desktop-light', use: {...devices['Desktop Chrome'], colorScheme: 'light'}},
        {name: 'desktop-dark', use: {...devices['Desktop Chrome'], colorScheme: 'dark'}},
    ],
});
