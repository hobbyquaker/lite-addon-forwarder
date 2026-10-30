<script lang="ts">
    // The GELF target's form: the receiver (host, port, transport, compression) and the message
    // (a preset, the short and full message, the level, the additional fields - templates).
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import {clone, type Presets, type Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    function applyPreset(name: string) {
        if (!draft.gelf || !presets) return;
        const p = presets.gelf[name];
        if (!p) return;
        draft.gelf.preset = name;
        draft.gelf.short_message = p.short_message;
        draft.gelf.full_message = p.full_message;
        draft.gelf.level = p.level;
        draft.gelf.fields = clone(p.fields);
        draft.gelf.all_fields = p.all_fields;
    }
    function markCustom() {
        if (draft.gelf) draft.gelf.preset = 'custom';
    }
    function addField() {
        draft.gelf?.fields.push({name: '', value: ''});
        markCustom();
    }
    function removeField(i: number) {
        draft.gelf?.fields.splice(i, 1);
        markCustom();
    }
</script>

{#if draft.gelf}
    {#if section === 'connection'}
        <div class="fw-row">
            <label for="fw-ghost">{t('Host')}</label>
            <div class="fw-inline">
                <input id="fw-ghost" class="hmm-input" type="text" bind:value={draft.gelf.host} placeholder="graylog.example" style="width:16em" />
                <label>{t('Port')} <input class="hmm-input" type="number" min="1" max="65535" bind:value={draft.gelf.port} style="width:6em" aria-label={t('Port')} /></label>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Transport')}</span>
            <div>
                <div class="fw-inline">
                    <label><input type="radio" name="fw-gtransport" value="udp" bind:group={draft.gelf.transport} /> UDP</label>
                    <label><input type="radio" name="fw-gtransport" value="tcp" bind:group={draft.gelf.transport} /> TCP</label>
                    <label><input type="radio" name="fw-gtransport" value="tls" bind:group={draft.gelf.transport} /> TLS</label>
                </div>
                <div class="fw-help">{t('UDP sends compressed datagrams, chunked above 1420 bytes; TCP and TLS send each message followed by a null byte, uncompressed. Match the input in Graylog.')}</div>
            </div>
        </div>
        {#if draft.gelf.transport === 'udp'}
            <div class="fw-row">
                <label for="fw-gcomp">{t('Compression')}</label>
                <select id="fw-gcomp" class="hmm-select" bind:value={draft.gelf.compression}>
                    <option value="gzip">gzip</option>
                    <option value="zlib">zlib</option>
                    <option value="none">{t('none')}</option>
                </select>
            </div>
        {/if}
    {:else}
        <div class="fw-row">
            <label for="fw-gpreset">{t('Preset')}</label>
            <div>
                <select id="fw-gpreset" class="hmm-select" value={draft.gelf.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presets?.gelf_order ?? [] as name (name)}
                        <option value={name}>{t('gpreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('preset.custom')}</option>
                </select>
                <div class="fw-help">{t('gpreset.help.' + (draft.gelf.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-gshort">{t('Short message')}</label>
            <TemplateField id="fw-gshort" bind:value={draft.gelf.short_message} selection={draft.selection} label={t('Short message')} oninput={markCustom} />
        </div>
        <div class="fw-row">
            <label for="fw-gfull">{t('Full message')}</label>
            <div>
                <TemplateField id="fw-gfull" bind:value={draft.gelf.full_message} selection={draft.selection} multiline rows={3} label={t('Full message')} placeholder={t('(none)')} oninput={markCustom} />
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-glevel">{t('Level')}</label>
            <div>
                <TemplateField id="fw-glevel" bind:value={draft.gelf.level} selection={draft.selection} label={t('Level')} placeholder={t('(a log line\'s priority, info otherwise)')} oninput={markCustom} />
                <div class="fw-help">{t('A template rendering emerg, alert, crit, err, warning, notice, info or debug (or 0 … 7).')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Additional fields')}</span>
            <div class="fw-tpl-rows">
                <label class="fw-inline"><input type="checkbox" bind:checked={draft.gelf.all_fields} onchange={markCustom} /> {t('every field of the record (_kind, _address, _key, _value, _device_name, …)')}</label>
                {#each draft.gelf.fields as f, i (i)}
                    <div class="fw-tpl-row notype">
                        <input class="hmm-input fw-mono" type="text" bind:value={f.name} oninput={markCustom} placeholder={t('field')} aria-label={t('Field name')} />
                        {#if f.name === '*'}
                            <span class="fw-help">{t('every label of a metric, every field of a log line')}</span>
                        {:else}
                            <TemplateField bind:value={f.value} selection={draft.selection} label={t('Field value')} oninput={markCustom} />
                        {/if}
                        <button class="fw-icon-button" type="button" onclick={() => removeField(i)} title={t('Remove')}>×</button>
                    </div>
                {/each}
                <div><button class="hmm-button" type="button" onclick={addField}>{t('Add field')}</button> <span class="fw-help">{t('Written with a leading underscore; an empty value leaves the field out.')}</span></div>
            </div>
        </div>
    {/if}
{/if}
