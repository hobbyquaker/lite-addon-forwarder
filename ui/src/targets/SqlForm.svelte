<script lang="ts">
    // The SQL target's form: the connection (dialect, host, port, database, user, TLS mode) and
    // the statements (a preset per dialect, the schema script run once per connection, the
    // INSERT with :name placeholders in the editor with completion).
    import {i18n, t} from 'occulite-ui/i18n';

    import SqlEditor from '../SqlEditor.svelte';
    import type {Presets, SqlDialect, Target, TemplateHelp} from '../model.js';

    let {draft = $bindable(), section, presets, help}: {draft: Target; section: 'connection' | 'templates'; presets: Presets | undefined; help: TemplateHelp | undefined} = $props();

    const lang = $derived(i18n.language === 'de' ? 'de' : 'en');
    const placeholders = $derived(help?.placeholders ?? help?.variables.map((v) => v.name) ?? []);
    const docs = $derived(Object.fromEntries((help?.variables ?? []).map((v) => [v.name, v.doc[lang] ?? ''])) as Record<string, string>);
    const presetNames = $derived((presets?.sql_order ?? []).filter((n) => draft.sql?.dialect === 'postgres' || !presets?.sql[n]?.timescale_only));

    function applyPreset(name: string) {
        if (!draft.sql || !presets) return;
        const p = presets.sql[name];
        if (!p) return;
        const st = draft.sql.dialect === 'mysql' ? p.mysql : p.postgres;
        draft.sql.preset = name;
        draft.sql.insert = st.insert;
        draft.sql.schema = st.schema;
    }
    function markCustom() {
        if (draft.sql) draft.sql.preset = 'custom';
    }
    function setDialect(d: SqlDialect) {
        if (!draft.sql) return;
        const was = draft.sql.dialect;
        draft.sql.dialect = d;
        if (draft.sql.port === (was === 'mysql' ? 3306 : 5432) || !draft.sql.port) draft.sql.port = d === 'mysql' ? 3306 : 5432;
        // the statements follow the dialect while they are still a preset's
        if (draft.sql.preset && draft.sql.preset !== 'custom') {
            if (d === 'mysql' && presets?.sql[draft.sql.preset]?.timescale_only) draft.sql.preset = 'wide';
            applyPreset(draft.sql.preset);
        }
    }
</script>

{#if draft.sql}
    {#if section === 'connection'}
        <div class="fw-row">
            <span class="fw-label">{t('Database system')}</span>
            <div class="fw-inline">
                <label><input type="radio" name="fw-qdialect" value="postgres" checked={draft.sql.dialect === 'postgres'} onchange={() => setDialect('postgres')} /> PostgreSQL / TimescaleDB</label>
                <label><input type="radio" name="fw-qdialect" value="mysql" checked={draft.sql.dialect === 'mysql'} onchange={() => setDialect('mysql')} /> MySQL / MariaDB</label>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-qhost">{t('Host')}</label>
            <div class="fw-inline">
                <input id="fw-qhost" class="hmm-input" type="text" bind:value={draft.sql.host} placeholder="db.example" style="width:16em" />
                <label>{t('Port')} <input class="hmm-input" type="number" min="1" max="65535" bind:value={draft.sql.port} style="width:6em" aria-label={t('Port')} /></label>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-qdb">{t('Database')}</label>
            <input id="fw-qdb" class="hmm-input" type="text" bind:value={draft.sql.database} placeholder="homematic" autocomplete="off" />
        </div>
        <div class="fw-row">
            <label for="fw-quser">{t('User')}</label>
            <input id="fw-quser" class="hmm-input" type="text" bind:value={draft.sql.user} autocomplete="off" />
        </div>
        <div class="fw-row">
            <label for="fw-qtls">{t('Encryption')}</label>
            <div>
                <select id="fw-qtls" class="hmm-select" bind:value={draft.sql.tls_mode}>
                    <option value="disable">{t('none (plain connection)')}</option>
                    <option value="require">{t('TLS, certificate not checked')}</option>
                    <option value="verify">{t('TLS, certificate checked')}</option>
                </select>
                <div class="fw-help">{t('With a check the server\'s certificate must match the host; an own CA goes into the TLS tab.')}</div>
            </div>
        </div>
    {:else}
        <div class="fw-row">
            <label for="fw-qpreset">{t('Preset')}</label>
            <div>
                <select id="fw-qpreset" class="hmm-select" value={draft.sql.preset} onchange={(e) => applyPreset((e.currentTarget as HTMLSelectElement).value)}>
                    {#each presetNames as name (name)}
                        <option value={name}>{t('qpreset.' + name)}</option>
                    {/each}
                    <option value="custom">{t('qpreset.custom')}</option>
                </select>
                <div class="fw-help">{t('qpreset.help.' + (draft.sql.preset || 'custom'))}</div>
            </div>
        </div>
        <div class="fw-row">
            <label for="fw-qinsert">INSERT</label>
            <div>
                <SqlEditor id="fw-qinsert" bind:value={draft.sql.insert} dialect={draft.sql.dialect} {placeholders} {docs} rows={10} label="INSERT" oninput={markCustom} />
                <div class="fw-help">{t('Run once per record of a batch, inside one transaction. The :name placeholders are the record\'s fields and go in as bound parameters - a value can never change the statement. Type : for the list; lists and dicts arrive as JSON text.')}</div>
            </div>
        </div>
        <div class="fw-row">
            <span class="fw-label">{t('Schema')}</span>
            <div>
                <label class="fw-inline"><input type="checkbox" bind:checked={draft.sql.install_schema} onchange={markCustom} /> {t('run this script once per connection, before the first batch')}</label>
                <SqlEditor id="fw-qschema" bind:value={draft.sql.schema} dialect={draft.sql.dialect} placeholders={[]} rows={8} label={t('Schema')} oninput={markCustom} />
                <div class="fw-help">{t('CREATE TABLE IF NOT EXISTS and the like, several statements separated by ;. Test runs it in a transaction that is rolled back (MySQL commits DDL by itself).')}</div>
            </div>
        </div>
    {/if}
{/if}
