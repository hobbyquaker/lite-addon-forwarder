<script lang="ts">
    // The targets: a card per target with its status, and the actions - edit, duplicate,
    // enable/disable, test, clear the queue, delete. "Add target" opens the editor.
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';
    import {formatDateTime, formatDuration} from 'occulite-ui/format';

    import ConfirmDialog from './ConfirmDialog.svelte';
    import TargetEditor from './TargetEditor.svelte';
    import {del, get, post} from './api.js';
    import {clone, describeTarget, formatBytes, TARGET_TYPES, type SelectionView, type Status, type StatusMessage, type Target, type TargetType, type TargetView, type TestResult} from './model.js';

    let {notices, status, selections, onchanged}: {notices: NoticesStore; status: StatusMessage | undefined; selections: SelectionView[]; onchanged: () => void} = $props();

    let targets = $state<TargetView[]>([]);
    let loaded = $state(false);
    let editing = $state<{target: Target | undefined; type: TargetType} | undefined>();
    let removing = $state<TargetView | undefined>();
    let busy = $state('');
    let testResult = $state<Record<string, TestResult>>({});

    async function load() {
        try {
            targets = await get<TargetView[]>('api/v1/targets');
            loaded = true;
        } catch (e) {
            notices.push('error', (e as Error).message);
        }
    }
    $effect(() => {
        void load();
    });

    const statusOf = $derived.by(() => {
        const m = new Map<string, Status>();
        for (const s of status?.targets ?? []) m.set(s.id, s);
        return m;
    });

    function st(tv: TargetView): Status | undefined {
        return statusOf.get(tv.id) ?? tv.status;
    }

    function selectionName(id: string): string {
        return selections.find((s) => s.id === id)?.name ?? (id ? id : t('everything'));
    }

    function stateLabel(s: Status | undefined): string {
        if (!s) return t('state.idle');
        return t('state.' + s.state);
    }

    /** The last error is shown while it is newer than the last success: a recovered target is clean. */
    function currentError(s: Status | undefined): string {
        if (!s) return '';
        const err = s.last_error || s.last_render_error;
        if (!err) return '';
        if (s.state === 'retrying' || s.state === 'error') return err;
        if (!s.last_success || s.last_success.startsWith('0001')) return err;
        return Date.parse(s.last_error_at) > Date.parse(s.last_success) ? err : '';
    }

    function lag(s: Status): string {
        if (!s.queued) return '–';
        return formatDuration(s.lag_ms);
    }

    async function toggle(tv: TargetView) {
        busy = tv.id;
        try {
            await post(`api/v1/targets/${encodeURIComponent(tv.id)}/enabled`, {enabled: !tv.enabled});
            await load();
            onchanged();
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function test(tv: TargetView) {
        busy = tv.id;
        try {
            const r = await post<TestResult>('api/v1/targets/test', tv);
            testResult = {...testResult, [tv.id]: r};
            if (r.ok) notices.push('info', t('Test of {name}: {message}', {name: tv.name, message: r.message.split('\n')[0] ?? ''}));
            else notices.push('error', t('Test of {name} failed: {message}', {name: tv.name, message: r.message}));
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function clearQueue(tv: TargetView) {
        busy = tv.id;
        try {
            await post(`api/v1/targets/${encodeURIComponent(tv.id)}/clear`);
            notices.push('info', t('Queue of {name} cleared', {name: tv.name}));
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = '';
        }
    }

    async function remove() {
        if (!removing) return;
        busy = removing.id;
        try {
            await del(`api/v1/targets/${encodeURIComponent(removing.id)}`);
            notices.push('info', t('Target {name} removed', {name: removing.name}));
            removing = undefined;
            await load();
            onchanged();
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = '';
        }
    }

    function duplicate(tv: TargetView) {
        const c = clone(tv);
        c.id = '';
        c.name = t('{name} (copy)', {name: tv.name});
        c.secrets = {};
        delete c.status;
        editing = {target: c, type: c.type};
    }

    function edit(tv: TargetView) {
        const c = clone(tv);
        delete c.status;
        editing = {target: c, type: c.type};
    }

    let addType = $state<TargetType>('influx');
</script>

<div class="fw-toolbar">
    <select class="hmm-select" bind:value={addType} aria-label={t('Target type')}>
        {#each TARGET_TYPES as tt (tt.type)}
            <option value={tt.type}>{t(tt.label)}</option>
        {/each}
    </select>
    <button class="hmm-button" type="button" onclick={() => (editing = {target: undefined, type: addType})}>{t('Add target')}</button>
    <span class="fw-spacer"></span>
    <span class="fw-muted fw-small">{t('A target receives the records of its selection. Nothing is sent anywhere you did not configure.')}</span>
</div>

{#if loaded && targets.length === 0}
    <div class="fw-empty">{t('No targets yet. Add one: InfluxDB, a webhook, Elasticsearch or Prometheus; pick a selection, test it, save.')}</div>
{/if}

<div class="fw-cards">
    {#each targets as tv (tv.id)}
        {@const s = st(tv)}
        {@const d = describeTarget(tv)}
        <div class="fw-card" class:disabled={!tv.enabled}>
            <div class="fw-card-head">
                <span class="fw-pill {s?.state ?? 'idle'}" title={stateLabel(s)}>{stateLabel(s)}</span>
                <h3 title={tv.name}>{tv.name}</h3>
                <span class="fw-muted fw-small">{d.kind}</span>
            </div>
            <div class="fw-card-line fw-small">
                <span class="fw-ellipsis fw-muted" title={d.where}>{d.where}</span>
                <span class="fw-ellipsis">{t('Selection')}: {selectionName(tv.selection)}</span>
            </div>
            <div class="fw-stats">
                <div class="fw-stat"><b>{s?.sent ?? 0}</b><span>{t('sent')}</span></div>
                <div class="fw-stat"><b>{s?.queued ?? 0}</b><span>{t('queued')}</span></div>
                <div class="fw-stat" title={s?.spill_bytes ? formatBytes(s.spill_bytes) : ''}><b>{s?.spilled ?? 0}</b><span>{t('on disk')}</span></div>
                <div class="fw-stat"><b>{s?.dropped ?? 0}</b><span>{t('dropped')}</span></div>
                <div class="fw-stat"><b>{s ? lag(s) : '–'}</b><span>{t('lag')}</span></div>
                <div class="fw-stat"><b>{s?.batches ?? 0}</b><span>{t('batches')}</span></div>
                <div class="fw-stat"><b>{s?.retried ?? 0}</b><span>{t('retries')}</span></div>
                <div class="fw-stat"><b>{s?.render_errors ?? 0}</b><span>{t('template errors')}</span></div>
                <div class="fw-stat">
                    <b>{s?.last_success && !s.last_success.startsWith('0001') ? formatDateTime(Date.parse(s.last_success)) : '–'}</b><span>{t('last success')}</span>
                </div>
            </div>
            <div class="fw-card-error" title={currentError(s)}>{currentError(s)}</div>
            {#if testResult[tv.id]}
                <div class="fw-test-result {testResult[tv.id]?.ok ? '' : 'fw-error'}">{testResult[tv.id]?.message}</div>
            {/if}
            <div class="fw-card-actions">
                <button class="hmm-button" type="button" onclick={() => edit(tv)} disabled={busy === tv.id}>{t('Edit')}</button>
                <button class="hmm-button" type="button" onclick={() => void test(tv)} disabled={busy === tv.id}>{t('Test')}</button>
                <button class="hmm-button" type="button" onclick={() => void toggle(tv)} disabled={busy === tv.id}>{tv.enabled ? t('Disable') : t('Enable')}</button>
                <button class="hmm-button" type="button" onclick={() => duplicate(tv)} disabled={busy === tv.id}>{t('Duplicate')}</button>
                <button class="hmm-button" type="button" onclick={() => void clearQueue(tv)} disabled={busy === tv.id || !(s?.queued ?? 0)} title={t('Drops the queued records')}>{t('Clear queue')}</button>
                <button class="hmm-button" type="button" onclick={() => (removing = tv)} disabled={busy === tv.id}>{t('Delete')}</button>
            </div>
        </div>
    {/each}
</div>

{#if editing}
    <TargetEditor
        open={true}
        target={editing.target}
        type={editing.type}
        {selections}
        {notices}
        onclose={() => (editing = undefined)}
        onsaved={() => {
            editing = undefined;
            void load();
            onchanged();
        }}
    />
{/if}

<ConfirmDialog
    open={removing !== undefined}
    title={t('Delete target')}
    message={removing ? t('Delete the target {name}? Its queued records are lost; the store keeps what it already received.', {name: removing.name}) : ''}
    confirmLabel={t('Delete')}
    danger
    busy={busy !== ''}
    onconfirm={() => void remove()}
    oncancel={() => (removing = undefined)}
/>
