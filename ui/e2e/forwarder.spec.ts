import {expect, test, type Page} from '@playwright/test';

const DEV = 'http://127.0.0.1:' + (process.env.PORT || 8795);

async function hookBodies(page: Page): Promise<unknown[][]> {
    const res = await page.request.get(`${DEV}/dev/hook/bodies`);
    const raw = (await res.json()) as unknown[] | null;
    return (raw ?? []) as unknown[][];
}

test.describe.configure({mode: 'serial'});

test.beforeAll(async ({request}) => {
    await request.get(`${DEV}/dev/reset`);
});

test('the page loads with the system connected', async ({page}) => {
    await page.goto('./');
    await expect(page.getByRole('heading', {name: 'Forwarder'})).toBeVisible();
    await expect(page.getByText(/datapoints · \d+ events|Datenpunkte · \d+ Ereignisse/)).toBeVisible();
    await expect(page.locator('.fw-empty')).toBeVisible();
});

test('a selection by room shows a live count', async ({page}) => {
    await page.goto('./');
    await page.getByRole('tab', {name: /Selections|Auswahlen/}).click();
    await page.getByRole('button', {name: /Add selection|Auswahl hinzufügen/}).click();
    const dlg = page.getByTestId('selection-editor');
    await expect(dlg).toBeVisible();
    await dlg.getByLabel('Name').fill('Bad');
    // everything first
    await expect(dlg.locator('.fw-match-count')).toContainText('14');
    // the room rule narrows it to the thermostat's channel
    await dlg.getByRole('button', {name: 'Bad', exact: true}).click();
    await expect(dlg.locator('.fw-match-count b').first()).toHaveText('5');
    // exclude the RSSI and the boost
    const exclude = dlg.locator('.fw-row', {hasText: /Exclude datapoints|Datenpunkte ausschließen/}).locator('input[type=text]');
    await exclude.fill('BOOST_MODE');
    await exclude.press('Enter');
    await expect(dlg.locator('.fw-match-count b').first()).toHaveText('4');
    // the devices tab: tick the key device, which adds its channels
    await dlg.getByRole('tab', {name: /Devices and channels|Geräte und Kanäle/}).click();
    await expect(dlg.locator('.fw-tree-row')).toHaveCount(2);
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    await expect(page.locator('.fw-card', {hasText: 'Bad'})).toContainText(/Rooms: Bad|Räume: Bad/);
});

test('a webhook target is created, tested, previewed and receives events', async ({page}) => {
    await page.goto('./');
    await page.getByRole('combobox', {name: /Target type|Zieltyp/}).selectOption('webhook');
    await page.getByRole('button', {name: /Add target|Ziel hinzufügen/}).click();
    const dlg = page.getByTestId('target-editor');
    await expect(dlg).toBeVisible();
    await dlg.getByLabel('Name', {exact: true}).fill('Hook');
    await dlg.getByLabel(/^Selection$|^Auswahl$/).selectOption({index: 1});
    await dlg.getByLabel('URL', {exact: true}).fill(`${DEV}/dev/hook`);
    // the live preview of the URL template renders
    await expect(dlg.locator('.fw-tpl-out').first()).toContainText('/dev/hook');
    // the body tab: the default body renders JSON of a recent record
    await dlg.getByRole('tab', {name: /Body|Inhalt/}).click();
    await expect(dlg.locator('.fw-tpl-out').first()).toContainText('"kind":"event"');
    // test and preview
    await dlg.getByRole('button', {name: /^Test|^Testen/}).click();
    await expect(dlg.locator('.fw-test-result')).toContainText('HTTP 204');
    await dlg.getByRole('button', {name: /Preview|Vorschau/}).click();
    await expect(dlg.locator('.fw-preview')).toContainText('POST');
    // a small batch interval so the test sees the records quickly
    await dlg.getByRole('tab', {name: /Delivery|Zustellung/}).click();
    await dlg.getByLabel(/Interval|Intervall/).fill('300');
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    const card = page.locator('.fw-card', {hasText: 'Hook'});
    await expect(card).toBeVisible();
    // push two events: one in the room, one not
    await page.request.get(`${DEV}/dev/push?address=000A1BE9A1B2C3:1&key=ACTUAL_TEMPERATURE&value=22.5`);
    await page.request.get(`${DEV}/dev/push?interface=BidCos-RF&address=MEQ0123456:1&key=PRESS_SHORT&value=true`);
    // the Test above sent one record already; wait for the event itself
    await expect.poll(async () => JSON.stringify(await hookBodies(page)), {timeout: 10_000}).toContain('ACTUAL_TEMPERATURE');
    const recs = (await hookBodies(page)).flat() as {key: string; channel_name: string; rooms: string[]; value: unknown}[];
    const temp = recs.find((r) => r.key === 'ACTUAL_TEMPERATURE');
    expect(temp).toBeTruthy();
    expect(temp?.channel_name).toBe('Heizung Bad');
    expect(temp?.rooms).toEqual(['Bad']);
    expect(recs.find((r) => r.key === 'PRESS_SHORT')).toBeFalsy();
    // the card shows the state and the count; the pill keeps its width across states
    await expect(card.locator('.fw-pill').first()).toHaveText(/ok|verbunden/);
    const box = await card.locator('.fw-pill').first().boundingBox();
    expect(box?.width).toBeGreaterThan(60);
    await expect(card.locator('.fw-stat').first()).toContainText(/[1-9]/);
    // the records page lists the event with its target
    await page.getByRole('tab', {name: /Records|Datensätze/}).click();
    await expect(page.locator('.fw-table')).toContainText('ACTUAL_TEMPERATURE');
    await expect(page.locator('.fw-table tr', {hasText: 'ACTUAL_TEMPERATURE'}).first()).toContainText('Hook');
});

test('an unreachable target shows retrying and recovers after an edit', async ({page}) => {
    await page.goto('./');
    const card = page.locator('.fw-card', {hasText: 'Hook'});
    await card.getByRole('button', {name: /Edit|Bearbeiten/}).click();
    const dlg = page.getByTestId('target-editor');
    await dlg.getByLabel('URL', {exact: true}).fill(`${DEV}/dev/hook/fail`);
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    await page.request.get(`${DEV}/dev/push?address=000A1BE9A1B2C3:1&key=ACTUAL_TEMPERATURE&value=23`);
    await expect(card.locator('.fw-pill').first()).toHaveText(/retrying|wiederholt/, {timeout: 10_000});
    await expect(card.locator('.fw-card-error')).toContainText('503');
    const before = (await hookBodies(page)).flat().length;
    await card.getByRole('button', {name: /Edit|Bearbeiten/}).click();
    await dlg.getByLabel('URL', {exact: true}).fill(`${DEV}/dev/hook`);
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(card.locator('.fw-pill').first()).toHaveText(/ok|verbunden/, {timeout: 10_000});
    // the queued record was kept across the edit and delivered
    await expect.poll(async () => (await hookBodies(page)).flat().length, {timeout: 10_000}).toBeGreaterThan(before);
});

test('an InfluxDB target with the clean preset previews line protocol; the secret is write-only', async ({page}) => {
    await page.goto('./');
    await page.getByRole('combobox', {name: /Target type|Zieltyp/}).selectOption('influx');
    await page.getByRole('button', {name: /Add target|Ziel hinzufügen/}).click();
    const dlg = page.getByTestId('target-editor');
    await dlg.getByLabel('Name', {exact: true}).fill('Influx');
    await dlg.getByLabel('URL', {exact: true}).fill('http://127.0.0.1:9');
    await dlg.getByLabel(/Organisation/).fill('home');
    await dlg.getByLabel(/Bucket/).fill('hm');
    await dlg.getByLabel(/Token/).fill('s3cret-token');
    await dlg.getByRole('tab', {name: /Schema/}).click();
    await expect(dlg.getByLabel(/Measurement/)).toHaveValue('{{ key }}');
    await expect(dlg.locator('.fw-tpl-out').first()).toContainText(/ACTUAL_TEMPERATURE|LOW_BAT|PRESS_SHORT/);
    await dlg.getByRole('button', {name: /Preview|Vorschau/}).click();
    await expect(dlg.locator('.fw-preview')).toContainText(/,device=000A1BE9A1B2C3,channel=1,.*value=2[0-9](\.5)?,value_num=/);
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    const card = page.locator('.fw-card', {hasText: 'Influx'});
    await expect(card).toBeVisible();
    // the secret never comes back
    const res = await page.request.get('api/v1/targets');
    expect(await res.text()).not.toContain('s3cret-token');
    await card.getByRole('button', {name: /Edit|Bearbeiten/}).click();
    await expect(dlg.getByLabel(/Token/)).toHaveAttribute('placeholder', /set|gesetzt/);
    await dlg.getByRole('button', {name: /Cancel|Abbrechen/}).click();
    // disable and delete through the kit's dialog, never window.confirm
    await card.getByRole('button', {name: /Disable|Deaktivieren/}).click();
    await expect(card.locator('.fw-pill').first()).toHaveText(/disabled|inaktiv/);
    await card.getByRole('button', {name: /Delete|Löschen/}).click();
    const confirm = page.getByTestId('confirm');
    await expect(confirm).toBeVisible();
    await confirm.getByRole('button', {name: /Delete|Löschen/}).click();
    await expect(card).toBeHidden();
    await page.screenshot({path: `test-results/targets-${test.info().project.name}.png`, fullPage: true});
    await page.getByRole('tab', {name: /Selections|Auswahlen/}).click();
    await page.locator('.fw-card').first().getByRole('button', {name: /Edit|Bearbeiten/}).click();
    await expect(page.getByTestId('selection-editor')).toBeVisible();
    await page.screenshot({path: `test-results/selection-${test.info().project.name}.png`});
});

test('a selection of metric groups, a Prometheus scrape target and an Elasticsearch target', async ({page}) => {
    await page.goto('./');
    // a selection of two metric groups, ticked in the editor's checklist
    await page.getByRole('tab', {name: /Selections|Auswahlen/}).click();
    await page.getByRole('button', {name: /Add selection|Auswahl hinzufügen/}).click();
    const sdlg = page.getByTestId('selection-editor');
    await sdlg.getByLabel('Name').fill('Metriken');
    await sdlg.getByRole('checkbox', {name: /^\s*(Homematic events|Homematic-Ereignisse)\s*$/}).uncheck();
    await sdlg.getByRole('checkbox', {name: /^\s*(System metrics|Systemmetriken)\s*$/}).check();
    await expect(sdlg.locator('.fw-groups-head')).toHaveCount(2);
    await sdlg.getByRole('checkbox', {name: /^CPU and load|^CPU und Last/}).check();
    await sdlg.getByRole('checkbox', {name: /^Service messages|^Servicemeldungen/}).check();
    await sdlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(sdlg).toBeHidden();
    await expect(page.locator('.fw-card', {hasText: 'Metriken'})).toContainText(/cpu/);

    // a Prometheus scrape target over that selection
    await page.getByRole('tab', {name: /Targets|Ziele/}).click();
    await page.getByRole('combobox', {name: /Target type|Zieltyp/}).selectOption('prometheus');
    await page.getByRole('button', {name: /Add target|Ziel hinzufügen/}).click();
    const dlg = page.getByTestId('target-editor');
    await dlg.getByLabel('Name', {exact: true}).fill('Prom');
    await dlg.getByLabel(/^Selection$|^Auswahl$/).selectOption({label: 'Metriken (0)'});
    await expect(dlg.getByText(/shown after saving|nach dem Speichern/)).toBeVisible();
    await dlg.getByRole('tab', {name: /^Metrics$|^Metriken$/}).click();
    await expect(dlg.getByLabel(/Metric name|Metrikname/)).toHaveValue(/homematic_/);
    await dlg.getByRole('button', {name: /^Test|^Testen/}).click();
    await expect(dlg.locator('.fw-test-result')).toContainText('homematic_forwarder_test');
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    const card = page.locator('.fw-card', {hasText: 'Prom'});
    await expect(card).toContainText('Prometheus scrape');
    // the endpoint answers the metrics (the dev server has no session gate; the real one does)
    const targets = (await (await page.request.get('api/v1/targets')).json()) as {id: string; name: string}[];
    const id = targets.find((t) => t.name === 'Prom')?.id ?? '';
    expect(id).not.toBe('');
    await expect
        .poll(async () => (await page.request.get(`metrics/${id}`)).text(), {timeout: 15_000})
        .toMatch(/cpu_count\{host="[^"]+"\} [0-9]+/);
    const text = await (await page.request.get(`metrics/${id}`)).text();
    expect(text).toContain('service_messages_count{host=');
    expect(text).toContain('type="LOW_BAT"} 1');
    expect(text).toContain('forwarder_target_sent_total{target="Prom"');
    expect((await page.request.get('metrics/nosuch')).status()).toBe(404);
    // the editor shows the endpoint's URL after saving
    await card.getByRole('button', {name: /Edit|Bearbeiten/}).click();
    await expect(dlg.locator('.fw-scrape-url')).toContainText(`metrics/${id}`);
    await dlg.getByRole('button', {name: /Cancel|Abbrechen/}).click();
    // the system page shows the poll
    await page.getByRole('tab', {name: /^System$/}).click();
    await expect(page.locator('.fw-card', {hasText: /Metrics|Metriken/}).first()).toContainText(/cpu, service_messages|service_messages/);

    // an Elasticsearch target against the dev receiver: preset documents, Test indexes one
    await page.getByRole('tab', {name: /Targets|Ziele/}).click();
    await page.getByRole('combobox', {name: /Target type|Zieltyp/}).selectOption('elastic');
    await page.getByRole('button', {name: /Add target|Ziel hinzufügen/}).click();
    await dlg.getByLabel('Name', {exact: true}).fill('Elastic');
    await dlg.getByLabel(/^Selection$|^Auswahl$/).selectOption('');
    await dlg.getByLabel('URL', {exact: true}).fill(`${DEV}/dev/es`);
    await dlg.getByRole('tab', {name: /Documents|Dokumente/}).click();
    await expect(dlg.getByLabel(/^Index$/)).toHaveValue(/homematic-/);
    await expect(dlg.locator('.fw-tpl-out').first()).toContainText(/homematic-(event|metric)-20/);
    await dlg.getByRole('button', {name: /Preview|Vorschau/}).click();
    await expect(dlg.locator('.fw-preview')).toContainText('{"index":{"_index":"homematic-');
    await expect(dlg.locator('.fw-preview')).toContainText('"@timestamp"');
    await dlg.getByRole('button', {name: /^Test|^Testen/}).click();
    await expect(dlg.locator('.fw-test-result')).toContainText(/1 document indexed/);
    await dlg.getByRole('button', {name: /Save|Speichern/}).click();
    await expect(dlg).toBeHidden();
    const ecard = page.locator('.fw-card', {hasText: 'Elastic'});
    await expect(ecard).toContainText('Elasticsearch');
    await expect(ecard).toContainText(/homematic-/);
    await page.screenshot({path: `test-results/targets-0.2-${test.info().project.name}.png`, fullPage: true});
});
