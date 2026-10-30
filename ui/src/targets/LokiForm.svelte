<script lang="ts">
    // The Loki target's form: the connection (URL, tenant, authentication, gzip) and the lines
    // (a preset, the stream labels, the line, the structured metadata - templates).
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import {clone, type Presets, type Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    function applyPreset(name: string) {
        if (!draft.loki || !presets) return;
        const p = presets.loki[name];
        if (!p) return;
        draft.loki.preset = name;
        draft.loki.labels = clone(p.labels);
        draft.loki.line = p.line;
        draft.loki.metadata = clone(p.metadata);
    }
    function markCustom() {
        if (draft.loki) draft.loki.preset = 'custom';
    }
    function add(list: 'labels' | 'metadata') {
        draft.loki?.[list].push({name: '', value: ''});
        markCustom();
    }
    function remove(list: 'labels' | 'metadata', i: number) {
        draft.loki?.[list].splice(i, 1);
        markCustom();
    }
</script>

{#if draft.loki}
    {#if section === 'connection'}
        <div class="fw-row">
            <label for="fw-lurl">URL</label>
            <div>
                <input id="fw-lurl" class="hmm-input" type="url" bind:value={draft.loki.url} placeholder="http://loki.example:3100" />
                <div class="fw-help">{t('The server; the push goes to /loki/api/v1/push below it.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-ltenant">{t('Tenant')}</label>
            <div>
                <input id="fw-ltenant" class="hmm-input" type="text" bind:value={draft.loki.tenant_id} placeholder={t('(none)')} autocomplete="off" />
                <div class="fw-help">{t('Sent as X-Scope-OrgID for a multi-tenant Loki or Grafana Cloud.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Authentication')}</span>
            <div class="fw-inline">
                <label><input type="radio" name="fw-lauth" value="none" bind:group={draft.loki.auth} /> {t('none')}</label>
                <label><input type="radio" name="fw-lauth" value="basic" bind:group={draft.loki.auth} /> Basic</label>
                <label><input type="radio" name="fw-lauth" value="bearer" bind:group={draft.loki.auth} /> Bearer</label>
            </div>
        </div>
        {#if draft.loki.auth === 'basic'}
            <div class="fw-row">
                <label for="fw-luser">{t('User')}</label>
                <input id="fw-luser" class="hmm-input" type="text" bind:value={draft.loki.user} autocomplete="off" />
            </div>
        {/if}
        <div class="fw-row">
            <span class="fw-label">{t('Compression')}</span>
            <label class="fw-inline"><input type="checkbox" bind:checked={draft.loki.gzip} /> gzip</label>
        </div>
    {:else}
        <div class="fw-row">
            <label for="fw-lpreset">{t('Preset')}</label>
            <div>
                <select id="fw-lpreset" class="hmm-select" value={draft.loki.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presets?.loki_order ?? [] as name (name)}
                        <option value={name}>{t('lpreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('preset.custom')}</option>
                </select>
                <div class="fw-help">{t('lpreset.help.' + (draft.loki.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Labels')}</span>
            <div class="fw-tpl-rows">
                {#each draft.loki.labels as l, i (i)}
                    <div class="fw-tpl-row notype">
                        <input class="hmm-input fw-mono" type="text" bind:value={l.name} oninput={markCustom} placeholder={t('label')} aria-label={t('Label name')} />
                        <TemplateField bind:value={l.value} selection={draft.selection} label={t('Label value')} oninput={markCustom} />
                        <button class="fw-icon-button" type="button" onclick={() => remove('labels', i)} title={t('Remove')}>×</button>
                    </div>
                {/each}
                <div><button class="hmm-button" type="button" onclick={() => add('labels')}>{t('Add label')}</button> <span class="fw-help">{t('Every distinct label set is a stream in Loki - keep the labels few and coarse (no addresses or values). An empty value leaves the label out.')}</span></div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-lline">{t('Line')}</label>
            <div>
                <TemplateField id="fw-lline" bind:value={draft.loki.line} selection={draft.selection} multiline rows={4} label={t('Line')} oninput={markCustom} />
                <div class="fw-help">{t('The log line: logfmt key=value pairs, or the record as JSON.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Structured metadata')}</span>
            <div class="fw-tpl-rows">
                {#each draft.loki.metadata as m, i (i)}
                    <div class="fw-tpl-row notype">
                        <input class="hmm-input fw-mono" type="text" bind:value={m.name} oninput={markCustom} placeholder={t('name')} aria-label={t('Metadata name')} />
                        {#if m.name === '*'}
                            <span class="fw-help">{t('every label of a metric, every field of a log line')}</span>
                        {:else}
                            <TemplateField bind:value={m.value} selection={draft.selection} label={t('Metadata value')} oninput={markCustom} />
                        {/if}
                        <button class="fw-icon-button" type="button" onclick={() => remove('metadata', i)} title={t('Remove')}>×</button>
                    </div>
                {/each}
                <div><button class="hmm-button" type="button" onclick={() => add('metadata')}>{t('Add metadata')}</button> <span class="fw-help">{t('Per entry, not indexed as a stream (Loki 3); remove them for an older Loki.')}</span></div>
            </div>
        </div>
    {/if}
{/if}
