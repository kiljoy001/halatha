// Turn the browser's IANA timezone into the table read from /env/timezone by
// 9front. Its 386 libc accepts at most 75 daylight intervals and 32-bit
// transition seconds, so cover the useful 2020-2037 range.

function parts(formatter, seconds) {
    const fields = Object.fromEntries(formatter.formatToParts(new Date(seconds * 1000))
        .filter(part => part.type !== "literal").map(part => [part.type, part.value]));
    return Date.UTC(Number(fields.year), Number(fields.month) - 1,
        Number(fields.day), Number(fields.hour), Number(fields.minute),
        Number(fields.second)) / 1000;
}

export function plan9Timezone(iana = Intl.DateTimeFormat().resolvedOptions().timeZone) {
    const zone = iana || "UTC";
    const format = new Intl.DateTimeFormat("en-US", {
        timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit",
        hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
    });
    const short = new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "short" });
    const start = Date.UTC(2020, 0, 1) / 1000;
    const end = Date.UTC(2038, 0, 1) / 1000;
    const day = 86400;
    const offset = seconds => parts(format, seconds) - seconds;
    const names = new Map();
    const intervals = [];
    let prior = offset(start);
    let intervalStart = start;
    names.set(prior, short.formatToParts(new Date(start * 1000))
        .find(part => part.type === "timeZoneName")?.value);
    for (let sample = start + day; sample <= end; sample += day) {
        const next = offset(sample);
        if (next === prior) continue;
        let low = sample - day, high = sample;
        while (high - low > 1) {
            const mid = Math.floor((low + high) / 2);
            if (offset(mid) === prior) low = mid;
            else high = mid;
        }
        intervals.push({ from: intervalStart, to: high, offset: prior });
        intervalStart = high;
        prior = next;
        names.set(next, short.formatToParts(new Date(high * 1000))
            .find(part => part.type === "timeZoneName")?.value);
    }
    intervals.push({ from: intervalStart, to: end, offset: prior });

    const offsets = [...new Set(intervals.map(interval => interval.offset))].sort((a, b) => a - b);
    const current = offset(Math.floor(Date.now() / 1000));
    // Plan 9's table can describe only two offsets. If a zone changes its
    // base offset more often, preserve the browser's current wall time.
    const fixed = offsets.length > 2;
    const standard = fixed ? current : offsets[0];
    const daylight = fixed ? current : offsets.at(-1);
    const label = (value, fallback) => {
        const name = names.get(value);
        return /^[A-Za-z]{3}$/.test(name || "") ? name : fallback;
    };
    const pairs = [];
    if (!fixed && daylight !== standard) {
        for (const interval of intervals) {
            if (interval.offset !== daylight) continue;
            pairs.push(interval.from + standard - 1, interval.to + standard - 1);
        }
    }
    if (pairs.length > 148) throw new Error(`too many timezone transitions for 9front: ${zone}`);
    return {
        zone,
        fixed,
        text: `${label(standard, "LOC")} ${standard} ${label(daylight, "LDT")} ${daylight}\n` +
            (pairs.length ? ` ${pairs.join(" ")} 0\n` : "0\n"),
    };
}
