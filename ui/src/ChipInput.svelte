<script lang="ts">
    // A list of strings as chips: type and press Enter (or comma) to add, click × to remove,
    // or click a suggestion below to toggle it. Used for keys (with wildcards), rooms,
    // functions, device types, channel types and interfaces.
    let {
        values = $bindable([]),
        suggestions = [],
        placeholder = '',
        label = '',
        allowFree = true,
        counts,
    }: {values?: string[]; suggestions?: string[]; placeholder?: string; label?: string; allowFree?: boolean; counts?: Map<string, number> | undefined} = $props();

    let text = $state('');
    let filter = $state('');

    function add(v: string) {
        v = v.trim();
        if (!v || values.includes(v)) return;
        values = [...values, v];
    }

    function remove(v: string) {
        values = values.filter((x) => x !== v);
    }

    function toggle(v: string) {
        if (values.includes(v)) remove(v);
        else add(v);
    }

    function onkey(e: KeyboardEvent) {
        if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault();
            if (allowFree || suggestions.includes(text.trim())) add(text);
            else if (visible.length === 1 && visible[0]) add(visible[0]);
            text = '';
        } else if (e.key === 'Backspace' && !text && values.length) {
            values = values.slice(0, -1);
        }
    }

    const visible = $derived.by(() => {
        const f = (filter || text).toLowerCase();
        const list = f ? suggestions.filter((s) => s.toLowerCase().includes(f)) : suggestions;
        return list.slice(0, 200);
    });
</script>

<div>
    <div class="fw-chips" role="list" aria-label={label}>
        {#each values as v (v)}
            <span class="fw-chip" role="listitem" title={v}><span>{v}</span><button type="button" aria-label="×" onclick={() => remove(v)}>×</button></span>
        {/each}
        <input type="text" bind:value={text} oninput={() => (filter = text)} onkeydown={onkey} {placeholder} aria-label={label} />
    </div>
    {#if suggestions.length}
        <div class="fw-suggest">
            {#each visible as s (s)}
                <button type="button" class:on={values.includes(s)} onclick={() => toggle(s)} title={counts?.get(s) !== undefined ? `${s} (${counts.get(s)})` : s}>{s}{#if counts?.get(s) !== undefined}<span class="fw-faint"> {counts.get(s)}</span>{/if}</button>
            {/each}
            {#if visible.length < suggestions.length}
                <span class="fw-faint fw-small">… {suggestions.length - visible.length}</span>
            {/if}
        </div>
    {/if}
</div>
