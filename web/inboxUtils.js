// Helpers for the Inbox page. startPolling is copied from
// pocketbase-openworkflow's workflowUtils.js (the plugins share no code).

// Calls fn every interval while the tab is visible (and once when it becomes
// visible again). Returns the stop function.
export function startPolling(fn, interval) {
    let timer = null;

    function start() {
        timer = timer || setInterval(fn, interval);
    }

    function stop() {
        clearInterval(timer);
        timer = null;
    }

    function onVisibilityChange() {
        if (document.hidden) {
            stop();
        } else {
            fn();
            start();
        }
    }

    if (!document.hidden) {
        start();
    }
    document.addEventListener("visibilitychange", onVisibilityChange);

    return () => {
        stop();
        document.removeEventListener("visibilitychange", onVisibilityChange);
    };
}

// PocketBase dates ("2026-10-14 12:00:00.000Z") as a Date (null when empty).
export function parseDate(value) {
    if (!value) {
        return null;
    }
    const date = new Date(value.replace(" ", "T"));
    return isNaN(date) ? null : date;
}

// Time for today, else the date (and the year when it isn't this one).
export function shortTime(value) {
    const date = parseDate(value);
    if (!date) {
        return "";
    }
    const now = new Date();
    if (date.toDateString() == now.toDateString()) {
        return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    }
    return date.toLocaleDateString([], {
        month: "short",
        day: "numeric",
        year: date.getFullYear() == now.getFullYear() ? undefined : "numeric",
    });
}

export function fullTime(value) {
    const date = parseDate(value);
    return date ? date.toLocaleString() : "";
}

// Fetches a message attachment with the superuser token (an <img src> can't
// send it) and returns an object url.
const mediaCache = new Map();

export async function mediaURL(mediaId) {
    if (!mediaCache.has(mediaId)) {
        const promise = fetch(app.pb.buildURL("/api/whatsapp/inbox/media/" + encodeURIComponent(mediaId)), {
            headers: { Authorization: app.pb.authStore.token },
        }).then(async (res) => {
            if (!res.ok) {
                const body = await res.json().catch(() => ({}));
                throw new Error(body.message || "The media is no longer available.");
            }
            return URL.createObjectURL(await res.blob());
        });
        promise.catch(() => mediaCache.delete(mediaId));
        mediaCache.set(mediaId, promise);
    }
    return mediaCache.get(mediaId);
}

export const STATUS_ICONS = {
    accepted: "ri-time-line",
    sent: "ri-check-line",
    delivered: "ri-check-double-line",
    read: "ri-check-double-line",
    failed: "ri-error-warning-line",
};
