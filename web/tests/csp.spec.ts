import { expect, test } from '@playwright/test';

import { loginOrSetup } from './fixtures/uiState';

// The UI must render under the server's CSP with zero violations.
test('csp: every main screen renders without CSP violations', async ({ page }) => {
  const base = process.env.SMOKE_BASE_URL!;
  const problems: string[] = [];
  page.on('console', (m) => {
    if (m.text().includes('Content-Security-Policy')) problems.push(`console: ${m.text()}`);
  });
  await page.addInitScript(() => {
    (window as unknown as { __csp: string[] }).__csp = [];
    document.addEventListener('securitypolicyviolation', (e) => {
      (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });

  // Fresh server: set up; otherwise reuse the shared session (the login limiter is 5 a minute).
  await loginOrSetup(page, base);

  const visit = async (hash: string, heading: string) => {
    await page.goto(base + '/' + hash);
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
    await page.waitForTimeout(300);
    const v = await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp);
    problems.push(...v.map((x) => `violation@${hash}: ${x}`));
  };
  await visit('#/', 'Slots');
  await visit('#/endpoints', 'Endpoints');
  await visit('#/settings', 'Settings');
  await visit('#/slots/1', 'Edit slot 1');

  expect(problems).toEqual([]);
});
