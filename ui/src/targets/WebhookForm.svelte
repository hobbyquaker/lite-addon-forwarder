<script lang="ts">
    // The webhook target's form: the URL template, method, authentication and headers, and the
    // body (format, a preset, the body template, the content type).
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import type {Presets, Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    // the headers are edited as rows and written back to the map on every change
    let headerRows = $state<{k: string; v: string}[]>(Object.entries(draft.webhook?.headers ?? {}).map(([k, v]) => ({k, v})));
    function syncHeaders() {
        if (!draft.webhook) return;
        const h: Record<string, string> = {};
        for (const r of headerRows) if (r.k.trim()) h[r.k.trim()] = r.v;
        draft.webhook.headers = h;
    }

    function applyPreset(name: string) {
        if (!draft.webhook || !presets) return;
        const p = presets.webhook[name];
        if (!p) return;
        draft.webhook.body = p.body;
        draft.webhook.format = p.format;
    }
</script>

{#if draft.webhook}
    {#if section === 'connection'}
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
                        <input class="hmm-input fw-mono" type="text" bind:value={h.k} oninput={syncHeaders} placeholder="X-Header" aria-label={t('Header name')} />
                        <input class="hmm-input fw-mono" type="text" bind:value={h.v} oninput={syncHeaders} placeholder={t('value')} aria-label={t('Header value')} />
                        <button class="fw-icon-button" type="button" onclick={() => { headerRows.splice(i, 1); syncHeaders(); }} title={t('Remove')}>×</button>
                    </div>
                {/each}
                <div><button class="hmm-button" type="button" onclick={() => headerRows.push({k: '', v: ''})}>{t('Add header')}</button></div>
            </div>
        </div>
    {:else}
        <div class="fw-row">
            <label for="fw-format">{t('Format')}</label>
            <div class="fw-inline">
                <select id="fw-format" class="hmm-select" bind:value={draft.webhook.format}>
                    <option value="json">JSON ({t('an array per batch')})</option>
                    <option value="ndjson">NDJSON ({t('one line per record')})</option>
                </select>
                <select class="hmm-select" value="" onchange={(e) => { applyPreset((e.currentTarget as HTMLSelectElement).value); (e.currentTarget as HTMLSelectElement).value = ''; }} aria-label={t('Preset')}>
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
{/if}
