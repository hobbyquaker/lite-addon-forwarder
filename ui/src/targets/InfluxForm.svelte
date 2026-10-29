<script lang="ts">
    // The InfluxDB target's form: the connection (API version, URL, database or org and bucket,
    // gzip) and the schema (a preset, then the measurement, tags and fields per record kind as
    // templates with live previews).
    import {Tabs} from 'occulite-ui';
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import {clone, type FieldTemplate, type InfluxTemplates, type Presets, type Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    let kindTab = $state<'event' | 'metric' | 'log'>('event');
    const kindTabs = $derived([
        {id: 'event', label: t('Events')},
        {id: 'metric', label: t('Metrics')},
        {id: 'log', label: t('Logs')},
    ]);
    const fieldTypes: FieldTemplate['type'][] = ['auto', 'float', 'integer', 'boolean', 'string'];

    function applyPreset(name: string) {
        if (!draft.influx || !presets) return;
        const p = presets.influx[name];
        if (!p) return;
        draft.influx.preset = name;
        draft.influx.event = clone(p.event);
        draft.influx.metric = clone(p.metric);
        draft.influx.log = clone(p.log);
    }
    function markCustom() {
        if (draft.influx) draft.influx.preset = 'custom';
    }
    function tpls(): InfluxTemplates | undefined {
        return draft.influx?.[kindTab];
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
</script>

{#if draft.influx}
    {#if section === 'connection'}
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
    {:else}
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
                            {#if tag.name === '*'}
                                <span class="fw-help">{t('every label of a metric as a tag')}</span>
                            {:else}
                                <TemplateField bind:value={tag.value} selection={draft.selection} label={t('Tag value')} />
                            {/if}
                            <button class="fw-icon-button" type="button" onclick={() => removeTag(i)} title={t('Remove')}>×</button>
                        </div>
                    {/each}
                    <div><button class="hmm-button" type="button" onclick={addTag}>{t('Add tag')}</button> <span class="fw-help">{t('An empty tag value leaves the tag out; a tag named * stands for every label of a metric.')}</span></div>
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
    {/if}
{/if}
