<script lang="ts">
    // The target editor: a dialog with the connection, the templates (with presets and live
    // previews), the delivery settings and TLS; Test sends one record, Preview shows the recent
    // records as this target would serialise them. The type's own fields live in its form
    // (targets/*Form.svelte); this dialog holds what every type has. Secrets are write-only: a
    // stored one shows as "set", a new value replaces it, nothing is ever read back.
    import {Dialog, Tabs} from 'occulite-ui';
    import {i18n, t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import {get, post, put} from './api.js';
    import {clone, secretNames, TARGET_TYPES, type Presets, type Preview, type SelectionView, type Target, type TargetType, type TemplateHelp, type TestResult} from './model.js';
    import ElasticForm from './targets/ElasticForm.svelte';
    import InfluxForm from './targets/InfluxForm.svelte';
    import PrometheusForm from './targets/PrometheusForm.svelte';
    import WebhookForm from './targets/WebhookForm.svelte';

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
        draft = d;
    }

    function body(): Target {
        const d = clone(draft!);
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

    const names = $derived(draft ? secretNames(draft) : []);
    const typeLabel = $derived(t(TARGET_TYPES.find((x) => x.type === type)?.label ?? type));
    const title = $derived(editing ? t('Edit target') : t('New target: {type}', {type: typeLabel}));
    const templatesLabel = $derived.by(() => {
        switch (draft?.type) {
            case 'influx':
                return t('Schema');
            case 'webhook':
                return t('Body');
            case 'elastic':
                return t('Documents');
            case 'prometheus':
                return t('Metrics');
        }
        return t('Templates');
    });
    const tabs = $derived([
        {id: 'connection', label: t('Connection')},
        {id: 'templates', label: templatesLabel},
        {id: 'delivery', label: t('Delivery')},
        {id: 'tls', label: 'TLS'},
    ]);
    const lang = $derived(i18n.language === 'de' ? 'de' : 'en');

    function secretLabel(name: string): string {
        switch (name) {
            case 'token':
                return t('Token');
            case 'api_key':
                return t('API key');
        }
        return t('Password');
    }
    const tlsNeeded = $derived(draft?.type !== 'prometheus' || draft.prometheus?.mode === 'remote_write');
</script>

<Dialog {open} {title} width="62rem" height="min(88vh, 52rem)" minWidth={720} closable={!busy} {onclose} testId="target-editor">
    {#if draft}
        <div class="fw-dialog-body">
            <div class="fw-form">
                <div class="fw-row">
                    <label for="fw-name">{t('Name')}</label>
                    <input id="fw-name" class="hmm-input" type="text" bind:value={draft.name} placeholder={typeLabel} />
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

            {#if tab === 'connection' || tab === 'templates'}
                <div class="fw-form">
                    {#if draft.type === 'influx'}
                        <InfluxForm bind:draft section={tab} {presets} />
                    {:else if draft.type === 'webhook'}
                        <WebhookForm bind:draft section={tab} {presets} />
                    {:else if draft.type === 'elastic'}
                        <ElasticForm bind:draft section={tab} {presets} />
                    {:else if draft.type === 'prometheus'}
                        <PrometheusForm bind:draft section={tab} {presets} />
                    {/if}
                    {#if tab === 'connection'}
                        {#each names as name (name)}
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
                    {:else}
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
                            <div class="fw-help">{t('While the target is unreachable the records wait here, then on disk; when both are full the oldest are dropped and counted.')}</div>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-spill">{t('Disk spill')}</label>
                        <div>
                            <div class="fw-inline"><input id="fw-spill" class="hmm-input" type="number" min="0" max="10000" bind:value={draft.queue.spill_mb} style="width:6em" /> MB</div>
                            <div class="fw-help">{t('The overflow on the system\'s storage while the target is down, kept over a restart; 0 switches it off. Nothing is written while the target keeps up.')}</div>
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
                    {#if !tlsNeeded}
                        <div class="fw-help">{t('A scrape endpoint is served by the system\'s own web server with its certificate; nothing to set here.')}</div>
                    {/if}
                    <div class="fw-row">
                        <span class="fw-label">{t('Certificate check')}</span>
                        <div>
                            <label class="fw-inline"><input type="checkbox" bind:checked={draft.tls.insecure} disabled={!tlsNeeded} /> {t('skip (insecure: accepts any certificate)')}</label>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-ca">{t('Own CA (PEM)')}</label>
                        <div>
                            <textarea id="fw-ca" rows="6" bind:value={draft.tls.ca} placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" disabled={!tlsNeeded}></textarea>
                            <div class="fw-help">{t('Trusted instead of the system\'s authorities, for a server with a self-signed or private certificate.')}</div>
                        </div>
                    </div>
                    <div class="fw-row">
                        <label for="fw-sni">{t('Server name')}</label>
                        <input id="fw-sni" class="hmm-input" type="text" bind:value={draft.tls.server_name} placeholder={t('(from the URL)')} disabled={!tlsNeeded} />
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
