import assert from "node:assert/strict";
import test from "node:test";
import { plan9Timezone } from "./browser-timezone.mjs";

// Match 9front's tzoffset: transition seconds are expressed in standard time
// and the daylight interval is open at the start, closed at the end.
function offsetAt(text, seconds) {
    const numbers = text.trim().split(/\s+/);
    const standard = Number(numbers[1]);
    const daylight = Number(numbers[3]);
    for (let i = 4; i + 1 < numbers.length && numbers[i] !== "0"; i += 2) {
        if (seconds + standard > Number(numbers[i]) &&
            seconds + standard <= Number(numbers[i + 1])) return daylight;
    }
    return standard;
}

test("browser timezone covers daylight transitions exactly", () => {
    const { text, fixed } = plan9Timezone("America/Los_Angeles");
    assert.equal(fixed, false);
    const spring = Date.UTC(2026, 2, 8, 10) / 1000;
    const autumn = Date.UTC(2026, 10, 1, 9) / 1000;
    assert.equal(offsetAt(text, spring - 1), -28800);
    assert.equal(offsetAt(text, spring), -25200);
    assert.equal(offsetAt(text, autumn - 1), -25200);
    assert.equal(offsetAt(text, autumn), -28800);
});

test("southern hemisphere and quarter-hour zones", () => {
    const sydney = plan9Timezone("Australia/Sydney");
    assert.equal(offsetAt(sydney.text, Date.UTC(2026, 0, 15) / 1000), 39600);
    assert.equal(offsetAt(sydney.text, Date.UTC(2026, 6, 15) / 1000), 36000);
    const kathmandu = plan9Timezone("Asia/Kathmandu");
    assert.equal(offsetAt(kathmandu.text, Date.UTC(2026, 0, 15) / 1000), 20700);
    assert.match(kathmandu.text, /0\n$/);
});
