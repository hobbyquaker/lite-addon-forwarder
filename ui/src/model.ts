// The API's shapes (docs/api.md). The JSON of a selection is shared with other addons that use
// the same selection model, so its field names are stable.

export type Kind = 'event' | 'metric' | 'log';

export interface Selection {
    id: string;
    name: string;
    kinds: Kind[];
    interfaces: string[];
    device_types: string[];
    channel_types: string[];
    objects: string[];
    exclude_objects: string[];
    keys: string[];
    exclude_keys: string[];
    rooms: string[];
    functions: string[];
    sources: string[];
    metric_groups: string[];
    log_streams: string[];
    log_units: string[];
    log_priority: string;
}

export interface Count {
    datapoints: number;
    channels: number;
    devices: number;
}

export interface SelectionView extends Selection {
    count: Count;
    used_by: string[];
    targets: number;
}

export function emptySelection(): Selection {
    return {
        id: '',
        name: '',
        kinds: ['event'],
        interfaces: [],
        device_types: [],
        channel_types: [],
        objects: [],
        exclude_objects: [],
        keys: [],
        exclude_keys: [],
        rooms: [],
        functions: [],
        sources: ['event', 'snapshot'],
        metric_groups: [],
        log_streams: [],
        log_units: [],
        log_priority: '',
    };
}

export interface NamedTemplate {
    name: string;
    value: string;
}

export interface FieldTemplate {
    name: string;
    value: string;
    type: 'auto' | 'float' | 'integer' | 'boolean' | 'string';
}

export interface InfluxTemplates {
    measurement: string;
    tags: NamedTemplate[];
    fields: FieldTemplate[];
}

export interface Influx {
    version: '1' | '2';
    url: string;
    database: string;
    retention_policy: string;
    user: string;
    org: string;
    bucket: string;
    gzip: boolean;
    preset: string;
    event: InfluxTemplates;
    metric: InfluxTemplates;
    log: InfluxTemplates;
}

export interface Webhook {
    url: string;
    method: 'POST' | 'PUT' | 'PATCH';
    format: 'json' | 'ndjson';
    body: string;
    content_type: string;
    headers: Record<string, string>;
    auth: 'none' | 'basic' | 'bearer';
    user: string;
    gzip: boolean;
}

export type TargetType = 'influx' | 'webhook';

export interface Target {
    id: string;
    name: string;
    type: TargetType;
    enabled: boolean;
    selection: string;
    batch: {size: number; interval_ms: number; max_bytes: number};
    queue: {max_records: number};
    retry: {min_ms: number; max_ms: number};
    tls: {insecure: boolean; ca: string; server_name: string};
    timeout_s: number;
    influx?: Influx;
    webhook?: Webhook;
    secrets: Record<string, boolean>;
    set_secrets?: Record<string, string>;
    clear_secrets?: string[];
}

export type TargetState = 'idle' | 'ok' | 'retrying' | 'error' | 'disabled';

export interface Status {
    id: string;
    name: string;
    type: TargetType;
    enabled: boolean;
    running: boolean;
    connected: boolean;
    state: TargetState;
    last_success: string;
    last_error: string;
    last_error_at: string;
    queued: number;
    in_flight: number;
    sent: number;
    batches: number;
    dropped: number;
    overflow: number;
    retried: number;
    render_errors: number;
    last_render_error: string;
    lag_ms: number;
    next_retry: string;
    attempt: number;
}

export interface TargetView extends Target {
    status?: Status;
}

export interface SourceStats {
    connected: boolean;
    interfaces: number;
    devices: number;
    channels: number;
    datapoints: number;
    events: number;
    snapshots: number;
    resyncs: number;
    last_event: string;
    last_error: string;
    meta_revision: number;
    inventory_at: string;
}

export interface StatusMessage {
    source: SourceStats;
    targets: Status[];
}

export interface Health {
    version: string;
    started: string;
    uptime_s: number;
    source: SourceStats;
    targets: number;
    selections: number;
    types: TargetType[];
}

export interface ChannelInfo {
    address: string;
    index: number;
    type: string;
    name: string;
    rooms: string[];
    functions: string[];
    keys: string[];
}

export interface DeviceInfo {
    interface: string;
    address: string;
    type: string;
    name: string;
    rooms: string[];
    functions: string[];
    channels: ChannelInfo[];
}

export interface Counted {
    name: string;
    count: number;
}

export interface Inventory {
    interfaces: {name: string; protocol: string; running: boolean; devices: number}[];
    devices: DeviceInfo[];
    rooms: string[];
    functions: string[];
    device_types: Counted[];
    channel_types: Counted[];
    keys: Counted[];
    datapoints: number;
    updated_at: string;
}

export interface Rec {
    ts: string;
    kind: Kind;
    interface?: string;
    device?: string;
    channel: number;
    address?: string;
    key?: string;
    value: unknown;
    value_num?: number;
    value_text?: string;
    type?: string;
    unit?: string;
    device_type?: string;
    channel_type?: string;
    device_name?: string;
    channel_name?: string;
    rooms: string[];
    functions: string[];
    source?: string;
    group?: string;
    name?: string;
    labels?: Record<string, string>;
    stream?: string;
    log_unit?: string;
    priority?: string;
    message?: string;
    summary: string;
    targets: string[];
}

export interface Match {
    count: Count;
    recent: Rec[];
}

export interface Preview {
    records: number;
    text: string;
    errors: string[];
}

export interface TestResult {
    ok: boolean;
    message: string;
    record: string;
}

export interface Rendered {
    record: string;
    output: string;
    error?: string;
}

export interface Presets {
    influx: Record<string, Influx>;
    influx_order: string[];
    webhook: Record<string, Webhook>;
    defaults: Record<TargetType, Target>;
}

export interface Settings {
    snapshot: boolean;
    recent: number;
    lang: string;
}

export interface TemplateHelp {
    variables: {name: string; type: string; doc: Record<string, string>}[];
    filters: {name: string; doc: Record<string, string>}[];
}

/** The parts of a selection that are rules, with how many entries each has. */
export function ruleCount(s: Selection): number {
    return (
        s.interfaces.length +
        s.device_types.length +
        s.channel_types.length +
        s.keys.length +
        s.exclude_keys.length +
        s.rooms.length +
        s.functions.length +
        s.metric_groups.length +
        s.log_streams.length +
        s.log_units.length +
        (s.log_priority ? 1 : 0)
    );
}

/** A deep copy of a plain value. */
export function clone<T>(v: T): T {
    return JSON.parse(JSON.stringify(v)) as T;
}

/** Formats a value for a table cell. */
export function formatValue(v: unknown): string {
    if (v === null || v === undefined) return '';
    if (typeof v === 'number') return Number.isInteger(v) ? String(v) : String(Math.round(v * 1000) / 1000);
    if (typeof v === 'boolean') return v ? 'true' : 'false';
    if (typeof v === 'string') return v;
    return JSON.stringify(v);
}
