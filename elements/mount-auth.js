// Sign only a challenge issued on the WebSocket being mounted. The helper holds
// the private key through factotum; browser code receives only the proof.
export function authenticateMount(ws, src, { exportName, user, aname, pairingToken }) {
    const gateway = new URL(src, document.baseURI);
    if (gateway.protocol === "ws:") gateway.protocol = "http:";
    if (gateway.protocol === "wss:") gateway.protocol = "https:";
    if (gateway.origin !== location.origin) {
        return Promise.reject(new Error("Factotum mount helper must be same-origin"));
    }
    if (typeof pairingToken !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(pairingToken)) {
        return Promise.reject(new Error("Enter the local gateway pairing code"));
    }
    const signURL = new URL(`${gateway.pathname}/sign`, gateway);
    return new Promise((resolve, reject) => {
        let state = "challenge";
        const abort = new AbortController();
        const fail = error => {
            if (state === "done") return;
            state = "done";
            clearTimeout(timer);
            abort.abort();
            ws.close();
            reject(error);
        };
        const timer = setTimeout(() => fail(new Error("Mount authorization timed out")), 30000);
        ws.onclose = event => fail(new Error(event.reason || "Mount authorization connection closed"));
        ws.onerror = () => fail(new Error("Mount authorization connection failed"));
        ws.onmessage = async event => {
            try {
                if (typeof event.data !== "string") throw new Error("Mount authorization requires a text challenge");
                const envelope = JSON.parse(event.data);
                if (state === "ready" && envelope.type === "mount-ready") {
                    state = "done";
                    clearTimeout(timer);
                    ws.onmessage = ws.onclose = ws.onerror = null;
                    resolve();
                    return;
                }
                if (state !== "challenge" || envelope.type !== "mount-challenge" ||
                    typeof envelope.payload !== "string" || envelope.payload.length > 4096) {
                    throw new Error("Unexpected mount authorization message");
                }
                state = "signing";
                const raw = atob(envelope.payload.replace(/-/g, "+").replace(/_/g, "/"));
                const challenge = JSON.parse(new TextDecoder().decode(Uint8Array.from(raw, c => c.charCodeAt(0))));
                if (challenge.protocol !== "halatha-mount-v1" || challenge.server !== gateway.origin ||
                    challenge.origin !== location.origin || challenge.export !== exportName ||
                    challenge.access !== "read-only" || challenge.user !== user || challenge.aname !== aname ||
                    typeof challenge.target !== "string" || !challenge.target ||
                    !/^[A-Za-z0-9_-]{43}$/.test(challenge.session) || !/^[A-Za-z0-9_-]{43}$/.test(challenge.nonce) ||
                    !Number.isSafeInteger(challenge.expires) || challenge.expires <= Date.now() ||
                    challenge.expires > Date.now() + 60000) {
                    throw new Error("Mount challenge does not match the requested export");
                }
                const response = await fetch(signURL, {
                    method: "POST",
                    headers: { "Content-Type": "application/json", "Authorization": `Bearer ${pairingToken}` },
                    credentials: "same-origin",
                    cache: "no-store",
                    body: JSON.stringify({ session: challenge.session }),
                    signal: abort.signal,
                });
                if (!response.ok) throw new Error("Factotum denied mount authorization");
                const proof = await response.json();
                if (state !== "signing") return;
                if (proof.type !== "mount-proof" || proof.session !== challenge.session ||
                    !/^[0-9a-f]{64}$/.test(proof.publicKey) || !/^[0-9a-f]{128}$/.test(proof.signature)) {
                    throw new Error("Invalid mount proof from local helper");
                }
                state = "ready";
                ws.send(JSON.stringify(proof));
            } catch (error) { fail(error); }
        };
    });
}
