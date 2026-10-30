<script lang="ts">
    // The syslog target's form: the receiver (host, port, transport, framing), the facility and
    // host name, and the message (a preset, severity, app name and msgid, the structured data
    // element with its parameters, the message text - all templates).
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import {clone, FACILITIES, type Presets, type Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    function applyPreset(name: string) {
        if (!draft.syslog || !presets) return;
        const p = presets.syslog[name];
        if (!p) return;
        draft.syslog.preset = name;
        draft.syslog.severity = p.severity;
        draft.syslog.app_name = p.app_name;
        draft.syslog.msg_id = p.msg_id;
        draft.syslog.sd_id = p.sd_id;
        draft.syslog.sd_params = clone(p.sd_params);
        draft.syslog.message = p.message;
    }
    function markCustom() {
        if (draft.syslog) draft.syslog.preset = 'custom';
    }
    function addParam() {
        draft.syslog?.sd_params.push({name: '', value: ''});
        markCustom();
    }
    function removeParam(i: number) {
        draft.syslog?.sd_params.splice(i, 1);
        markCustom();
    }
</script>

{#if draft.syslog}
    {#if section === 'connection'}
        <div class="fw-row">
            <label for="fw-yhost">{t('Host')}</label>
            <div class="fw-inline">
                <input id="fw-yhost" class="hmm-input" type="text" bind:value={draft.syslog.host} placeholder="syslog.example" style="width:16em" />
                <label>{t('Port')} <input class="hmm-input" type="number" min="1" max="65535" bind:value={draft.syslog.port} style="width:6em" aria-label={t('Port')} /></label>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Transport')}</span>
            <div>
                <div class="fw-inline">
                    <label><input type="radio" name="fw-ytransport" value="udp" bind:group={draft.syslog.transport} /> UDP</label>
                    <label><input type="radio" name="fw-ytransport" value="tcp" bind:group={draft.syslog.transport} /> TCP</label>
                    <label><input type="radio" name="fw-ytransport" value="tls" bind:group={draft.syslog.transport} /> TLS</label>
                </div>
                <div class="fw-help">{t('UDP is not confirmed and loses messages under load; TCP and TLS keep a connection. The TLS tab holds the certificate settings.')}</div>
            </div>
        </div>
        {#if draft.syslog.transport !== 'udp'}
            <div class="fw-row">
                <label for="fw-yframing">{t('Framing')}</label>
                <div>
                    <select id="fw-yframing" class="hmm-select" bind:value={draft.syslog.framing}>
                        <option value="octet">{t('octet counting (RFC 6587)')}</option>
                        <option value="newline">{t('one message per line')}</option>
                    </select>
                    <div class="fw-help">{t('rsyslog and syslog-ng take both; octet counting keeps a message with line breaks whole.')}</div>
                </div>
            </div>
        {/if}
        <div class="fw-row">
            <label for="fw-yfacility">{t('Facility')}</label>
            <select id="fw-yfacility" class="hmm-select" bind:value={draft.syslog.facility}>
                {#each FACILITIES as f, i (f)}
                    <option value={i}>{i} {f}</option>
                {/each}
            </select>
        </div>
        <div class="fw-row">
            <label for="fw-yhostname">{t('Host name')}</label>
            <div>
                <input id="fw-yhostname" class="hmm-input" type="text" bind:value={draft.syslog.hostname} placeholder={t('(the system\'s)')} />
                <div class="fw-help">{t('The HOSTNAME field of every message.')}</div>
            </div>
        </div>
    {:else}
        <div class="fw-row">
            <label for="fw-ypreset">{t('Preset')}</label>
            <div>
                <select id="fw-ypreset" class="hmm-select" value={draft.syslog.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presets?.syslog_order ?? [] as name (name)}
                        <option value={name}>{t('ypreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('preset.custom')}</option>
                </select>
                <div class="fw-help">{t('ypreset.help.' + (draft.syslog.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-ymsg">{t('Message')}</label>
            <div>
                <TemplateField id="fw-ymsg" bind:value={draft.syslog.message} selection={draft.selection} multiline rows={3} label={t('Message')} oninput={markCustom} />
                <div class="fw-help">{t('The MSG part; line breaks become spaces.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-yseverity">{t('Severity')}</label>
            <div>
                <TemplateField id="fw-yseverity" bind:value={draft.syslog.severity} selection={draft.selection} label={t('Severity')} placeholder={t('(a log line\'s priority, info otherwise)')} oninput={markCustom} />
                <div class="fw-help">{t('A template rendering emerg, alert, crit, err, warning, notice, info or debug (or 0 … 7).')}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-yapp">APP-NAME</label>
            <div class="fw-inline">
                <TemplateField id="fw-yapp" bind:value={draft.syslog.app_name} selection={draft.selection} label="APP-NAME" oninput={markCustom} />
                <label>MSGID <TemplateField bind:value={draft.syslog.msg_id} selection={draft.selection} label="MSGID" oninput={markCustom} /></label>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-ysdid">{t('Structured data')}</label>
            <div>
                <div class="fw-inline"><input id="fw-ysdid" class="hmm-input fw-mono" type="text" bind:value={draft.syslog.sd_id} oninput={markCustom} placeholder={t('(none)')} style="width:16em" /> <span class="fw-help">{t('the SD-ID (name@enterprise number); empty sends none')}</span></div>
                {#if draft.syslog.sd_id}
                    <div class="fw-tpl-rows">
                        {#each draft.syslog.sd_params as p, i (i)}
                            <div class="fw-tpl-row notype">
                                <input class="hmm-input fw-mono" type="text" bind:value={p.name} oninput={markCustom} placeholder={t('parameter')} aria-label={t('Parameter name')} />
                                {#if p.name === '*'}
                                    <span class="fw-help">{t('every label of a metric, every field of a log line')}</span>
                                {:else}
                                    <TemplateField bind:value={p.value} selection={draft.selection} label={t('Parameter value')} oninput={markCustom} />
                                {/if}
                                <button class="fw-icon-button" type="button" onclick={() => removeParam(i)} title={t('Remove')}>×</button>
                            </div>
                        {/each}
                        <div><button class="hmm-button" type="button" onclick={addParam}>{t('Add parameter')}</button> <span class="fw-help">{t('An empty value leaves the parameter out.')}</span></div>
                    </div>
                {/if}
            </div>
        </div>
    {/if}
{/if}
