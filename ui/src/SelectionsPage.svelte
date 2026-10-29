<script lang="ts">
    // The selections: a card per selection with its count and the targets that use it; the
    // editor in a dialog.
    import {Dialog} from 'occulite-ui';
    import {t} from 'occulite-ui/i18n';
    import type {NoticesStore} from 'occulite-ui/notices';

    import ConfirmDialog from './ConfirmDialog.svelte';
    import SelectionEditor from './SelectionEditor.svelte';
    import {del, post, put} from './api.js';
    import {clone, emptySelection, ruleCount, type Inventory, type Match, type Selection, type SelectionView} from './model.js';

    let {notices, selections, inventory, onchanged, onrefresh}: {notices: NoticesStore; selections: SelectionView[]; inventory: Inventory | undefined; onchanged: () => void; onrefresh: () => void} = $props();

    let draft = $state<Selection | undefined>();
    let removing = $state<SelectionView | undefined>();
    let busy = $state(false);
    let error = $state('');

    function open(s?: Selection, copy = false) {
        const d = s ? clone(s) : emptySelection();
        if (copy) {
            d.id = '';
            d.name = t('{name} (copy)', {name: d.name});
        }
        draft = d;
        error = '';
        onrefresh();
    }

    async function save() {
        if (!draft) return;
        busy = true;
        error = '';
        try {
            if (draft.id) await put(`api/v1/selections/${encodeURIComponent(draft.id)}`, draft);
            else await post('api/v1/selections', draft);
            notices.push('info', t('Selection {name} saved', {name: draft.name}));
            draft = undefined;
            onchanged();
        } catch (e) {
            error = (e as Error).message;
        } finally {
            busy = false;
        }
    }

    async function remove() {
        if (!removing) return;
        busy = true;
        try {
            await del(`api/v1/selections/${encodeURIComponent(removing.id)}`);
            notices.push('info', t('Selection {name} removed', {name: removing.name}));
            removing = undefined;
            onchanged();
        } catch (e) {
            notices.push('error', (e as Error).message);
        } finally {
            busy = false;
        }
    }

    const match = (s: Selection) => post<Match>('api/v1/selections/match?n=12', s);

    function describe(s: Selection): string {
        const parts: string[] = [];
        if (s.kinds.length && s.kinds.length < 3) parts.push(s.kinds.map((k) => t('kind.' + k)).join(', '));
        if (s.interfaces.length) parts.push(s.interfaces.join(', '));
        if (s.rooms.length) parts.push(t('Rooms') + ': ' + s.rooms.join(', '));
        if (s.functions.length) parts.push(t('Functions') + ': ' + s.functions.join(', '));
        if (s.device_types.length) parts.push(s.device_types.join(', '));
        if (s.channel_types.length) parts.push(s.channel_types.join(', '));
        if (s.keys.length) parts.push(s.keys.join(', '));
        if (s.exclude_keys.length) parts.push(t('not') + ' ' + s.exclude_keys.join(', '));
        if (s.objects.length) parts.push(t('{n} ticked', {n: s.objects.length}));
        if (s.exclude_objects.length) parts.push(t('{n} excluded', {n: s.exclude_objects.length}));
        return parts.length ? parts.join(' · ') : t('everything');
    }
</script>

<div class="fw-toolbar">
    <button class="hmm-button" type="button" onclick={() => open()}>{t('Add selection')}</button>
    <span class="fw-spacer"></span>
    <span class="fw-muted fw-small">{t('A selection says what is forwarded; every target picks one. Rules follow renames and room changes live.')}</span>
</div>

{#if selections.length === 0}
    <div class="fw-empty">{t('No selections yet. Without one a target receives everything; a selection narrows it by rules, rooms, functions or ticked devices.')}</div>
{/if}

<div class="fw-cards">
    {#each selections as s (s.id)}
        <div class="fw-card">
            <div class="fw-card-head">
                <h3 title={s.name}>{s.name}</h3>
                <span class="fw-muted fw-small">{s.targets ? t('{n} targets', {n: s.targets}) : t('unused')}</span>
            </div>
            <div class="fw-small fw-muted fw-ellipsis" title={describe(s)}>{describe(s)}</div>
            <div class="fw-stats">
                <div class="fw-stat"><b>{s.count.datapoints}</b><span>{t('datapoints')}</span></div>
                <div class="fw-stat"><b>{s.count.channels}</b><span>{t('channels')}</span></div>
                <div class="fw-stat"><b>{s.count.devices}</b><span>{t('devices')}</span></div>
                <div class="fw-stat"><b>{ruleCount(s)}</b><span>{t('rules')}</span></div>
            </div>
            <div class="fw-card-actions">
                <button class="hmm-button" type="button" onclick={() => open(s)}>{t('Edit')}</button>
                <button class="hmm-button" type="button" onclick={() => open(s, true)}>{t('Duplicate')}</button>
                <button class="hmm-button" type="button" onclick={() => (removing = s)} disabled={s.targets > 0} title={s.targets > 0 ? t('Used by: {names}', {names: s.used_by.join(', ')}) : ''}>{t('Delete')}</button>
            </div>
        </div>
    {/each}
</div>

<Dialog open={draft !== undefined} title={draft?.id ? t('Edit selection') : t('New selection')} width="62rem" height="min(88vh, 54rem)" minWidth={720} closable={!busy} onclose={() => (draft = undefined)} testId="selection-editor">
    {#if draft}
        <div class="fw-dialog-body">
            <SelectionEditor bind:value={draft} {inventory} onmatch={match} />
            {#if error}<div class="fw-error">{error}</div>{/if}
        </div>
    {/if}
    {#snippet buttons()}
        <button class="hmm-button" type="button" onclick={() => (draft = undefined)} disabled={busy}>{t('Cancel')}</button>
        <button class="hmm-button" type="button" onclick={() => void save()} disabled={busy || !draft?.name.trim()}>{t('Save')}</button>
    {/snippet}
</Dialog>

<ConfirmDialog
    open={removing !== undefined}
    title={t('Delete selection')}
    message={removing ? t('Delete the selection {name}?', {name: removing.name}) : ''}
    confirmLabel={t('Delete')}
    danger
    {busy}
    onconfirm={() => void remove()}
    oncancel={() => (removing = undefined)}
/>
