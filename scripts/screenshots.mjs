#!/usr/bin/env node
// Capture the README screenshots of the dashboard into docs/images.
//
// Builds and starts scripts/demo in a temporary directory: generated history and
// a fake Bluetooth backend, so it never touches a treadmill or your data. Uses
// the system Chrome/Chromium via playwright-core.
//
//   cd scripts && npm install && npm run screenshots
//
//   CHROMIUM_PATH   browser binary (default: first one found below)
//
// Pass file names to capture only those: npm run screenshots -- goals.png
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const outDir = join(root, 'docs', 'images');

// Captures at 2x for sharp images on high-DPI screens. A shot is the viewport;
// without `height` the viewport grows to the whole page, so the sidebar
// (one viewport tall) still reaches the bottom.
const shots = [
    // Cards and charts, cut just below them; demo history fills one month best.
    { file: 'dashboard.png', path: '/?period=1m', width: 1280, height: 828 },
    { file: 'goals.png', path: '/goals.html', width: 1280 },
    { file: 'dashboard-mobile.png', path: '/?period=1m', width: 390, height: 844 },
];

const hideCss = `* { transition: none !important; animation: none !important; caret-color: transparent !important; }`;

const browserCandidates = [
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/Applications/Chromium.app/Contents/MacOS/Chromium',
];

function findBrowser() {
    const path = process.env.CHROMIUM_PATH ?? browserCandidates.find((candidate) => existsSync(candidate));
    if (!path || !existsSync(path)) {
        throw new Error('No Chrome or Chromium found. Set CHROMIUM_PATH to the browser binary.');
    }
    return path;
}

function freePort() {
    return new Promise((resolve, reject) => {
        const server = createServer().once('error', reject);
        server.listen(0, '127.0.0.1', () => {
            const { port } = server.address();
            server.close(() => resolve(port));
        });
    });
}

// Ready once the agent reports its fake link, so the sidebar shows "Connected".
async function waitForDemo(baseUrl, demo) {
    for (let i = 0; i < 100 && demo.exitCode === null; i++) {
        const live = await fetch(`${baseUrl}/api/live`).then((r) => r.json(), () => null);
        if (live?.snapshot?.connection === 'connected') {
            return;
        }
        await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error(`Demo at ${baseUrl} did not come up; see its log above.`);
}

async function settle(page) {
    await page.waitForSelector('body:not(.loading)');
    await page.waitForSelector('.device-card[data-link="connected"]');
    await page.waitForLoadState('networkidle');
    await page.addStyleTag({ content: hideCss });
    await page.evaluate(() => document.fonts.ready);
}

// Unix socket paths are short, so keep the directory directly under tmpdir.
const dir = mkdtempSync(join(tmpdir(), 'tread-shots-'));
let demo;
let browser;
try {
    const demoBin = join(dir, 'demo');
    execFileSync('go', ['build', '-o', demoBin, './scripts/demo'], { cwd: root, stdio: 'inherit' });
    const listen = `127.0.0.1:${await freePort()}`;
    const baseUrl = `http://${listen}`;
    demo = spawn(demoBin, ['-dir', dir, '-listen', listen], { stdio: ['ignore', 'inherit', 'inherit'] });
    await waitForDemo(baseUrl, demo);

    mkdirSync(outDir, { recursive: true });
    browser = await chromium.launch({ executablePath: findBrowser() });
    const page = await browser.newPage({ deviceScaleFactor: 2 });
    const only = process.argv.slice(2);
    for (const shot of shots.filter((s) => only.length === 0 || only.includes(s.file))) {
        await page.setViewportSize({ width: shot.width, height: shot.height ?? 800 });
        await page.goto(baseUrl + shot.path);
        await settle(page);
        if (!shot.height) {
            const height = await page.evaluate(() => document.documentElement.scrollHeight);
            await page.setViewportSize({ width: shot.width, height });
        }
        await page.mouse.move(0, 0);

        const path = join(outDir, shot.file);
        await page.screenshot({ path });
        console.log(`wrote ${path}`);
    }
} finally {
    await browser?.close();
    if (demo?.exitCode === null) {
        demo.kill();
        await once(demo, 'exit');
    }
    rmSync(dir, { recursive: true, force: true });
}
