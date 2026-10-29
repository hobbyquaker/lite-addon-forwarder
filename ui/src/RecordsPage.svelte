<script lang="ts">
    // The recent records as the addon saw them, with the targets each one went to; polled every
    // two seconds while the page is open. A selection filter shows what one selection matches.
    import {onMount} from 'svelte';
    import {t} from 'occulite-ui/i18n';

    import {get} from './api.js';
    import {formatValue, type Rec, type SelectionView, type Status} from './model.js';

    let {selections, targets}: {selections: SelectionView[]; targets: Status[]} = $props();

    let records = $state<Rec[]>([]);
    let selection = $state('');
    let paused = $state(false);
    let error = $state('');

    async function load() {
        if (paused) return;
        try {
            records = await get<Rec[]>(`api/v1/recent?n=200${selection ? '&selection=' + encodeURIComponent(selection) : ''}`);
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }

    onMount(() => {
        void load();
        const timer = setInterval(() => void load(), 2000);
        return () => clearInterval(timer);
    });

    $effect(() => {
        void selection;
        void load();
    });

    function targetNames(ids: string[]): string {
        return ids.map((id) => targets.find((s) => s.id === id)?.name ?? id).join(', ');
    }
</script>

<div class="fw-toolbar">
    <select class="hmm-select" bind:value={selection} aria-label={t('Selection')}>
        <option value="">{t('all records')}</option>
        {#each selections as s (s.id)}
            <option value={s.id}>{t('Selection')}: {s.name}</option>
        {/each}
    </select>
    <button class="hmm-button" type="button" onclick={() => (paused = !paused)} style="min-width:6em">{paused ? t('Resume') : t('Pause')}</button>
    <span class="fw-muted fw-small">{t('{n} records, newest first', {n: records.length})}</span>
    {#if error}<span class="fw-error fw-small">{error}</span>{/if}
</div>

<div class="fw-table-wrap">
    <table class="fw-table">
        <thead>
            <tr>
                <th>{t('Time')}</th>
                <th>{t('Kind')}</th>
                <th>{t('Interface')}</th>
                <th>{t('Address')}</th>
                <th>{t('Name')}</th>
                <th>{t('Datapoint')}</th>
                <th>{t('Value')}</th>
                <th>{t('Rooms')}</th>
                <th>{t('Source')}</th>
                <th>{t('Targets')}</th>
            </tr>
        </thead>
        <tbody>
            {#each records as r, i (i)}
                <tr>
                    <td class="fw-mono">{r.ts.replace('T', ' ').slice(0, 23)}</td>
                    <td><span class="fw-pill kind">{t('kind.' + r.kind)}</span></td>
                    <td>{r.interface ?? r.group ?? r.stream ?? ''}</td>
                    <td class="fw-mono">{r.address ?? r.log_unit ?? ''}</td>
                    <td title={r.channel_name}>{r.channel_name ?? r.name ?? ''}</td>
                    <td class="fw-mono">{r.key ?? r.name ?? r.priority ?? ''}</td>
                    <td class="num" title={r.value_text || formatValue(r.value)}>{formatValue(r.value)}{r.value_text && r.type === 'ENUM' ? ` (${r.value_text})` : ''}{r.unit ? ' ' + r.unit : ''}</td>
                    <td title={r.rooms.join(', ')}>{r.rooms.join(', ')}</td>
                    <td class="fw-muted">{r.source ?? ''}</td>
                    <td title={targetNames(r.targets)}>{r.targets.length ? targetNames(r.targets) : '–'}</td>
                </tr>
            {:else}
                <tr><td colspan="10" class="fw-muted">{t('No records yet.')}</td></tr>
            {/each}
        </tbody>
    </table>
</div>
