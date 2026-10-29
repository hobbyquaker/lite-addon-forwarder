<script lang="ts">
    // The target editor: a dialog with the connection, the templates (with presets and live
    // previews), the delivery settings and TLS; Test sends one record, Preview shows the recent
    // records as this target would serialise them. Secrets are write-only: a stored one shows
    // as "set", a new value replaces it, nothing is ever read back.
    import {Dialog, Tabs} from 'occulite-ui';
    import {i18n, t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import TemplateField from './TemplateField.svelte';
    import {get, post, put} from './api.js';
    import {clone, type FieldTemplate, type Influx, type InfluxTemplates, type Presets, type Preview, type SelectionView, type Target, type TargetType, type TemplateHelp, type TestResult} from './model.js';

    let {open, target, type, selections, notices, onclose, onsaved}: {open: boolean; target: Target | undefined; type: TargetType; selections: SelectionView[]; notices: NoticesStore; onclose: () => void; onsaved: () => void} = $props();

    const editing = $derived(target !== undefined && target.id !== '');
    let draft = $state<Target | undefined>();
    let presets = $state<Presets | undefined>();
    let help = $state<TemplateHelp | undefined>();
    let tab = $state('connection');
    let secrets = $state<Record<string, string>>({});
    let clearSecrets = $state<string[]>([]);
    let busy = $state(false);
    let error = $state('');
    let testResult = $state<TestResult | undefined>();
    let preview = $state<Preview | undefined>();
    let showHelp = $state(false);
    let kindTab = $state<'event' | 'metric' | 'log'>('event');
    let headerRows = $state<{k: string; v: string}[]>([]);

    $effect(() => {
        get<Presets>('api/v1/presets')
            .then((p) => {
                presets = p;
                if (!draft) start(p);
            })
            .catch((e) => (error = (e as Error).message));
        get<TemplateHelp>('api/v1/templates/help')
            .then((h) => (help = h))
            .catch(() => {});
    });

    function start(p: Presets) {
        const d = target ? clone(target) : clone(p.defaults[type]);
        if (!target) {
            d.name = '';
            d.selection = selections[0]?.id ?? '';
        }
        headerRows = Object.entries(d.webhook?.headers ?? {}).map(([k, v]) => ({k, v}));
        draft = d;
    }

    function body(): Target {
        const d = clone(draft!);
        if (d.webhook) {
            d.webhook.headers = {};
            for (const h of headerRows) if (h.k.trim()) d.webhook.headers[h.k.trim()] = h.v;
        }
        const set: Record<string, string> = {};
        for (const [k, v] of Object.entries(secrets)) if (v) set[k] = v;
        d.set_secrets = set;
        d.clear_secrets = clearSecrets;
        return d;
    }

    async function save() {
        if (!draft) return;
        busy = true;
        error = '';
        try {
            const b = body();
            if (editing) await put(`api/v1/targets/${encodeURIComponent(draft.id)}`, b);
            else await post('api/v1/targets', b);
            notices.push('info', t('Target {name} saved', {name: draft.name}));
            onsaved();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }

    async function test() {
        if (!draft) return;
        busy = true;
        testResult = undefined;
        try {
            testResult = await post<TestResult>('api/v1/targets/test', body());
        } catch (e) {
            testResult = {ok: false, message: (e as Error).message, record: ''};
        } finally {
            busy = false;
        }
    }

    async function loadPreview() {
        if (!draft) return;
        busy = true;
        try {
            preview = await post<Preview>('api/v1/targets/preview?n=10', body());
        } catch (e) {
            preview = {records: 0, text: '', errors: [(e as Error).message]};
        } finally {
            busy = false;
        }
    }

    function applyPreset(name: string) {
        if (!draft?.influx || !presets) return;
        const p = presets.influx[name];
        if (!p) return;
        draft.influx.preset = name;
        draft.influx.event = clone(p.event);
        draft.influx.metric = clone(p.metric);
        draft.influx.log = clone(p.log);
    }

    function applyWebhookPreset(name: string) {
        if (!draft?.webhook || !presets) return;
        const p = presets.webhook[name];
        if (!p) return;
        draft.webhook.body = p.body;
        draft.webhook.format = p.format;
    }

    function markCustom() {
        if (draft?.influx) draft.influx.preset = 'custom';
    }

    function tpls(): InfluxTemplates | undefined {
        return draft?.influx?.[kindTab];
    }

    function addTag() {
        tpls()?.tags.push({name: '', value: ''});
        markCustom();
    }
    function addField() {
        tpls()?.fields.push({name: '', value: '', type: 'auto'});
        markCustom();
    }
    function removeTag(i: number) {
        tpls()?.tags.splice(i, 1);
        markCustom();
    }
    function removeField(i: number) {
        tpls()?.fields.splice(i, 1);
        markCustom();
    }

    const secretNames = $derived.by(() => {
        if (!draft) return [] as string[];
        if (draft.type === 'influx') return draft.influx?.version === '1' ? ['password'] : ['token'];
        return draft.webhook?.auth === 'basic' ? ['password'] : draft.webhook?.auth === 'bearer' ? ['token'] : [];
    });

    const title = $derived(editing ? t('Edit target') : type === 'influx' ? t('New InfluxDB target') : t('New webhook target'));
    const tabs = $derived([
        {id: 'connection', label: t('Connection')},
        {id: 'templates', label: draft?.type === 'influx' ? t('Schema') : t('Body')},
        {id: 'delivery', label: t('Delivery')},
        {id: 'tls', label: 'TLS'},
    ]);
    const kindTabs = $derived([
        {id: 'event', label: t('Events')},
        {id: 'metric', label: t('Metrics')},
        {id: 'log', label: t('Logs')},
    ]);
    const lang = $derived(i18n.language === 'de' ? 'de' : 'en');
    const fieldTypes: FieldTemplate['type'][] = ['auto', 'float', 'integer', 'boolean', 'string'];

    function secretLabel(name: string): string {
        return name === 'token' ? t('Token') : t('Password');
    }
</script>

<Dialog {open} {title} width="62rem" height="min(88vh, 52rem)" minWidth={720} closable={!busy} {onclose} testId="target-editor">
    {#if draft}
        <div class="fw-dialog-body">
            <div class="fw-form">
                <div class="fw-row">
                    <label for="fw-name">{t('Name')}</label>
                    <input id="fw-name" class="hmm-input" type="text" bind:value={draft.name} placeholder={draft.type === 'influx' ? 'InfluxDB' : 'Webhook'} />
                </div>
                <div class="fw-row">
                    <label for="fw-selection">{t('Selection')}</label>
                    <div>
                        <select id="fw-selection" class="hmm-select" bind:value={draft.selection}>
                            <option value="">{t('everything (no selection)')}</option>
                            {#each selections as s (s.id)}
                                <option value={s.id}>{s.name} ({s.count.datapoints})</option>
                            {/each}
                        </select>
                        <div class="fw-help">{t('What this target receives; selections are managed on their own page.')}</div>
                    </div>
                </div>
                <div class="fw-row">
                    <span class="fw-label">{t('Enabled')}</span>
                    <label class="fw-inline"><input type="checkbox" bind:checked={draft.enabled} /> {t('forward records to this target')}</label>
                </div>
            </div>
            <div class="fw-dialog-tabs"><Tabs {tabs} bind:active={tab} /></div>

            {#if tab === 'connection'}
                <div class="fw-form">
                    {#if draft.influx}
                        <div class="fw-row">
                            <span class="fw-label">{t('API')}</span>
                            <div class="fw-inline">
                                <label><input type="radio" name="fw-ver" value="2" bind:group={draft.influx.version} /> InfluxDB 2 / 3 (<code>/api/v2/write</code>)</label>
                                <label><input type="radio" name="fw-ver" value="1" bind:group={draft.influx.version} /> InfluxDB 1 (<code>/write</code>)</label>
                            </div>
                        </div>
                        <div class="fw-row">
                            <label for="fw-url">URL</label>
                            <div>
                                <input id="fw-url" class="hmm-input" type="url" bind:value={draft.influx.url} placeholder="http://influx.example:8086" />
                                <div class="fw-help">{t('The server without a path; VictoriaMetrics, QuestDB and Telegraf accept the same write.')}</div>
                            </div>
                        </div>
                        {#if draft.influx.version === '1'}
                            <div class="fw-row">
                                <label for="fw-db">{t('Database')}</label>
                                <input id="fw-db" class="hmm-input" type="text" bind:value={draft.influx.database} />
                            </div>
                            <div class="fw-row">
                                <label for="fw-rp">{t('Retention policy')}</label>
                                <input id="fw-rp" class="hmm-input" type="text" bind:value={draft.influx.retention_policy} placeholder={t('(default)')} />
                            </div>
                            <div class="fw-row">
                                <label for="fw-user">{t('User')}</label>
                                <input id="fw-user" class="hmm-input" type="text" bind:value={draft.influx.user} autocomplete="off" />
                            </div>
                        {:else}
                            <div class="fw-row">
                                <label for="fw-org">{t('Organisation')}</label>
                                <input id="fw-org" class="hmm-input" type="text" bind:value={draft.influx.org} />
                            </div>
                            <div class="fw-row">
                                <label for="fw-bucket">{t('Bucket')}</label>
                                <input id="fw-bucket" class="hmm-input" type="text" bind:value={draft.influx.bucket} />
                            </div>
                        {/if}
                        <div class="fw-row">
                            <span class="fw-label">{t('Compression')}</span>
                            <label class="fw-inline"><input type="checkbox" bind:checked={draft.influx.gzip} /> gzip</label>
                        </div>
                    {:else if draft.webhook}
                        <div class="fw-row">
                            <label for="fw-wurl">URL</label>
                            <div>
                                <TemplateField id="fw-wurl" bind:value={draft.webhook.url} selection={draft.selection} placeholder="https://host.example/hook/{'{{'} kind {'}}'}" label="URL" />
                                <div class="fw-help">{t('A template: a batch is grouped by the rendered URL.')}</div>
                            </div>
                        </div>
                        <div class="fw-row">
                            <label for="fw-method">{t('Method')}</label>
                            <div class="fw-inline">
                                <select id="fw-method" class="hmm-select" bind:value={draft.webhook.method}>
                                    <option>POST</option>
                                    <option>PUT</option>
                                    <option>PATCH</option>
                                </select>
                                <label><input type="checkbox" bind:checked={draft.webhook.gzip} /> gzip</label>
                            </div>
                        </div>
                        <div class="fw-row">
                            <span class="fw-label">{t('Authentication')}</span>
                            <div class="fw-inline">
                                <label><input type="radio" name="fw-auth" value="none" bind:group={draft.webhook.auth} /> {t('none')}</label>
                                <label><input type="radio" name="fw-auth" value="basic" bind:group={draft.webhook.auth} /> Basic</label>
                                <label><input type="radio" name="fw-auth" value="bearer" bind:group={draft.webhook.auth} /> Bearer</label>
                            </div>
                        </div>
                        {#if draft.webhook.auth === 'basic'}
                            <div class="fw-row">
                                <label for="fw-wuser">{t('User')}</label>
                                <input id="fw-wuser" class="hmm-input" type="text" bind:value={draft.webhook.user} autocomplete="off" />
                            </div>
                        {/if}
                        <div class="fw-row">
                            <span class="fw-label">{t('Headers')}</span>
                            <div class="fw-tpl-rows">
                                {#each headerRows as h, i (i)}
                                    <div class="fw-tpl-row notype">
                                        <input class="hmm-input fw-mono" type="text" bind:value={h.k} placeholder="X-Header" aria-label={t('Header name')} />
                                        <input class="hmm-input fw-mono" type="text" bind:value={h.v} placeholder={t('value')} aria-label={t('Header value')} />
                                        <button class="fw-icon-button" type="button" onclick={() => headerRows.splice(i, 1)} title={t('Remove')}>×</button>
                                    </div>
                                {/each}
                                <div><button class="hmm-button" type="button" onclick={() => headerRows.push({k: '', v: ''})}>{t('Add header')}</button></div>
                            </div>
                        </div>
                    {/if}
                    {#each secretNames as name (name)}
                        <div class="fw-row">
                            <label for="fw-secret-{name}">{secretLabel(name)}</label>
                            <div>
                                <div class="fw-inline">
                                    <input id="fw-secret-{name}" class="hmm-input" type="password" autocomplete="new-password" bind:value={secrets[name]} placeholder={draft.secrets?.[name] && !clearSecrets.includes(name) ? t('(set - enter a new value to replace it)') : t('(not set)')} />
                                    {#if draft.secrets?.[name]}
                                        <label><input type="checkbox" checked={clearSecrets.includes(name)} onchange={(e) => (clearSecrets = (e.currentTarget as HTMLInputElement).checked ? [...clearSecrets, name] : clearSecrets.filter((x) => x !== name))} /> {t('remove')}</label>
                                    {/if}
                                </div>
                                <div class="fw-help">{t('Stored in a file only the addon can read; never shown again.')}</div>
                            </div>
                        </div>
                    {/each}
                </div>
            {:else if tab === 'templates'}
                <div class="fw-form">
                    {#if draft.influx}
                        <div class="fw-row">
                            <label for="fw-preset">{t('Preset')}</label>
                            <div>
                                <select id="fw-preset" class="hmm-select" value={draft.influx.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                                    {#each presets?.influx_order ?? [] as name (name)}
                                        <option value={name}>{t('preset.' + name)}</option>
                                    {/each}
                                    <option value="custom">{t('preset.custom')}</option>
                                </select>
                                <div class="fw-help">{t('preset.help.' + (draft.influx.preset || 'custom'))}</div>
                            </div>
                        </div>
                        <div class="fw-dialog-tabs"><Tabs tabs={kindTabs} bind:active={kindTab} /></div>
                        {#key kindTab}
                            {@const tp = draft.influx[kindTab]}
                            <div class="fw-row">
                                <label for="fw-measurement">{t('Measurement')}</label>
                                <TemplateField id="fw-measurement" bind:value={tp.measurement} selection={draft.selection} label={t('Measurement')} />
                            </div>
                            <div class="fw-row">
                                <span class="fw-label">{t('Tags')}</span>
                                <div class="fw-tpl-rows">
                                    {#each tp.tags as tag, i (i)}
                                        <div class="fw-tpl-row notype">
                                            <input class="hmm-input fw-mono" type="text" bind:value={tag.name} oninput={markCustom} placeholder={t('tag')} aria-label={t('Tag name')} />
                                            <TemplateField bind:value={tag.value} selection={draft.selection} label={t('Tag value')} />
                                            <button class="fw-icon-button" type="button" onclick={() => removeTag(i)} title={t('Remove')}>×</button>
                                        </div>
                                    {/each}
                                    <div><button class="hmm-button" type="button" onclick={addTag}>{t('Add tag')}</button> <span class="fw-help">{t('An empty tag value leaves the tag out.')}</span></div>
                                </div>
                            </div>
                            <div class="fw-row">
                                <span class="fw-label">{t('Fields')}</span>
                                <div class="fw-tpl-rows">
                                    {#each tp.fields as f, i (i)}
                                        <div class="fw-tpl-row">
                                            <input class="hmm-input fw-mono" type="text" bind:value={f.name} oninput={markCustom} placeholder={t('field')} aria-label={t('Field name')} />
                                            <TemplateField bind:value={f.value} selection={draft.selection} label={t('Field value')} />
                                            <select class="hmm-select" bind:value={f.type} onchange={markCustom} aria-label={t('Field type')}>
                                                {#each fieldTypes as ft (ft)}
                                                    <option value={ft}>{t('type.' + ft)}</option>
                                                {/each}
                                            </select>
                                            <button class="fw-icon-button" type="button" onclick={() => removeField(i)} title={t('Remove')}>×</button>
                                        </div>
                                    {/each}
                                    <div><button class="hmm-button" type="button" onclick={addField}>{t('Add field')}</button> <span class="fw-help">{t('auto: the value in its own type (numbers as floats); an empty value leaves the field out.')}</span></div>
                                </div>
                            </div>
                        {/key}
                    {:else if draft.webhook}
                        <div class="fw-row">
                            <label for="fw-format">{t('Format')}</label>
                            <div class="fw-inline">
                                <select id="fw-format" class="hmm-select" bind:value={draft.webhook.format}>
                                    <option value="json">JSON ({t('an array per batch')})</option>
                                    <option value="ndjson">NDJSON ({t('one line per record')})</option>
                                </select>
                                <select class="hmm-select" value="" onchange={(e) => { applyWebhookPreset((e.currentTarget as HTMLSelectElement).value); (e.currentTarget as HTMLSelectElement).value = ''; }} aria-label={t('Preset')}>
                                    <option value="">{t('Preset…')}</option>
                                    <option value="record">{t('the whole record')}</option>
                                    <option value="compact">{t('compact')}</option>
                                </select>
                            </div>
                        </div>
                        <div class="fw-row">
                            <label for="fw-body">{t('Body per record')}</label>
                            <div>
                                <TemplateField id="fw-body" bind:value={draft.webhook.body} selection={draft.selection} multiline rows={6} label={t('Body')} />
                                <div class="fw-help">{t('A template that renders one JSON document; the default is the whole record.')}</div>
                            </div>
                        </div>
                        <div class="fw-row">
                            <label for="fw-ct">Content-Type</label>
                            <input id="fw-ct" class="hmm-input" type="text" bind:value={draft.webhook.content_type} placeholder={draft.webhook.format === 'ndjson' ? 'application/x-ndjson' : 'application/json'} />
                        </div>
                    {/if}
                    <div>
                        <button class="hmm-button" type="button" onclick={() => (showHelp = !showHelp)}>{showHelp ? t('Hide variables') : t('Variables and filters')}</button>
                    </div>
                    {#if showHelp && help}
                        <div class="fw-match">
                            <table class="fw-help-table">
                                <tbody>
                                    {#each help.variables as v (v.name)}
                                        <tr><td>{'{{ ' + v.name + ' }}'}</td><td class="fw-muted">{v.type}</td><td>{v.doc[lang]}</td></tr>
                                    {/each}
                                    {#each help.filters as f (f.name)}
                                        <tr><td>| {f.name}</td><td></td><td>{f.doc[lang]}</td></tr>
                                    {/each}
                                </tbody>
                            </table>
                        </div>
                    {/if}
                </div>
            {:else if tab === 'delivery'}
                <div class="fw-form">
                    <div class="fw-row">
                        <label for="fw-bsize">{t('Batch')}</label>
                        <div class="fw-inline">
                            <input id="fw-bsize" class="hmm-input" type="number" min="1" max="10000" bind:value={draft.batch.size} style="width:6em" /> {t('records, or every')}
                            <input class="hmm-input" type="number" min="100" max="3600000" step="100" bind:value={draft.batch.interval_ms} style="width:7em" aria-label={t('Interval (ms)')} /> ms,
                            {t('at most')} <input class="hmm-input" type="number" min="1024" bind:value={draft.batch.max_bytes} style="width:8em" aria-label={t('Bytes')} /> {t('bytes')}
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-queue">{t('Queue')}</label>
                        <div>
                            <div class="fw-inline"><input id="fw-queue" class="hmm-input" type="number" min="100" max="1000000" bind:value={draft.queue.max_records} style="width:8em" /> {t('records in memory')}</div>
                            <div class="fw-help">{t('While the target is unreachable the records wait here; when it is full the oldest are dropped and counted.')}</div>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-retry">{t('Retry')}</label>
                        <div class="fw-inline">
                            <input id="fw-retry" class="hmm-input" type="number" min="100" bind:value={draft.retry.min_ms} style="width:7em" /> ms {t('doubling up to')}
                            <input class="hmm-input" type="number" min="100" bind:value={draft.retry.max_ms} style="width:7em" aria-label={t('Maximum (ms)')} /> ms
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-timeout">{t('Timeout')}</label>
                        <div class="fw-inline"><input id="fw-timeout" class="hmm-input" type="number" min="1" max="300" bind:value={draft.timeout_s} style="width:5em" /> s</div>
                    </div>
                </div>
            {:else}
                <div class="fw-form">
                    <div class="fw-row">
                        <span class="fw-label">{t('Certificate check')}</span>
                        <div>
                            <label class="fw-inline"><input type="checkbox" bind:checked={draft.tls.insecure} /> {t('skip (insecure: accepts any certificate)')}</label>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-ca">{t('Own CA (PEM)')}</label>
                        <div>
                            <textarea id="fw-ca" rows="6" bind:value={draft.tls.ca} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false"></textarea>
                            <div class="fw-help">{t('Trusted instead of the system\'s authorities, for a server with a self-signed or private certificate.')}</div>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-sni">{t('Server name')}</label>
                        <input id="fw-sni" class="hmm-input" type="text" bind:value={draft.tls.server_name} placeholder={t('(from the URL)')} />
                    </div>
                </div>
            {/if}

            {#if preview}
                <div class="fw-match">
                    <div class="fw-small fw-muted">{t('{n} recent records as this target sends them', {n: preview.records})}{#if preview.errors.length} · <span class="fw-error">{preview.errors.length} {t('errors')}</span>{/if}</div>
                    <pre class="fw-preview">{preview.text}{preview.errors.length ? '\n' + preview.errors.join('\n') : ''}</pre>
                </div>
            {/if}
            {#if testResult}
                <div class="fw-test-result {testResult.ok ? '' : 'fw-error'}">{testResult.ok ? t('OK') + ' · ' : t('Failed') + ' · '}{testResult.message}</div>
            {/if}
            {#if error}
                <div class="fw-error">{error}</div>
            {/if}
        </div>
    {:else}
        <p class="fw-muted">{t('Loading…')}</p>
    {/if}
    {#snippet buttons()}
        <button class="hmm-button" type="button" onclick={() => void loadPreview()} disabled={busy || !draft}>{t('Preview')}</button>
        <button class="hmm-button" type="button" onclick={() => void test()} disabled={busy || !draft}>{t('Test')}</button>
        <span class="fw-spacer"></span>
        <button class="hmm-button" type="button" onclick={onclose} disabled={busy}>{t('Cancel')}</button>
        <button class="hmm-button" type="button" onclick={() => void save()} disabled={busy || !draft}>{t('Save')}</button>
    {/snippet}
</Dialog>
