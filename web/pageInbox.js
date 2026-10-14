// Inbox: the WhatsApp conversations stored by the plugin (VelaStack mode).
//
// A ground-truth view for superusers of what is in _whatsappContacts and
// _whatsappMessages, with replies sent through VelaStack. The open
// conversation is driven by the `contact` query param, so it deep-links.

import { STATUS_ICONS, fullTime, mediaURL, shortTime, startPolling } from "./inboxUtils";

const API = "/api/whatsapp/inbox";
const POLL_INTERVAL = 5000;
const CONTACT_QUERY_KEY = "contact";

const INCOMING_HINTS = {
    webhook: "VelaStack delivers new messages to this app's webhook.",
    polling: "This app isn't reachable over public https, so new messages are fetched from VelaStack every few seconds.",
    "": "Incoming messages need the VelaStack sender. Set it up in Settings > WhatsApp.",
};

export function pageInbox(route) {
    app.store.title = "Inbox";

    const data = store({
        contacts: [],
        incoming: null,
        loading: true,
        activeId: route.query[CONTACT_QUERY_KEY]?.[0] || "",
        thread: null, // {contact, messages}
        threadLoading: false,
        templates: null,
        reply: {
            mode: "text", // text | template
            text: "",
            template: "",
            params: "",
            sending: false,
            error: "",
        },
        get active() {
            return data.contacts.find((c) => c.id == data.activeId) || data.thread?.contact || null;
        },
    });

    const uid = app.utils.randomString(6);
    const contactsKey = "wa_inbox_" + uid;
    const threadKey = "wa_thread_" + uid;

    async function loadContacts(silent = false) {
        try {
            const res = await app.pb.send(API, { method: "GET", requestKey: contactsKey });
            if (JSON.stringify(res.contacts) != JSON.stringify(data.contacts)) {
                data.contacts = res.contacts || [];
            }
            data.incoming = res.incoming || null;
        } catch (err) {
            if (!err.isAbort && !silent) app.checkApiError(err);
        }
        data.loading = false;
    }

    async function loadThread(silent = false) {
        const id = data.activeId;
        if (!id) {
            data.thread = null;
            return;
        }
        if (!silent) {
            data.threadLoading = true;
        }
        try {
            const res = await app.pb.send(`${API}/${encodeURIComponent(id)}`, { method: "GET", requestKey: threadKey });
            if (id == data.activeId && JSON.stringify(res) != JSON.stringify(data.thread)) {
                data.thread = res;
            }
        } catch (err) {
            if (!err.isAbort && !silent) app.checkApiError(err);
        }
        data.threadLoading = false;
    }

    async function markRead(contact) {
        if (!contact?.unread) {
            return;
        }
        try {
            await app.pb.send(`${API}/${encodeURIComponent(contact.id)}/read`, { method: "POST" });
            loadContacts(true);
        } catch (err) {
            app.checkApiError(err);
        }
    }

    async function loadTemplates() {
        if (data.templates) {
            return;
        }
        try {
            const res = await app.pb.send(`${API}/templates`, { method: "GET" });
            data.templates = res.templates || [];
        } catch (err) {
            data.templates = [];
            data.reply.error = err.message || "Couldn't load the templates.";
        }
    }

    function openContact(id) {
        if (id == data.activeId) {
            return;
        }
        data.activeId = id;
        data.thread = null;
        data.reply.text = "";
        data.reply.error = "";
        app.utils.replaceHashQueryParams({ [CONTACT_QUERY_KEY]: id || null });
        loadThread();
        markRead(data.contacts.find((c) => c.id == id));
        const contact = data.contacts.find((c) => c.id == id);
        setReplyMode(contact && !contact.windowOpen ? "template" : "text");
    }

    function setReplyMode(mode) {
        data.reply.mode = mode;
        data.reply.error = "";
        if (mode == "template") {
            loadTemplates();
        }
    }

    async function send() {
        const contact = data.active;
        if (!contact || data.reply.sending) {
            return;
        }

        let body;
        if (data.reply.mode == "text") {
            const text = data.reply.text.trim();
            if (!text) {
                return;
            }
            body = { text };
        } else {
            const [name, language] = data.reply.template.split("|");
            if (!name) {
                data.reply.error = "Pick a template.";
                return;
            }
            const params = data.reply.params
                .split("\n")
                .map((line) => line.trim())
                .filter(Boolean);
            body = { template: { name, language, params } };
        }

        data.reply.sending = true;
        data.reply.error = "";
        try {
            await app.pb.send(`${API}/${encodeURIComponent(contact.id)}`, { method: "POST", body });
            data.reply.text = "";
            data.reply.params = "";
            await Promise.all([loadThread(true), loadContacts(true)]);
        } catch (err) {
            data.reply.error = err.message || "WhatsApp didn't send the message.";
            if (err.response?.code == "window_closed") {
                setReplyMode("template");
            }
        }
        data.reply.sending = false;
    }

    loadContacts().then(() => {
        if (data.activeId) {
            const contact = data.contacts.find((c) => c.id == data.activeId);
            loadThread();
            markRead(contact);
            setReplyMode(contact && !contact.windowOpen ? "template" : "text");
        }
    });

    const stopPolling = startPolling(() => {
        loadContacts(true);
        loadThread(true);
    }, POLL_INTERVAL);

    return t.div(
        {
            pbEvent: "pageInbox",
            className: "page page-whatsapp-inbox",
            onunmount: () => {
                stopPolling();
                app.pb.cancelRequest(contactsKey);
                app.pb.cancelRequest(threadKey);
            },
        },
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav({ className: "breadcrumbs" }, t.div({ className: "breadcrumb-item" }, "WhatsApp inbox")),
                t.div({ className: "flex-fill" }),
                () =>
                    data.incoming
                        ? t.span(
                            {
                                className: () => "label " + (data.incoming.error ? "warning" : data.incoming.mode ? "success" : ""),
                                ariaLabel: app.attrs.tooltip(data.incoming.error || INCOMING_HINTS[data.incoming.mode] || "", "left"),
                            },
                            data.incoming.mode == "webhook"
                                ? "Webhook"
                                : data.incoming.mode == "polling"
                                ? "Polling"
                                : "Not receiving",
                        )
                        : t.span({}),
                t.button(
                    {
                        type: "button",
                        className: "btn secondary",
                        onclick: () => {
                            loadContacts();
                            loadThread();
                        },
                    },
                    t.i({ className: "ri-refresh-line" }),
                    t.span({ className: "txt" }, "Refresh"),
                ),
            ),
            () =>
                data.incoming && !data.incoming.mode
                    ? t.div({ className: "alert warning m-b-sm" }, t.div({ className: "content" }, INCOMING_HINTS[""]))
                    : data.incoming?.error
                    ? t.div({ className: "alert warning m-b-sm" }, t.div({ className: "content" }, data.incoming.error))
                    : t.span({}),
            t.div(
                { className: () => "wa-inbox " + (data.activeId ? "wa-inbox-thread-open" : "") },
                contactList(),
                threadView(),
            ),
        ),
    );

    // ---------------------------------------------------------------

    function contactList() {
        return t.div(
            { className: "wa-contacts" },
            () => {
                if (!data.contacts.length) {
                    return t.p(
                        { className: "wa-empty txt-hint" },
                        data.loading ? "Loading..." : "No conversations yet. Messages people send to the number show up here.",
                    );
                }
                return data.contacts.map((c) =>
                    t.button(
                        {
                            type: "button",
                            className: () => "wa-contact " + (c.id == data.activeId ? "active" : ""),
                            onclick: () => openContact(c.id),
                        },
                        t.div(
                            { className: "wa-contact-top" },
                            t.span({ className: "wa-contact-name" }, c.name || c.phone),
                            t.span({ className: "wa-contact-time txt-hint" }, shortTime(c.lastMessageAt)),
                        ),
                        t.div(
                            { className: "wa-contact-bottom" },
                            c.windowOpen
                                ? t.span({ className: "wa-window-dot", ariaLabel: app.attrs.tooltip("Reply window open", "right") })
                                : undefined,
                            t.span({ className: "wa-contact-last txt-hint" }, c.lastMessage),
                            c.unread ? t.span({ className: "label success wa-unread" }, "" + c.unread) : undefined,
                        ),
                    )
                );
            },
        );
    }

    function threadView() {
        return t.div({ className: "wa-thread" }, () => {
            const contact = data.active;
            if (!contact) {
                return t.p({ className: "wa-empty txt-hint" }, "Pick a conversation.");
            }

            return [
                t.div(
                    { className: "wa-thread-header" },
                    t.button(
                        {
                            type: "button",
                            className: "btn sm secondary transparent circle wa-back",
                            ariaLabel: "Back",
                            onclick: () => openContact(""),
                        },
                        t.i({ className: "ri-arrow-left-line" }),
                    ),
                    t.div(
                        { className: "flex-fill" },
                        t.strong(null, contact.name || contact.phone),
                        contact.name ? t.span({ className: "txt-hint m-l-5" }, contact.phone) : undefined,
                        contact.user && contact.userCollection
                            ? t.a(
                                {
                                    className: "link-hint m-l-10",
                                    href: `#/collections?collection=${encodeURIComponent(contact.userCollection)}&record=${encodeURIComponent(contact.user)}`,
                                },
                                t.i({ className: "ri-user-line" }),
                                " " + contact.userCollection + " record",
                            )
                            : undefined,
                    ),
                    t.span(
                        {
                            className: "label " + (contact.windowOpen ? "success" : ""),
                            ariaLabel: app.attrs.tooltip(
                                contact.windowOpen
                                    ? "The customer wrote in the last 24 hours, so any message can be sent."
                                    : "More than 24 hours since the customer last wrote: only templates can be sent.",
                                "left",
                            ),
                        },
                        contact.windowOpen ? "Window open" : "Templates only",
                    ),
                ),
                t.div({ className: "wa-messages" }, () => {
                    if (!data.thread) {
                        return t.p({ className: "wa-empty txt-hint" }, data.threadLoading ? "Loading..." : "");
                    }
                    const byMessageId = new Map(data.thread.messages.map((m) => [m.messageId, m]));
                    return data.thread.messages.map((m) => messageBubble(m, byMessageId));
                }),
                replyBox(contact),
            ];
        });
    }

    function messageBubble(m, byMessageId) {
        const quoted = m.replyTo ? byMessageId.get(m.replyTo) : null;
        return t.div(
            { className: "wa-message wa-" + m.direction },
            quoted
                ? t.div({ className: "wa-quote txt-hint" }, quoted.text || "[" + quoted.type + "]")
                : undefined,
            m.mediaId ? mediaView(m) : undefined,
            m.template
                ? t.div({ className: "wa-template txt-hint" }, t.i({ className: "ri-file-list-3-line" }), " " + m.template.split("|")[0])
                : undefined,
            m.text
                ? t.div({ className: "wa-text" }, m.text)
                : !m.mediaId && !m.template
                ? t.div({ className: "wa-text txt-hint" }, "[" + (m.type || "message") + "]")
                : undefined,
            t.div(
                { className: "wa-meta txt-hint", title: fullTime(m.sentAt || m.created) },
                shortTime(m.sentAt || m.created),
                m.direction == "out" && STATUS_ICONS[m.status]
                    ? t.i({
                        className: STATUS_ICONS[m.status] + " wa-status-" + m.status,
                        ariaLabel: app.attrs.tooltip(m.status + (m.error ? ": " + m.error : ""), "left"),
                    })
                    : undefined,
            ),
            m.error ? t.div({ className: "wa-error" }, m.error) : undefined,
        );
    }

    function mediaView(m) {
        const media = store({ url: "", error: "", loading: false });
        const isImage = (m.mediaMime || "").startsWith("image/") || m.type == "image" || m.type == "sticker";

        async function load() {
            media.loading = true;
            try {
                media.url = await mediaURL(m.mediaId);
            } catch (err) {
                media.error = err.message;
            }
            media.loading = false;
        }

        if (isImage) {
            load();
            return t.div({ className: "wa-media" }, () =>
                media.url
                    ? t.img({ src: media.url, alt: m.text || "image" })
                    : t.span({ className: "txt-hint" }, media.error || "Loading image..."));
        }

        return t.div({ className: "wa-media" }, () =>
            media.url
                ? t.a(
                    { href: media.url, download: m.mediaName || m.type, className: "link-hint" },
                    t.i({ className: "ri-download-line" }),
                    " " + (m.mediaName || "Download " + m.type),
                )
                : t.button(
                    { type: "button", className: () => "btn sm secondary " + (media.loading ? "loading" : ""), onclick: load },
                    t.i({ className: "ri-attachment-2" }),
                    t.span({ className: "txt" }, media.error || "Load " + (m.mediaName || m.type)),
                ));
    }

    function replyBox(contact) {
        return t.form(
            {
                className: "wa-reply",
                onsubmit: (e) => {
                    e.preventDefault();
                    send();
                },
            },
            t.div(
                { className: "wa-reply-modes" },
                t.button(
                    {
                        type: "button",
                        className: () => "btn sm " + (data.reply.mode == "text" ? "" : "secondary transparent"),
                        disabled: !contact.windowOpen,
                        onclick: () => setReplyMode("text"),
                    },
                    "Message",
                ),
                t.button(
                    {
                        type: "button",
                        className: () => "btn sm " + (data.reply.mode == "template" ? "" : "secondary transparent"),
                        onclick: () => setReplyMode("template"),
                    },
                    "Template",
                ),
            ),
            () =>
                data.reply.mode == "text"
                    ? t.div(
                        { className: "field" },
                        t.textarea({
                            rows: 2,
                            placeholder: "Write a reply",
                            value: () => data.reply.text,
                            oninput: (e) => (data.reply.text = e.target.value),
                            onkeydown: (e) => {
                                if (e.key == "Enter" && (e.metaKey || e.ctrlKey)) {
                                    e.preventDefault();
                                    send();
                                }
                            },
                        }),
                    )
                    : t.div(
                        { className: "wa-template-fields" },
                        t.div(
                            { className: "field" },
                            () =>
                                data.templates == null
                                    ? t.span({ className: "loader sm" })
                                    : app.components.select({
                                        options: data.templates.map((tpl) => ({
                                            value: tpl.name + "|" + tpl.language,
                                            label: tpl.name + " (" + tpl.language + ")",
                                        })),
                                        placeholder: data.templates.length ? "Pick a template" : "No approved templates",
                                        value: () => data.reply.template,
                                        onchange: (selected) => (data.reply.template = selected?.[0]?.value || ""),
                                    }),
                        ),
                        t.div(
                            { className: "field" },
                            t.textarea({
                                rows: 2,
                                placeholder: "Values for {{1}}, {{2}}… one per line",
                                value: () => data.reply.params,
                                oninput: (e) => (data.reply.params = e.target.value),
                            }),
                        ),
                    ),
            () => (data.reply.error ? t.div({ className: "field-help error" }, data.reply.error) : t.span({})),
            t.div(
                { className: "wa-reply-actions" },
                t.span(
                    { className: "txt-hint" },
                    () => (data.reply.mode == "text" ? "⌘/Ctrl + Enter to send" : "Templates can go out any time."),
                ),
                t.button(
                    { type: "submit", className: () => "btn " + (data.reply.sending ? "loading" : ""), disabled: () => data.reply.sending },
                    t.i({ className: "ri-send-plane-2-line" }),
                    t.span({ className: "txt" }, "Send"),
                ),
            ),
        );
    }
}
