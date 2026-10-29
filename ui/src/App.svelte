<script lang="ts">
    // The configuration page: targets, selections, the live records, the settings and about.
    // The status stream keeps the targets' cards current.
    import {onMount} from 'svelte';
    import {Notices, Tabs, ThemeSwitch} from 'occulite-ui';
    import {t} from 'occulite-ui/i18n';
    import {NoticesStore} from 'occulite-ui/notices';
    import {cycleTheme, theme} from 'occulite-ui/theme';

    import AboutPage from './AboutPage.svelte';
    import RecordsPage from './RecordsPage.svelte';
    import SelectionsPage from './SelectionsPage.svelte';
    import TargetsPage from './TargetsPage.svelte';
    import {get, stream} from './api.js';
    import type {Health, Inventory, SelectionView, StatusMessage} from './model.js';

    const notices = new NoticesStore();
    let tab = $state('targets');
    let health = $state<Health | undefined>();
    let status = $state<StatusMessage | undefined>();
    let inventory = $state<Inventory | undefined>();
    let selections = $state<SelectionView[]>([]);
    let error = $state('');

    async function loadInventory() {
        try {
            inventory = await get<Inventory>('api/v1/inventory');
        } catch (e) {
            error = (e as Error).message;
        }
    }

    async function loadSelections() {
        try {
            selections = await get<SelectionView[]>('api/v1/selections');
        } catch (e) {
            error = (e as Error).message;
        }
    }

    async function loadHealth() {
        try {
            health = await get<Health>('api/v1/health');
            error = '';
        } catch (e) {
            error = (e as Error).message;
        }
    }

    onMount(() => {
        void loadHealth();
        void loadInventory();
        void loadSelections();
        const s = stream((m) => (status = m));
        const timer = setInterval(() => {
            void loadHealth();
            if (tab === 'selections') void loadInventory();
        }, 30000);
        return () => {
            s.close();
            clearInterval(timer);
        };
    });

    const tabs = $derived([
        {id: 'targets', label: t('Targets'), badge: status?.targets.length || undefined},
        {id: 'selections', label: t('Selections'), badge: selections.length || undefined},
        {id: 'records', label: t('Records')},
        {id: 'about', label: t('System')},
    ]);
</script>

<div class="fw-app">
    <header class="fw-header">
        <h1>{t('Forwarder')}</h1>
        <Tabs {tabs} bind:active={tab} />
        <span class="fw-spacer"></span>
        {#if status}
            <span class="fw-small {status.source.connected ? 'fw-muted' : 'fw-error'}" title={status.source.last_error || ''}>
                {status.source.connected ? t('{n} datapoints · {e} events', {n: status.source.datapoints, e: status.source.events}) : t('Not connected to the system')}
            </span>
        {/if}
        {#if !theme.followsShell}
            <ThemeSwitch theme={theme.choice} onclick={cycleTheme} />
        {/if}
    </header>
    {#if error}
        <div class="fw-banner">{t('The addon does not answer: {error}', {error})}</div>
    {/if}
    <Notices notices={notices.visible} ondismiss={(id) => notices.dismiss(id)} />
    <main class="fw-page">
        {#if tab === 'targets'}
            <TargetsPage {notices} {status} {selections} onchanged={() => void loadHealth()} />
        {:else if tab === 'selections'}
            <SelectionsPage {notices} {selections} {inventory} onchanged={() => void loadSelections()} onrefresh={() => void loadInventory()} />
        {:else if tab === 'records'}
            <RecordsPage {selections} targets={status?.targets ?? []} />
        {:else}
            <AboutPage {notices} {health} {status} />
        {/if}
    </main>
</div>
