<script lang="ts">
    // The system page: the source's state, the settings (snapshot, language of the labels,
    // recent records) and about.
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';
    import {formatDateTime, formatDuration} from 'occulite-ui/format';

    import {get, put} from './api.js';
    import type {Health, Settings, StatusMessage} from './model.js';

    let {notices, health, status}: {notices: NoticesStore; health: Health | undefined; status: StatusMessage | undefined} = $props();

    let settings = $state<Settings | undefined>();
    let busy = $state(false);

    $effect(() => {
        get<Settings>('api/v1/settings')
            .then((s) => (settings = s))
            .catch((e) => notices.push('error', (e as Error).message));
    });

    async function save() {
        if (!settings) return;
        busy = true;
        try {
            settings = await put<Settings>('api/v1/settings', settings);
            notices.push('info', t('Settings saved'));
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = false;
        }
    }

    const src = $derived(status?.source ?? health?.source);
    const met = $derived(status?.metrics ?? health?.metrics);
    const metErrors = $derived(Object.entries(met?.errors ?? {}));
    const when = (s: string | undefined) => (s && !s.startsWith('0001') ? formatDateTime(Date.parse(s)) : '–');
</script>

<div class="fw-cards">
    <div class="fw-card fw-about">
        <div class="fw-card-head"><h3>{t('Source')}</h3><span class="fw-pill {src?.connected ? 'ok' : 'error'}">{src?.connected ? t('connected') : t('not connected')}</span></div>
        <dl>
            <dt>{t('Interfaces')}</dt>
            <dd>{src?.interfaces ?? 0}</dd>
            <dt>{t('Devices / channels / datapoints')}</dt>
            <dd>{src?.devices ?? 0} / {src?.channels ?? 0} / {src?.datapoints ?? 0}</dd>
            <dt>{t('Events since start')}</dt>
            <dd>{src?.events ?? 0}</dd>
            <dt>{t('Snapshot values')}</dt>
            <dd>{src?.snapshots ?? 0}</dd>
            <dt>{t('Stream resyncs')}</dt>
            <dd>{src?.resyncs ?? 0}</dd>
            <dt>{t('Last event')}</dt>
            <dd>{when(src?.last_event)}</dd>
            <dt>{t('Metadata revision')}</dt>
            <dd>{src?.meta_revision ?? 0}</dd>
            {#if src?.last_error}
                <dt>{t('Last error')}</dt>
                <dd class="fw-error">{src.last_error}</dd>
            {/if}
        </dl>
    </div>

    <div class="fw-card fw-about">
        <div class="fw-card-head"><h3>{t('Settings')}</h3></div>
        {#if settings}
            <div class="fw-form">
                <div class="fw-row">
                    <span class="fw-label">{t('Snapshot')}</span>
                    <div>
                        <label class="fw-inline"><input type="checkbox" bind:checked={settings.snapshot} /> {t('forward every current value at start and after a stream resync')}</label>
                        <div class="fw-help">{t('Gives the stores a defined starting value; off, only changes are forwarded.')}</div>
                    </div>
                </div>
                <div class="fw-row">
                    <label for="fw-lang">{t('Language of names')}</label>
                    <div>
                        <select id="fw-lang" class="hmm-select" bind:value={settings.lang}>
                            <option value="de">Deutsch</option>
                            <option value="en">English</option>
                        </select>
                        <div class="fw-help">{t('The room and function names in the records.')}</div>
                    </div>
                </div>
                <div class="fw-row">
                    <label for="fw-recent">{t('Recent records')}</label>
                    <div class="fw-inline"><input id="fw-recent" class="hmm-input" type="number" min="10" max="10000" bind:value={settings.recent} style="width:6em" /> {t('kept for the previews')}</div>
                </div>
                <div class="fw-row">
                    <label for="fw-minterval">{t('Metrics interval')}</label>
                    <div>
                        <div class="fw-inline"><input id="fw-minterval" class="hmm-input" type="number" min="10" max="3600" bind:value={settings.metrics_interval_s} style="width:6em" /> s</div>
                        <div class="fw-help">{t('How often the statistics and the system\'s figures are polled; only the groups a target\'s selection wants.')}</div>
                    </div>
                </div>
                <div><button class="hmm-button" type="button" onclick={() => void save()} disabled={busy}>{t('Save')}</button></div>
            </div>
        {/if}
    </div>

    <div class="fw-card fw-about">
        <div class="fw-card-head"><h3>{t('Metrics')}</h3><span class="fw-pill {metErrors.length ? 'retrying' : met?.polls ? 'ok' : 'idle'}">{metErrors.length ? t('{n} errors', {n: metErrors.length}) : met?.polls ? t('polling') : t('state.idle')}</span></div>
        <dl>
            <dt>{t('Last poll')}</dt>
            <dd>{when(met?.last_poll)}{#if met?.polls} ({met.duration_ms} ms){/if}</dd>
            <dt>{t('Groups polled')}</dt>
            <dd class="fw-mono fw-small">{met?.groups?.length ? met.groups.join(', ') : t('none - no target wants metrics')}</dd>
            <dt>{t('Records per poll')}</dt>
            <dd>{met?.records ?? 0}</dd>
            {#each metErrors as [g, e] (g)}
                <dt class="fw-error">{g}</dt>
                <dd class="fw-error">{e}</dd>
            {/each}
        </dl>
    </div>

    <div class="fw-card fw-about">
        <div class="fw-card-head"><h3>{t('About')}</h3></div>
        <dl>
            <dt>{t('Version')}</dt>
            <dd>{health?.version ?? '–'}</dd>
            <dt>{t('Running since')}</dt>
            <dd>{health ? `${when(health.started)} (${formatDuration(health.uptime_s * 1000)})` : '–'}</dd>
            <dt>{t('Target types')}</dt>
            <dd>{health?.types.join(', ') ?? '–'}</dd>
        </dl>
        <p class="fw-small fw-muted">{t('about.privacy')}</p>
        <p class="fw-links fw-small">
            <a href="https://github.com/hobbyquaker/lite-addon-forwarder" target="_blank" rel="noopener">lite-addon-forwarder {t('on GitHub')}</a>
            · <a href="https://github.com/hobbyquaker/lite-addon-forwarder/blob/main/CHANGELOG.md" target="_blank" rel="noopener">{t('Changelog')}</a>
            · <a href="https://github.com/hobbyquaker/lite-addon-forwarder/issues" target="_blank" rel="noopener">{t('Issues')}</a>
        </p>
    </div>
</div>
