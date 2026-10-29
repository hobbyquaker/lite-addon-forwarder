<script lang="ts">
    // The selection editor, a standalone component: the kinds, the rules (interfaces, device
    // types, channel types, datapoint keys with wildcards, rooms, functions, sources), the
    // device tree with ticks and exclusions, and the live count and preview of what matches.
    // It knows the inventory (props) and asks `onmatch` for the count, so another addon with
    // the same selection model can host it with its own backend.
    import {Tabs} from 'occulite-ui';
    import {i18n, t} from 'occulite-ui/i18n';

    import ChipInput from './ChipInput.svelte';
    import {formatValue, ruleCount, type Inventory, type Match, type Selection} from './model.js';

    let {
        value = $bindable(),
        inventory,
        onmatch,
        metricGroups = [],
        logStreams = [],
    }: {value: Selection; inventory: Inventory | undefined; onmatch: (s: Selection) => Promise<Match>; metricGroups?: string[]; logStreams?: string[]} = $props();

    let tab = $state('rules');
    let search = $state('');
    let expanded = $state<Record<string, boolean>>({});
    let match = $state<Match | undefined>();
    let matchError = $state('');
    let timer: ReturnType<typeof setTimeout> | undefined;
    let seq = 0;

    // the live count: debounced on every change of the selection
    $effect(() => {
        const snapshot = JSON.stringify(value);
        if (timer) clearTimeout(timer);
        const mine = ++seq;
        timer = setTimeout(() => {
            onmatch(JSON.parse(snapshot) as Selection)
                .then((m) => {
                    if (mine === seq) {
                        match = m;
                        matchError = '';
                    }
                })
                .catch((e) => {
                    if (mine === seq) matchError = (e as Error).message;
                });
        }, 300);
        return () => {
            if (timer) clearTimeout(timer);
        };
    });

    const tabs = $derived([
        {id: 'rules', label: t('Rules'), badge: ruleCount(value) || undefined},
        {id: 'devices', label: t('Devices and channels'), badge: value.objects.length + value.exclude_objects.length || undefined},
    ]);

    function toggleKind(k: 'event' | 'metric' | 'log') {
        value.kinds = value.kinds.includes(k) ? value.kinds.filter((x) => x !== k) : [...value.kinds, k];
    }
    const hasKind = (k: 'event' | 'metric' | 'log') => value.kinds.length === 0 || value.kinds.includes(k);
    const SOURCES = ['event', 'snapshot', 'sweep'];
    const lang = $derived(i18n.language === 'de' ? 'de' : 'en');
    // the metric groups the backend documents, by source; the prop is the fallback
    const groups = $derived(inventory?.metric_groups ?? metricGroups.map((name) => ({name, source: 'host' as const, title: {de: name, en: name}, doc: {de: '', en: ''}, metrics: []})));
    const groupSources: {source: 'homematic' | 'host'; label: string}[] = [
        {source: 'homematic', label: 'Homematic statistics'},
        {source: 'host', label: 'System metrics'},
    ];
    function toggleGroup(name: string, on: boolean) {
        value.metric_groups = on ? [...new Set([...value.metric_groups, name])] : value.metric_groups.filter((x) => x !== name);
    }
    function tickGroups(source: string, on: boolean) {
        const names = groups.filter((g) => g.source === source).map((g) => g.name);
        if (on) value.metric_groups = [...new Set([...value.metric_groups, ...names])];
        else {
            const set = new Set(names);
            value.metric_groups = value.metric_groups.filter((x) => !set.has(x));
        }
    }

    const counts = $derived.by(() => {
        const m = new Map<string, number>();
        for (const c of inventory?.keys ?? []) m.set(c.name, c.count);
        for (const c of inventory?.device_types ?? []) m.set(c.name, c.count);
        for (const c of inventory?.channel_types ?? []) m.set(c.name, c.count);
        return m;
    });

    // the device tree
    const devices = $derived.by(() => {
        const q = search.trim().toLowerCase();
        const list = inventory?.devices ?? [];
        if (!q) return list;
        return list.filter((d) => [d.name, d.address, d.type, d.interface, ...d.rooms, ...d.channels.map((c) => c.name + ' ' + c.address + ' ' + c.type)].join(' ').toLowerCase().includes(q));
    });

    function ref(iface: string, address: string): string {
        return `${iface}/${address}`;
    }
    const ticked = (r: string) => value.objects.includes(r);
    const excluded = (r: string) => value.exclude_objects.includes(r);

    function tick(r: string, on: boolean) {
        value.objects = on ? [...new Set([...value.objects, r])] : value.objects.filter((x) => x !== r);
        if (on) value.exclude_objects = value.exclude_objects.filter((x) => x !== r);
    }
    function exclude(r: string, on: boolean) {
        value.exclude_objects = on ? [...new Set([...value.exclude_objects, r])] : value.exclude_objects.filter((x) => x !== r);
        if (on) value.objects = value.objects.filter((x) => x !== r);
    }
    function tickAllVisible(on: boolean) {
        const refs = devices.map((d) => ref(d.interface, d.address));
        if (on) value.objects = [...new Set([...value.objects, ...refs])];
        else {
            const set = new Set(refs);
            value.objects = value.objects.filter((x) => !set.has(x));
        }
    }
    /** A channel of a ticked device counts as ticked unless excluded. */
    function channelState(iface: string, dev: string, ch: string): 'on' | 'inherited' | 'off' {
        const r = ref(iface, ch);
        if (ticked(r)) return 'on';
        if (ticked(ref(iface, dev)) && !excluded(r)) return 'inherited';
        return 'off';
    }
</script>

<div class="fw-form">
    <div class="fw-row">
        <label for="fw-sel-name">{t('Name')}</label>
        <input id="fw-sel-name" class="hmm-input" type="text" bind:value={value.name} placeholder={t('e.g. temperatures, ground floor')} />
    </div>
    <div class="fw-row">
        <span class="fw-label">{t('Kinds')}</span>
        <div class="fw-checks">
            <label><input type="checkbox" checked={value.kinds.includes('event')} onchange={() => toggleKind('event')} /> {t('Homematic events')}</label>
            <label><input type="checkbox" checked={value.kinds.includes('metric')} onchange={() => toggleKind('metric')} /> {t('System metrics')}</label>
            <label><input type="checkbox" checked={value.kinds.includes('log')} onchange={() => toggleKind('log')} /> {t('Logs')}</label>
            {#if value.kinds.length === 0}<span class="fw-help">{t('none ticked: every kind')}</span>{/if}
        </div>
    </div>
</div>

<div class="fw-match">
    <div class="fw-match-count">
        {#if match}
            <span><b>{match.count.datapoints}</b> {t('datapoints')}</span>
            <span><b>{match.count.channels}</b> {t('channels')}</span>
            <span><b>{match.count.devices}</b> {t('devices')}</span>
            <span class="fw-muted fw-small">{t('of {n} on the system', {n: inventory?.datapoints ?? 0})}</span>
        {:else if matchError}
            <span class="fw-error">{matchError}</span>
        {:else}
            <span class="fw-muted">{t('Counting…')}</span>
        {/if}
        <span class="fw-spacer"></span>
        <span class="fw-muted fw-small">{t('Within a rule any entry matches; across rules all must; empty rules do not restrict.')}</span>
    </div>
    <pre class="fw-preview">{#if match}{#each match.recent as r (r.ts + (r.address ?? '') + (r.key ?? ''))}{r.ts.slice(11, 19)}  {r.summary}{#if r.channel_name}  ({r.channel_name}{r.rooms.length ? ' · ' + r.rooms.join(',') : ''}){/if}{#if r.value !== undefined && r.kind === 'event'}  = {formatValue(r.value)}{r.unit ? ' ' + r.unit : ''}{/if}
{:else}{t('No recent record matches yet.')}{/each}{/if}</pre>
</div>

<div class="fw-dialog-tabs"><Tabs {tabs} bind:active={tab} /></div>

{#if tab === 'rules'}
    <div class="fw-form">
        {#if hasKind('event')}
            <div class="fw-row">
                <span class="fw-label">{t('Interfaces')}</span>
                <ChipInput bind:values={value.interfaces} suggestions={(inventory?.interfaces ?? []).map((i) => i.name)} allowFree={false} label={t('Interfaces')} placeholder={t('all')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Device types')}</span>
                <ChipInput bind:values={value.device_types} suggestions={(inventory?.device_types ?? []).map((c) => c.name)} {counts} label={t('Device types')} placeholder={t('all')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Channel types')}</span>
                <ChipInput bind:values={value.channel_types} suggestions={(inventory?.channel_types ?? []).map((c) => c.name)} {counts} label={t('Channel types')} placeholder={t('all')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Datapoints')}</span>
                <div>
                    <ChipInput bind:values={value.keys} suggestions={(inventory?.keys ?? []).map((c) => c.name)} {counts} label={t('Datapoints')} placeholder={t('all; wildcards: *_TEMPERATURE, PRESS_*')} />
                </div>
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Exclude datapoints')}</span>
                <div>
                    <ChipInput bind:values={value.exclude_keys} suggestions={(inventory?.keys ?? []).map((c) => c.name)} {counts} label={t('Exclude datapoints')} placeholder={t('none; e.g. RSSI_*, PRESS_*')} />
                </div>
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Rooms')}</span>
                <ChipInput bind:values={value.rooms} suggestions={inventory?.rooms ?? []} allowFree={false} label={t('Rooms')} placeholder={t('all')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Functions')}</span>
                <ChipInput bind:values={value.functions} suggestions={inventory?.functions ?? []} allowFree={false} label={t('Functions')} placeholder={t('all')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Sources')}</span>
                <div>
                    <div class="fw-checks">
                        {#each SOURCES as s (s)}
                            <label><input type="checkbox" checked={value.sources.includes(s)} onchange={() => (value.sources = value.sources.includes(s) ? value.sources.filter((x) => x !== s) : [...value.sources, s])} /> {t('source.' + s)}</label>
                        {/each}
                    </div>
                    <div class="fw-help">{t('source.help')}</div>
                </div>
            </div>
        {/if}
        {#if hasKind('metric')}
            <div class="fw-row">
                <span class="fw-label">{t('Metric groups')}</span>
                <div>
                    <div class="fw-help">{t('None ticked: every group. A group is polled only while a target wants it.')}</div>
                    {#each groupSources as gs (gs.source)}
                        {@const own = groups.filter((g) => g.source === gs.source)}
                        {#if own.length}
                            <div class="fw-groups-head">
                                <b>{t(gs.label)}</b>
                                <button class="fw-link" type="button" onclick={() => tickGroups(gs.source, true)}>{t('all')}</button>
                                <button class="fw-link" type="button" onclick={() => tickGroups(gs.source, false)}>{t('none')}</button>
                            </div>
                            <div class="fw-groups">
                                {#each own as g (g.name)}
                                    <label class="fw-group" title={g.metrics.map((m) => `${m.name}${m.labels.length ? '{' + m.labels.join(',') + '}' : ''}${m.unit ? ' [' + m.unit + ']' : ''} - ${m.doc[lang]}`).join('\n')}>
                                        <input type="checkbox" checked={value.metric_groups.includes(g.name)} onchange={(e) => toggleGroup(g.name, (e.currentTarget as HTMLInputElement).checked)} />
                                        <span class="fw-group-text"><span class="fw-group-title">{g.title[lang]} <code class="fw-faint">{g.name}</code></span><span class="fw-help">{g.doc[lang]}</span></span>
                                    </label>
                                {/each}
                            </div>
                        {/if}
                    {/each}
                </div>
            </div>
        {/if}
        {#if hasKind('log')}
            <div class="fw-row">
                <span class="fw-label">{t('Log streams')}</span>
                <ChipInput bind:values={value.log_streams} suggestions={logStreams} label={t('Log streams')} placeholder={logStreams.length ? t('all') : t('(log streams come with a later version)')} />
            </div>
            <div class="fw-row">
                <span class="fw-label">{t('Log units')}</span>
                <ChipInput bind:values={value.log_units} label={t('Log units')} placeholder={t('all; e.g. occulited.service')} />
            </div>
            <div class="fw-row">
                <label for="fw-prio">{t('Priority')}</label>
                <select id="fw-prio" class="hmm-select" bind:value={value.log_priority}>
                    <option value="">{t('all')}</option>
                    {#each ['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug'] as p (p)}
                        <option value={p}>{t('up to {p}', {p})}</option>
                    {/each}
                </select>
            </div>
        {/if}
    </div>
{:else}
    <div class="fw-toolbar">
        <input class="hmm-input" type="search" bind:value={search} placeholder={t('Search name, address, type, room')} style="width:20em" />
        <button class="hmm-button" type="button" onclick={() => tickAllVisible(true)}>{t('Tick shown')}</button>
        <button class="hmm-button" type="button" onclick={() => tickAllVisible(false)}>{t('Untick shown')}</button>
        <span class="fw-help">{t('Nothing ticked: every device the rules allow. A tick on a device includes its channels; × excludes a device or channel whatever the rules say.')}</span>
    </div>
    <div class="fw-tree" role="tree">
        {#each devices as d (d.interface + '/' + d.address)}
            {@const r = ref(d.interface, d.address)}
            {@const open = expanded[r] ?? false}
            <div class="fw-tree-row" role="treeitem" aria-expanded={open} aria-selected={ticked(r)}>
                <button class="caret" type="button" onclick={() => (expanded[r] = !open)} aria-label={open ? '−' : '+'}>{open ? '▾' : '▸'}</button>
                <input type="checkbox" checked={ticked(r)} onchange={(e) => tick(r, (e.currentTarget as HTMLInputElement).checked)} aria-label={d.name || d.address} />
                <span class="name" title="{d.name} ({d.address})">{d.name || d.address} <span class="fw-faint">{d.address}</span></span>
                <span class="type fw-small" title={d.interface}>{d.interface}</span>
                <span class="type" title={d.type}>{d.type}</span>
                <button class="x" class:on={excluded(r)} type="button" onclick={() => exclude(r, !excluded(r))} title={excluded(r) ? t('excluded - click to include again') : t('exclude this device')}>×</button>
            </div>
            {#if open}
                {#each d.channels as c (c.address)}
                    {@const cr = ref(d.interface, c.address)}
                    {@const st = channelState(d.interface, d.address, c.address)}
                    <div class="fw-tree-row channel" role="treeitem" aria-selected={st !== 'off'}>
                        <span></span>
                        <input type="checkbox" checked={st !== 'off'} indeterminate={st === 'inherited'} onchange={(e) => tick(cr, (e.currentTarget as HTMLInputElement).checked)} aria-label={c.name || c.address} />
                        <span class="name" title="{c.name} ({c.address}) {c.keys.join(', ')}">{c.index}: {c.name || c.address}{#if c.rooms.length}<span class="fw-faint"> · {c.rooms.join(', ')}</span>{/if}</span>
                        <span class="type fw-small">{c.keys.length} {t('dp')}</span>
                        <span class="type" title={c.type}>{c.type}</span>
                        <button class="x" class:on={excluded(cr)} type="button" onclick={() => exclude(cr, !excluded(cr))} title={excluded(cr) ? t('excluded - click to include again') : t('exclude this channel')}>×</button>
                    </div>
                {/each}
            {/if}
        {:else}
            <div class="fw-empty">{inventory ? t('No device matches the search.') : t('Loading…')}</div>
        {/each}
    </div>
{/if}
