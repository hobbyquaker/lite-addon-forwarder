<script lang="ts">
    // A yes/no question in the kit's dialog, never window.confirm.
    import {Dialog} from 'occulite-ui';
    import {t} from 'occulite-ui/i18n';

    let {
        open,
        title,
        message,
        confirmLabel,
        danger = false,
        busy = false,
        onconfirm,
        oncancel,
    }: {open: boolean; title: string; message: string; confirmLabel?: string; danger?: boolean; busy?: boolean; onconfirm: () => void; oncancel: () => void} = $props();
</script>

<Dialog {open} {title} width="30rem" closable={!busy} onclose={oncancel} testId="confirm">
    <p>{message}</p>
    {#snippet buttons()}
        <button class="hmm-button" type="button" onclick={oncancel} disabled={busy}>{t('Cancel')}</button>
        <button class="hmm-button {danger ? 'fw-error' : ''}" type="button" onclick={onconfirm} disabled={busy}>{confirmLabel ?? t('OK')}</button>
    {/snippet}
</Dialog>
