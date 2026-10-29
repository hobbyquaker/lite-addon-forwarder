<script lang="ts">
    // A template input with its live preview: the template is rendered on the backend against
    // the most recent records (of the target's selection when one is set); the first result is
    // shown under the field, errors inline in red. Debounced, so typing does not flood the API.
    import {t} from 'occulite-ui/i18n';

    import {post} from './api.js';
    import type {Rendered} from './model.js';

    let {
        value = $bindable(''),
        selection = '',
        placeholder = '',
        multiline = false,
        rows = 3,
        id = '',
        label = '',
        disabled = false,
        oninput = undefined,
    }: {value?: string; selection?: string; placeholder?: string; multiline?: boolean; rows?: number; id?: string; label?: string; disabled?: boolean; oninput?: () => void} = $props();

    let output = $state('');
    let error = $state('');
    let more = $state<Rendered[]>([]);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let seq = 0;

    function render(src: string, sel: string) {
        const mine = ++seq;
        post<{error?: string; results: Rendered[]}>('api/v1/templates/render', {template: src, selection: sel, n: 3})
            .then((r) => {
                if (mine !== seq) return;
                if (r.error) {
                    error = r.error;
                    output = '';
                    more = [];
                    return;
                }
                const first = r.results[0];
                error = first?.error ?? '';
                output = first?.output ?? '';
                more = r.results;
            })
            .catch((e) => {
                if (mine !== seq) return;
                error = (e as Error).message;
            });
    }

    $effect(() => {
        const src = value;
        const sel = selection;
        if (timer) clearTimeout(timer);
        if (!src.trim()) {
            output = '';
            error = '';
            more = [];
            return;
        }
        timer = setTimeout(() => render(src, sel), 250);
        return () => {
            if (timer) clearTimeout(timer);
        };
    });

    const title = $derived(more.map((m) => `${m.record}\n  → ${m.error ? '⚠ ' + m.error : m.output}`).join('\n'));
</script>

<div class="fw-tpl">
    {#if multiline}
        <textarea {id} class="fw-mono" {rows} bind:value {placeholder} {disabled} aria-label={label} spellcheck="false" {oninput}></textarea>
    {:else}
        <input {id} class="hmm-input fw-mono" type="text" bind:value {placeholder} {disabled} aria-label={label} spellcheck="false" {oninput} />
    {/if}
    <div class="fw-tpl-out" class:fw-error={!!error} title={error || title}>
        {#if error}
            {error}
        {:else if value.trim()}
            → {output === '' ? t('(empty)') : output}
        {/if}
    </div>
</div>
