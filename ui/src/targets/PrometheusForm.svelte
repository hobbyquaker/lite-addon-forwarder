<script lang="ts">
    // The Prometheus target's form: the mode (a scrape endpoint the system serves, or
    // remote_write), the endpoint's URL or the push URL and authentication, and the metric
    // name and labels as templates with a preset.
    import {t} from 'occulite-ui/i18n';

    import TemplateField from '../TemplateField.svelte';
    import {clone, type Presets, type Target} from '../model.js';

    let {draft = $bindable(), section, presets}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined} = $props();

    const scrapeURL = $derived(draft.id ? new URL(`metrics/${encodeURIComponent(draft.id)}`, document.baseURI).toString() : '');

    function applyPreset(name: string) {
        if (!draft.prometheus || !presets) return;
        const p = presets.prometheus[name];
        if (!p) return;
        draft.prometheus.preset = name;
        draft.prometheus.name = p.name;
        draft.prometheus.labels = clone(p.labels);
    }
    function markCustom() {
        if (draft.prometheus) draft.prometheus.preset = 'custom';
    }
    function addLabel() {
        draft.prometheus?.labels.push({name: '', value: ''});
        markCustom();
    }
    function removeLabel(i: number) {
        draft.prometheus?.labels.splice(i, 1);
        markCustom();
    }
</script>

{#if draft.prometheus}
    {#if section === 'connection'}
        <div class="fw-row">
            <span class="fw-label">{t('Mode')}</span>
            <div class="fw-checks">
                <label><input type="radio" name="fw-pmode" value="scrape" bind:group={draft.prometheus.mode} /> {t('scrape: the system serves an endpoint Prometheus reads')}</label>
                <label><input type="radio" name="fw-pmode" value="remote_write" bind:group={draft.prometheus.mode} /> {t('remote_write: the addon pushes to Prometheus, VictoriaMetrics, Mimir, Thanos or Grafana Cloud')}</label>
            </div>
        </div>
        {#if draft.prometheus.mode === 'scrape'}
            <div class="fw-row">
                <span class="fw-label">{t('Endpoint')}</span>
                <div>
                    {#if scrapeURL}
                        <code class="fw-mono fw-scrape-url">{scrapeURL}</code>
                    {:else}
                        <span class="fw-muted">{t('(shown after saving)')}</span>
                    {/if}
                    <div class="fw-help">{t('Prometheus scrapes it with the token below as a Bearer credential (authorization: credentials in its job) or as ?token=. The newest value of every series is answered, the forwarder\'s own figures appended.')}</div>
                </div>
            </div>
            <div class="fw-row">
                <label for="fw-pstale">{t('Staleness')}</label>
                <div class="fw-inline">
                    <input id="fw-pstale" class="hmm-input" type="number" min="0" max="604800" bind:value={draft.prometheus.stale_s} style="width:7em" /> s
                    <span class="fw-help">{t('a series without a new record for this long is left out; 0 keeps every series')}</span>
                </div>
            </div>
        {:else}
            <div class="fw-row">
                <label for="fw-purl">URL</label>
                <div>
                    <input id="fw-purl" class="hmm-input" type="url" bind:value={draft.prometheus.url} placeholder="http://prometheus.example:9090/api/v1/write" />
                    <div class="fw-help">{t('The remote write endpoint: /api/v1/write on Prometheus (started with --web.enable-remote-write-receiver) and VictoriaMetrics, /api/v1/push on Mimir.')}</div>
                </div>
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Authentication')}</span>
                <div class="fw-inline">
                    <label><input type="radio" name="fw-pauth" value="none" bind:group={draft.prometheus.auth} /> {t('none')}</label>
                    <label><input type="radio" name="fw-pauth" value="basic" bind:group={draft.prometheus.auth} /> Basic</label>
                    <label><input type="radio" name="fw-pauth" value="bearer" bind:group={draft.prometheus.auth} /> Bearer</label>
                </div>
            </div>
            {#if draft.prometheus.auth === 'basic'}
                <div class="fw-row">
                    <label for="fw-puser">{t('User')}</label>
                    <input id="fw-puser" class="hmm-input" type="text" bind:value={draft.prometheus.user} autocomplete="off" />
                </div>
            {/if}
        {/if}
    {:else}
        <div class="fw-row">
            <label for="fw-ppreset">{t('Preset')}</label>
            <div>
                <select id="fw-ppreset" class="hmm-select" value={draft.prometheus.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presets?.prometheus_order ?? [] as name (name)}
                        <option value={name}>{t('ppreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('preset.custom')}</option>
                </select>
                <div class="fw-help">{t('ppreset.help.' + (draft.prometheus.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-pname">{t('Metric name')}</label>
            <div>
                <TemplateField id="fw-pname" bind:value={draft.prometheus.name} selection={draft.selection} label={t('Metric name')} oninput={markCustom} />
                <div class="fw-help">{t('Characters Prometheus does not allow become _; only records with a number are exported.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Labels')}</span>
            <div class="fw-tpl-rows">
                {#each draft.prometheus.labels as l, i (i)}
                    <div class="fw-tpl-row notype">
                        <input class="hmm-input fw-mono" type="text" bind:value={l.name} oninput={markCustom} placeholder={t('label')} aria-label={t('Label name')} />
                        {#if l.name === '*'}
                            <span class="fw-help">{t('every label of a metric')}</span>
                        {:else}
                            <TemplateField bind:value={l.value} selection={draft.selection} label={t('Label value')} oninput={markCustom} />
                        {/if}
                        <button class="fw-icon-button" type="button" onclick={() => removeLabel(i)} title={t('Remove')}>×</button>
                    </div>
                {/each}
                <div><button class="hmm-button" type="button" onclick={addLabel}>{t('Add label')}</button> <span class="fw-help">{t('An empty label value leaves the label out; a label named * stands for every label of a metric.')}</span></div>
            </div>
        </div>
    {/if}
{/if}
