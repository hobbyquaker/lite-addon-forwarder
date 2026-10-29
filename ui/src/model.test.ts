import {describe, expect, it} from 'vitest';

import {emptySelection, formatValue, ruleCount} from './model.js';

describe('model', () => {
    it('counts the rules of a selection', () => {
        const s = emptySelection();
        expect(ruleCount(s)).toBe(0);
        s.rooms = ['Bad'];
        s.keys = ['A', 'B'];
        s.log_priority = 'err';
        expect(ruleCount(s)).toBe(4);
    });
    it('formats values', () => {
        expect(formatValue(21.456789)).toBe('21.457');
        expect(formatValue(3)).toBe('3');
        expect(formatValue(true)).toBe('true');
        expect(formatValue(null)).toBe('');
        expect(formatValue({a: 1})).toBe('{"a":1}');
    });
});
