import {describe, expect, it} from 'vitest';

import {describeTarget, emptySelection, formatValue, ruleCount, secretNames, TARGET_TYPES, type Target} from './model.js';

function target(type: Target['type'], extra: Partial<Target>): Target {
    return {id: 'x', name: 'n', type, enabled: true, selection: '', batch: {size: 1, interval_ms: 1, max_bytes: 1}, queue: {max_records: 1, spill_mb: 0}, retry: {min_ms: 1, max_ms: 1}, tls: {insecure: false, ca: '', server_name: ''}, timeout_s: 1, secrets: {}, ...extra};
}

describe('model', () => {
    it('counts the rules of a selection', () => {
        const s = emptySelection();
        expect(ruleCount(s)).toBe(0);
        s.rooms = ['Bad'];
        s.keys = ['A', 'B'];
        s.log_priority = 'err';
        expect(ruleCount(s)).toBe(4);
    });
    it('knows the secrets and the description of the 0.3.0 types', () => {
        expect(TARGET_TYPES.map((t) => t.type)).toEqual(['influx', 'webhook', 'elastic', 'prometheus', 'sql', 'syslog', 'gelf', 'loki']);
        const sql = target('sql', {sql: {dialect: 'mysql', host: 'db', port: 3306, database: 'hm', user: 'u', tls_mode: 'disable', insert: '', schema: '', install_schema: true, preset: 'wide'}});
        expect(secretNames(sql)).toEqual(['password']);
        expect(describeTarget(sql)).toEqual({kind: 'MySQL / MariaDB', where: 'db:3306/hm'});
        const syslog = target('syslog', {syslog: {host: 'h', port: 514, transport: 'tls', framing: 'octet', facility: 16, severity: '', hostname: '', app_name: 'a', msg_id: 'm', sd_id: '', sd_params: [], message: 'x', preset: 'plain'}});
        expect(secretNames(syslog)).toEqual([]);
        expect(describeTarget(syslog)).toEqual({kind: 'Syslog TLS', where: 'h:514'});
        const gelf = target('gelf', {gelf: {host: 'g', port: 12201, transport: 'udp', compression: 'gzip', short_message: 's', full_message: '', level: '', fields: [], all_fields: true, preset: 'record'}});
        expect(describeTarget(gelf).kind).toBe('GELF UDP');
        const loki = target('loki', {loki: {url: 'http://l:3100', tenant_id: '', auth: 'bearer', user: '', gzip: true, labels: [], line: 'x', metadata: [], preset: 'logfmt'}});
        expect(secretNames(loki)).toEqual(['token']);
        expect(describeTarget(loki)).toEqual({kind: 'Loki', where: 'http://l:3100'});
    });
    it('formats values', () => {
        expect(formatValue(21.456789)).toBe('21.457');
        expect(formatValue(3)).toBe('3');
        expect(formatValue(true)).toBe('true');
        expect(formatValue(null)).toBe('');
        expect(formatValue({a: 1})).toBe('{"a":1}');
    });
});
