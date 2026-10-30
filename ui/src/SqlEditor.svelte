<script lang="ts">
    // The SQL statement editor: CodeMirror 6 with the dialect's highlighting and a completion
    // of the record's :name placeholders (typed as `:` plus letters). CodeMirror is loaded on
    // first use, so the page itself stays small; until then a plain textarea holds the text.
    // The colours are the kit's tokens (app.css, .fw-cm), so dark mode comes with them.
    import {onMount} from 'svelte';
    import type {CompletionContext} from '@codemirror/autocomplete';
    import type {EditorView as EditorViewT} from '@codemirror/view';

    import type {SqlDialect} from './model.js';

    let {
        value = $bindable(''),
        dialect = 'postgres',
        placeholders = [],
        docs = {},
        rows = 8,
        id = '',
        label = '',
        oninput = undefined,
    }: {value?: string; dialect?: SqlDialect; placeholders?: string[]; docs?: Record<string, string>; rows?: number; id?: string; label?: string; oninput?: () => void} = $props();

    let host: HTMLDivElement | undefined = $state();
    let ready = $state(false);
    // the editor instance and what it needs to react to prop changes (type-only imports, so
    // the modules are not loaded with the page)
    let view: EditorViewT | undefined;
    let setDialect: ((d: SqlDialect) => void) | undefined;
    let setPlaceholders: ((p: string[], docs: Record<string, string>) => void) | undefined;
    let lastValue = value;

    onMount(() => {
        let gone = false;
        void (async () => {
            const [{EditorState, Compartment}, {EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection}, {sql, PostgreSQL, MySQL}, {autocompletion, closeBrackets}, {defaultHighlightStyle, syntaxHighlighting, bracketMatching}, {defaultKeymap, history, historyKeymap, indentWithTab}] = await Promise.all([
                import('@codemirror/state'),
                import('@codemirror/view'),
                import('@codemirror/lang-sql'),
                import('@codemirror/autocomplete'),
                import('@codemirror/language'),
                import('@codemirror/commands'),
            ]);
            if (gone || !host) return;
            const language = new Compartment();
            const completion = new Compartment();
            const languageOf = (d: SqlDialect) => sql({dialect: d === 'mysql' ? MySQL : PostgreSQL, upperCaseKeywords: true});
            const completionOf = (names: string[], d: Record<string, string>) =>
                autocompletion({
                    override: [
                        (ctx: CompletionContext) => {
                            const word = ctx.matchBefore(/:[A-Za-z_]*/);
                            if (!word || (word.from === word.to && !ctx.explicit)) return null;
                            // a `::` is a cast, not a placeholder
                            if (word.from > 0 && ctx.state.doc.sliceString(word.from - 1, word.from) === ':') return null;
                            return {from: word.from, options: names.map((n) => ({label: ':' + n, type: 'variable', detail: d[n] ?? ''}))};
                        },
                    ],
                    activateOnTyping: true,
                });
            const v = new EditorView({
                parent: host,
                state: EditorState.create({
                    doc: value,
                    extensions: [
                        lineNumbers(),
                        highlightActiveLine(),
                        drawSelection(),
                        history(),
                        bracketMatching(),
                        closeBrackets(),
                        language.of(languageOf(dialect)),
                        completion.of(completionOf(placeholders, docs)),
                        syntaxHighlighting(defaultHighlightStyle, {fallback: true}),
                        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
                        EditorView.lineWrapping,
                        EditorView.updateListener.of((u) => {
                            if (u.docChanged) {
                                lastValue = u.state.doc.toString();
                                value = lastValue;
                                oninput?.();
                            }
                        }),
                        EditorView.contentAttributes.of({'aria-label': label, id}),
                    ],
                }),
            });
            view = v;
            setDialect = (d) => v.dispatch({effects: language.reconfigure(languageOf(d))});
            setPlaceholders = (p, d) => v.dispatch({effects: completion.reconfigure(completionOf(p, d))});
            ready = true;
        })();
        return () => {
            gone = true;
            view?.destroy();
            view = undefined;
        };
    });

    // a value set from outside (a preset) replaces the document
    $effect(() => {
        const v = value;
        if (view && v !== lastValue) {
            lastValue = v;
            view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: v}});
        }
    });
    $effect(() => {
        const d = dialect;
        setDialect?.(d);
    });
    $effect(() => {
        const p = placeholders;
        const d = docs;
        setPlaceholders?.(p, d);
    });
</script>

<div class="fw-cm" class:ready style="--fw-cm-rows: {rows}">
    {#if !ready}
        <textarea {id} class="fw-mono fw-cm-fallback" {rows} bind:value aria-label={label} spellcheck="false" {oninput}></textarea>
    {/if}
    <div class="fw-cm-host" bind:this={host} hidden={!ready}></div>
</div>
