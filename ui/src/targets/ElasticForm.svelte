<script lang="ts">
    // The Elasticsearch/OpenSearch target's form: the connection (URL, authentication, ingest
    // pipeline, gzip, the index template) and the documents (a preset, the index name and the
    // document as templates, the action).
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import type {Presets, Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    function applyPreset(name: string) {
        if (!draft.elastic || !presets) return;
        const p = presets.elastic[name];
        if (!p) return;
        draft.elastic.preset = name;
        draft.elastic.index = p.index;
        draft.elastic.document = p.document;
        draft.elastic.timestamp = p.timestamp;
    }
    function markCustom() {
        if (draft.elastic) draft.elastic.preset = 'custom';
    }
</script>

{#if draft.elastic}
    {#if section === 'connection'}
        <div class="fw-row">
            <label for="fw-eurl">URL</label>
            <div>
                <input id="fw-eurl" class="hmm-input" type="url" bind:value={draft.elastic.url} placeholder="https://elastic.example:9200" />
                <div class="fw-help">{t('The server without a path; Elasticsearch 7 and 8 and OpenSearch 1 and 2 take the same bulk write.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Authentication')}</span>
            <div class="fw-inline">
                <label><input type="radio" name="fw-eauth" value="none" bind:group={draft.elastic.auth} /> {t('none')}</label>
                <label><input type="radio" name="fw-eauth" value="basic" bind:group={draft.elastic.auth} /> Basic</label>
                <label><input type="radio" name="fw-eauth" value="apikey" bind:group={draft.elastic.auth} /> API key</label>
                <label><input type="radio" name="fw-eauth" value="bearer" bind:group={draft.elastic.auth} /> Bearer</label>
            </div>
        </div>
        {#if draft.elastic.auth === 'basic'}
            <div class="fw-row">
                <label for="fw-euser">{t('User')}</label>
                <input id="fw-euser" class="hmm-input" type="text" bind:value={draft.elastic.user} autocomplete="off" />
            </div>
        {/if}
        <div class="fw-row">
            <label for="fw-epipe">{t('Ingest pipeline')}</label>
            <div>
                <input id="fw-epipe" class="hmm-input" type="text" bind:value={draft.elastic.pipeline} placeholder={t('(none)')} />
                <div class="fw-help">{t('Optional: a pipeline of the store that every document passes.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Index template')}</span>
            <div>
                <label class="fw-inline"><input type="checkbox" bind:checked={draft.elastic.install_template} /> {t('install one on the first connection for')} <input class="hmm-input fw-mono" type="text" bind:value={draft.elastic.template_pattern} style="width:12em" aria-label={t('Index pattern')} disabled={!draft.elastic.install_template} /></label>
                <div class="fw-help">{t('Maps the record\'s fields with fixed types (value as keyword, value_num as double, ts and @timestamp as date), so a value that is a number on one datapoint and a text on the next cannot break the mapping.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Compression')}</span>
            <label class="fw-inline"><input type="checkbox" bind:checked={draft.elastic.gzip} /> gzip</label>
        </div>
    {:else}
        <div class="fw-row">
            <label for="fw-epreset">{t('Preset')}</label>
            <div>
                <select id="fw-epreset" class="hmm-select" value={draft.elastic.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presets?.elastic_order ?? [] as name (name)}
                        <option value={name}>{t('epreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('preset.custom')}</option>
                </select>
                <div class="fw-help">{t('epreset.help.' + (draft.elastic.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-eindex">{t('Index')}</label>
            <div>
                <TemplateField id="fw-eindex" bind:value={draft.elastic.index} selection={draft.selection} label={t('Index')} oninput={markCustom} />
                <div class="fw-help">{t('A template per record, lower-cased: a daily index, one per kind, or a data stream.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-eaction">{t('Action')}</label>
            <div class="fw-inline">
                <select id="fw-eaction" class="hmm-select" bind:value={draft.elastic.action}>
                    <option value="index">index ({t('an index')})</option>
                    <option value="create">create ({t('a data stream')})</option>
                </select>
                <label><input type="checkbox" bind:checked={draft.elastic.timestamp} onchange={markCustom} /> {t('add @timestamp')}</label>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-edoc">{t('Document')}</label>
            <div>
                <TemplateField id="fw-edoc" bind:value={draft.elastic.document} selection={draft.selection} multiline rows={6} label={t('Document')} oninput={markCustom} />
                <div class="fw-help">{t('A template that renders one JSON object; the default is the whole record.')}</div>
            </div>
        </div>
    {/if}
{/if}
