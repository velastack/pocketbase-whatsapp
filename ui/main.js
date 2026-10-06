// WhatsApp auth superuser UI extension.
//
// Adds:
// - "Settings > WhatsApp" page with the sender (transport) settings
//   and the per auth collection WhatsApp auth configs;
// - a read-only "WhatsApp" summary accordion in the collection "Options" tab
//   (the collection modal saves only the core collection options so the
//   WhatsApp config is edited on the settings page).

const API = "/api/whatsapp";

const MODE_OPTIONS = [
    { label: "Off", value: "off" },
    { label: "Development (log the codes)", value: "dev" },
    { label: "VelaStack", value: "velastack" },
    { label: "WhatsApp Cloud API (direct)", value: "direct" },
];

const MODE_HELP = {
    "": "WhatsApp auth is disabled until a sender is configured.",
    dev: "The codes are written to the app logs (Logs page and console) instead of being sent. Use it only for local development.",
    velastack:
        "The codes are sent through VelaStack. No Meta business verification is required: VelaStack sends from its shared verified number, or from your own number once it is connected in the VelaStack dashboard.",
    direct:
        "The codes are sent with your own WhatsApp Business Account. Meta requires a verified business and an APPROVED authentication template with a copy code button.",
};

app.store.settingsNavGroups.System.push({
    href: "#/settings/whatsapp",
    icon: "ri-whatsapp-line",
    label: "WhatsApp",
});

app.routes.superuserOnly("#/settings/whatsapp", pageWhatsAppSettings);

document.addEventListener("mount:mfaAccordion", (e) => {
    const modal = e.detail.closest("[data-pb='collectionUpsertModal']");
    if (modal?.dataset.collectionname == "_superusers") {
        return;
    }

    e.detail.before(whatsappSummaryAccordion(modal));
});

// -------------------------------------------------------------------
// helpers
// -------------------------------------------------------------------

function apiErrors(err) {
    const result = {};

    function flatten(obj, prefix) {
        for (const key in obj || {}) {
            const val = obj[key];
            if (val && typeof val === "object" && !("message" in val)) {
                flatten(val, prefix + key + ".");
            } else if (val?.message) {
                result[prefix + key] = val.message;
            }
        }
    }

    flatten(err?.response?.data, "");

    return result;
}

function fieldError(errors, key) {
    return () => {
        const msg = errors()?.[key];
        if (msg) {
            return t.div({ className: "field-help error" }, msg);
        }
    };
}

function settingsSidebar() {
    return app.components.pageSidebar(
        { className: "settings-sidebar" },
        t.nav({ className: "sidebar-content scrollable" }, () => {
            const result = [];

            for (const groupName in app.store.settingsNavGroups) {
                const children = app.store.settingsNavGroups[groupName];

                result.push(t.details(
                    { className: "nav-group", "html-data-group": groupName, open: true },
                    t.summary(
                        { tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false },
                        groupName,
                    ),
                    () =>
                        children.map((link) => {
                            const isLocal = link.href.startsWith("#/");

                            return t.a(
                                {
                                    href: () => link.href,
                                    target: () => (!isLocal ? "_blank" : undefined),
                                    rel: () => (!isLocal ? "noopener noreferrer" : undefined),
                                    className: (el) => {
                                        const isActive = link.isActive?.(el)
                                            || app.utils.isActivePath(link.href, false);
                                        return `nav-item ${isActive ? "active" : ""}`;
                                    },
                                },
                                () => (link.icon ? t.i({ className: link.icon, ariaHidden: true }) : undefined),
                                t.span({ className: "txt" }, () => link.label),
                            );
                        }),
                ));
            }

            return result;
        }),
    );
}

// -------------------------------------------------------------------
// Settings > WhatsApp page
// -------------------------------------------------------------------

function pageWhatsAppSettings() {
    app.store.title = "WhatsApp";

    const data = store({
        isLoading: true,
        isSaving: false,
        form: null,
        initSerialized: "null",
        locked: [],
        encrypted: false,
        isDev: false,
        customTransport: false,
        errors: {},
        status: null,
        mode: "",
        isStatusLoading: false,
        testPhone: "",
        isTesting: false,
        collections: [],
        get hasChanges() {
            return data.initSerialized != JSON.stringify(data.form);
        },
    });

    load();

    async function load() {
        data.isLoading = true;

        try {
            const [settings, collections] = await Promise.all([
                app.pb.send(API + "/settings", { requestKey: "whatsapp.settings" }),
                app.pb.send(API + "/collections", { requestKey: "whatsapp.collections" }),
            ]);

            initSettings(settings);
            data.collections = collections;
            data.isLoading = false;

            loadStatus();
        } catch (err) {
            if (!err.isAbort) {
                app.checkApiError(err);
            }
        }
    }

    function initSettings(result) {
        data.form = result.settings;
        data.form.direct.languages = data.form.direct.languages || [];
        data.locked = result.locked || [];
        data.encrypted = result.encrypted;
        data.isDev = result.isDev;
        data.customTransport = result.customTransport;
        data.initSerialized = JSON.stringify(data.form);
        data.errors = {};
    }

    async function loadStatus() {
        data.isStatusLoading = true;

        try {
            const result = await app.pb.send(API + "/status", { requestKey: "whatsapp.status" });
            data.status = result.status;
            data.mode = result.mode;
        } catch (err) {
            if (!err.isAbort) {
                data.status = { ok: false, message: err.message };
            }
        }

        data.isStatusLoading = false;
    }

    async function save() {
        if (data.isSaving || !data.hasChanges) {
            return;
        }

        data.isSaving = true;

        try {
            const result = await app.pb.send(API + "/settings", {
                method: "PATCH",
                body: app.utils.filterRedactedProps(data.form),
            });
            initSettings(result);
            app.toasts.success("Successfully saved the WhatsApp settings.");
            loadStatus();
        } catch (err) {
            data.errors = apiErrors(err);
            app.toasts.error(err.message || "Failed to save the WhatsApp settings.");
        }

        data.isSaving = false;
    }

    async function sendTest() {
        if (data.isTesting || !data.testPhone) {
            return;
        }

        data.isTesting = true;

        try {
            await app.pb.send(API + "/test", { method: "POST", body: { phone: data.testPhone } });
            app.toasts.success("Test code sent to " + data.testPhone + ".");
        } catch (err) {
            app.toasts.error(err.message || "Failed to send the test code.");
        }

        data.isTesting = false;
    }

    function isLocked(field) {
        return data.locked.includes(field);
    }

    function textInput(key, label, opts = {}) {
        const [group, prop] = key.split(".");

        return [
            t.div(
                { className: "field" },
                t.label(
                    { htmlFor: "wa." + key },
                    t.span({ className: "txt" }, label),
                    () => (isLocked(key) ? t.span({ className: "label sm m-l-5" }, "env") : undefined),
                ),
                t.input({
                    id: "wa." + key,
                    name: key,
                    type: opts.secret ? "password" : "text",
                    autocomplete: opts.secret ? "new-password" : "off",
                    placeholder: () => {
                        if (opts.secret && typeof data.form[group][prop] === "undefined") {
                            return "* * * * * *";
                        }
                        return opts.placeholder || "";
                    },
                    disabled: () => isLocked(key),
                    value: () => {
                        const v = data.form[group][prop];
                        return v === "******" ? "" : v || "";
                    },
                    oninput: (e) => (data.form[group][prop] = e.target.value),
                    onfocus: () => {
                        // clear the masked secret on edit
                        if (opts.secret && data.form[group][prop] === "******") {
                            delete data.form[group][prop];
                        }
                    },
                }),
            ),
            fieldError(() => data.errors, key),
            opts.help ? t.div({ className: "field-help" }, opts.help) : undefined,
        ];
    }

    function senderForm() {
        return t.form(
            {
                className: "grid",
                inert: () => data.isSaving,
                onsubmit: (e) => {
                    e.preventDefault();
                    save();
                },
            },
            () => {
                if (data.customTransport) {
                    return t.div(
                        { className: "col-lg-12" },
                        t.div(
                            { className: "alert info" },
                            t.div(
                                { className: "content" },
                                "A custom transport is registered in the app code and it takes precedence over the settings below.",
                            ),
                        ),
                    );
                }
            },
            t.div(
                { className: "col-lg-6" },
                t.div(
                    { className: "field" },
                    t.label(
                        { htmlFor: "wa.mode" },
                        t.span({ className: "txt" }, "Sender"),
                        () => (isLocked("mode") ? t.span({ className: "label sm m-l-5" }, "env") : undefined),
                    ),
                    app.components.select({
                        id: "wa.mode",
                        name: "mode",
                        required: true,
                        options: MODE_OPTIONS,
                        disabled: () => isLocked("mode"),
                        value: () => data.form.mode || "off",
                        onchange: (selected) => {
                            const v = selected?.[0]?.value || "off";
                            data.form.mode = v == "off" ? "" : v;
                        },
                    }),
                ),
                fieldError(() => data.errors, "mode"),
            ),
            t.div(
                { className: "col-lg-12" },
                t.p({ className: "txt-hint" }, () => MODE_HELP[data.form.mode || ""]),
            ),
            // velastack
            () => {
                if (data.form.mode != "velastack") {
                    return;
                }

                return [
                    t.div(
                        { className: "col-lg-6" },
                        textInput("velastack.apiKey", "VelaStack API key", {
                            secret: true,
                            help: "Create a key for this instance in the VelaStack dashboard.",
                        }),
                    ),
                    t.div(
                        { className: "col-lg-6" },
                        textInput("velastack.relayURL", "Relay URL", {
                            placeholder: "https://velastack.dev/api/whatsapp/v1",
                        }),
                    ),
                    t.div(
                        { className: "col-lg-12" },
                        t.div(
                            { className: "field" },
                            t.input({
                                id: "wa.velastack.forwardClientIP",
                                type: "checkbox",
                                className: "switch",
                                checked: () => !!data.form.velastack.forwardClientIP,
                                onchange: (e) => (data.form.velastack.forwardClientIP = e.target.checked),
                            }),
                            t.label(
                                { htmlFor: "wa.velastack.forwardClientIP" },
                                "Forward the end user IP to VelaStack (cross-app fraud detection)",
                            ),
                        ),
                    ),
                ];
            },
            // direct
            () => {
                if (data.form.mode != "direct") {
                    return;
                }

                return [
                    t.div({ className: "col-lg-6" }, textInput("direct.phoneNumberId", "Phone number ID")),
                    t.div(
                        { className: "col-lg-6" },
                        textInput("direct.wabaId", "WhatsApp Business Account ID", {
                            help: "Optional, used to check the template approval status.",
                        }),
                    ),
                    t.div(
                        { className: "col-lg-12" },
                        textInput("direct.accessToken", "Access token", {
                            secret: true,
                            help: "A system user token with the whatsapp_business_messaging permission.",
                        }),
                    ),
                    t.div(
                        { className: "col-lg-6" },
                        textInput("direct.templateName", "Authentication template name", {
                            placeholder: "eg. login_code",
                        }),
                    ),
                    t.div(
                        { className: "col-lg-3" },
                        t.div(
                            { className: "field" },
                            t.label(
                                { htmlFor: "wa.direct.languages" },
                                t.span({ className: "txt" }, "Template languages"),
                                () => (isLocked("direct.languages")
                                    ? t.span({ className: "label sm m-l-5" }, "env")
                                    : undefined),
                            ),
                            t.input({
                                id: "wa.direct.languages",
                                type: "text",
                                placeholder: "en_US, es_MX",
                                disabled: () => isLocked("direct.languages"),
                                value: () => (data.form.direct.languages || []).join(", "),
                                onchange: (e) => {
                                    data.form.direct.languages = e.target.value
                                        .split(",")
                                        .map((v) => v.trim())
                                        .filter(Boolean);
                                },
                            }),
                        ),
                        fieldError(() => data.errors, "direct.languages"),
                    ),
                    t.div(
                        { className: "col-lg-3" },
                        textInput("direct.graphVersion", "Graph API version", { placeholder: "v24.0" }),
                    ),
                ];
            },
            () => {
                if (!data.encrypted && (data.form.mode == "velastack" || data.form.mode == "direct")) {
                    return t.div(
                        { className: "col-lg-12" },
                        t.p(
                            { className: "txt-hint txt-sm" },
                            "Tip: start the app with an encryption key (--encryptionEnv) to store the secrets encrypted.",
                        ),
                    );
                }
            },
            t.div({ className: "col-lg-12" }, t.hr()),
            t.div(
                { className: "col-lg-12" },
                t.div(
                    { className: "flex" },
                    t.div({ className: "m-r-auto" }),
                    () => {
                        if (!data.hasChanges) {
                            return;
                        }

                        return [
                            t.button(
                                {
                                    type: "button",
                                    className: "btn transparent secondary",
                                    onclick: () => {
                                        data.form = JSON.parse(data.initSerialized);
                                        data.errors = {};
                                    },
                                },
                                t.span({ className: "txt" }, "Cancel"),
                            ),
                            t.button(
                                {
                                    className: () => `btn expanded-lg ${data.isSaving ? "loading" : ""}`,
                                    disabled: () => data.isSaving,
                                },
                                t.span({ className: "txt" }, "Save changes"),
                            ),
                        ];
                    },
                ),
            ),
        );
    }

    function statusPanel() {
        return t.div(
            { className: "grid" },
            t.div(
                { className: "col-lg-12" },
                () => {
                    if (data.isStatusLoading && !data.status) {
                        return t.span({ className: "loader" });
                    }

                    const status = data.status || {};
                    const sender = status.sender;

                    return t.div(
                        { className: () => `alert ${status.ok ? "success" : "warning"}` },
                        t.div(
                            { className: "content" },
                            t.p(null, t.strong(null, status.ok ? "Ready" : "Not ready"), " ", status.message || ""),
                            sender
                                ? t.p(
                                    null,
                                    "Sender: ",
                                    t.strong(null, sender.displayName || "-"),
                                    sender.phone ? " (" + sender.phone + ")" : "",
                                    sender.kind
                                        ? " · " + (sender.kind == "shared" ? "shared number" : "own number")
                                        : "",
                                )
                                : undefined,
                            status.languages?.length
                                ? t.p(null, "Languages: " + status.languages.join(", "))
                                : undefined,
                        ),
                        t.button(
                            {
                                type: "button",
                                className: () => `btn sm transparent ${data.isStatusLoading ? "loading" : ""}`,
                                onclick: loadStatus,
                            },
                            t.i({ className: "ri-refresh-line", ariaHidden: true }),
                        ),
                    );
                },
            ),
            t.div(
                { className: "col-lg-6" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: "wa.testPhone" }, "Send a test code to"),
                    t.input({
                        id: "wa.testPhone",
                        type: "tel",
                        placeholder: "+16165550123",
                        value: () => data.testPhone,
                        oninput: (e) => (data.testPhone = e.target.value),
                    }),
                ),
            ),
            t.div(
                { className: "col-lg-6 flex" },
                t.button(
                    {
                        type: "button",
                        className: () => `btn outline m-t-auto ${data.isTesting ? "loading" : ""}`,
                        disabled: () => !data.testPhone || data.isTesting || data.hasChanges || !data.status?.ok,
                        onclick: sendTest,
                    },
                    t.i({ className: "ri-whatsapp-line", ariaHidden: true }),
                    t.span({ className: "txt" }, "Send test code"),
                ),
            ),
        );
    }

    return t.div(
        { className: "page page-whatsapp-settings" },
        settingsSidebar(),
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav(
                    { className: "breadcrumbs" },
                    t.div({ className: "breadcrumb-item" }, "Settings"),
                    t.div({ className: "breadcrumb-item" }, () => app.store.title),
                ),
            ),
            t.div(
                { className: "wrapper m-b-base" },
                () => {
                    if (data.isLoading) {
                        return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                    }

                    return [
                        t.p({ className: "txt-lg" }, "Let users sign in with a one-time code sent to their WhatsApp."),
                        t.div({ className: "section-heading" }, t.strong(null, "Sender")),
                        senderForm(),
                        t.div({ className: "section-heading m-t-base" }, t.strong(null, "Status")),
                        statusPanel(),
                        t.div({ className: "section-heading m-t-base" }, t.strong(null, "Auth collections")),
                        ...(data.collections.length
                            ? data.collections.map((item) => collectionAccordion(item))
                            : [t.p({ className: "txt-hint" }, "There are no auth collections.")]),
                    ];
                },
            ),
            t.footer({ className: "page-footer" }, app.components.credits()),
        ),
    );
}

// -------------------------------------------------------------------
// per collection config
// -------------------------------------------------------------------

function collectionAccordion(initialItem) {
    const uid = "wa_" + app.utils.randomString();

    const data = store({
        item: initialItem,
        config: JSON.parse(JSON.stringify(initialItem.config)),
        initSerialized: JSON.stringify(initialItem.config),
        isSaving: false,
        isSettingUp: false,
        errors: {},
        get hasChanges() {
            return data.initSerialized != JSON.stringify(data.config);
        },
    });

    function init(item) {
        data.item = item;
        data.config = JSON.parse(JSON.stringify(item.config));
        data.initSerialized = JSON.stringify(item.config);
        data.errors = {};
    }

    async function save() {
        if (data.isSaving) {
            return;
        }

        data.isSaving = true;

        try {
            const item = await app.pb.send(API + "/collections/" + encodeURIComponent(data.item.name), {
                method: "PATCH",
                body: data.config,
            });
            init(item);
            app.toasts.success(`Successfully saved the WhatsApp auth config for "${item.name}".`);
        } catch (err) {
            data.errors = apiErrors(err);
            app.toasts.error(err.message || "Failed to save the WhatsApp auth config.");
        }

        data.isSaving = false;
    }

    async function setup(body, successMsg) {
        if (data.isSettingUp) {
            return;
        }

        data.isSettingUp = true;

        try {
            const item = await app.pb.send(API + "/collections/" + encodeURIComponent(data.item.name) + "/setup", {
                method: "POST",
                body,
            });

            // keep the unsaved config changes
            const pending = data.config;
            init(item);
            data.config = Object.assign(pending, {
                phoneField: item.config.phoneField,
                phoneVerifiedField: item.config.phoneVerifiedField,
            });

            // refresh the cached collections list used by the collections page
            app.store.addOrUpdateCollection?.(await app.pb.collections.getOne(item.id));

            app.toasts.success(successMsg);
        } catch (err) {
            app.toasts.error(err.message || "Failed to update the collection.");
        }

        data.isSettingUp = false;
    }

    function switchField(key, label) {
        return [
            t.div(
                { className: "field" },
                t.input({
                    id: uid + "." + key,
                    type: "checkbox",
                    className: "switch",
                    checked: () => !!data.config[key],
                    onchange: (e) => (data.config[key] = e.target.checked),
                }),
                t.label({ htmlFor: uid + "." + key }, label),
            ),
            fieldError(() => data.errors, key),
        ];
    }

    function numberField(key, label) {
        return [
            t.div(
                { className: "field" },
                t.label({ htmlFor: uid + "." + key }, label),
                t.input({
                    id: uid + "." + key,
                    type: "number",
                    min: 1,
                    step: 1,
                    value: () => data.config[key] || "",
                    oninput: (e) => (data.config[key] = parseInt(e.target.value, 10) || 0),
                }),
            ),
            fieldError(() => data.errors, key),
        ];
    }

    return t.details(
        { name: "whatsapp-collections", className: "accordion" },
        t.summary(
            null,
            t.i({ className: "ri-group-line", ariaHidden: true }),
            t.span({ className: "txt" }, () => data.item.name),
            t.span({
                className: () => `label m-l-auto ${data.item.config.enabled ? "success" : ""}`,
                textContent: () => (data.item.config.enabled ? "Enabled" : "Disabled"),
            }),
        ),
        t.form(
            {
                className: "grid sm",
                inert: () => data.isSaving || data.isSettingUp,
                onsubmit: (e) => {
                    e.preventDefault();
                    save();
                },
            },
            t.div({ className: "col-sm-12" }, switchField("enabled", "Enable WhatsApp auth")),
            // phone field
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uid + ".phoneField" }, "Phone field (E.164, unique)"),
                    // recreated on fields change because the select doesn't rematch the value on options change
                    () => {
                        const options = data.item.textFields.map((f) => ({ label: f, value: f }));

                        return app.components.select({
                            id: uid + ".phoneField",
                            required: true,
                            options,
                            value: () => data.config.phoneField || "",
                            onchange: (selected) => (data.config.phoneField = selected?.[0]?.value || ""),
                        });
                    },
                ),
                fieldError(() => data.errors, "phoneField"),
            ),
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uid + ".phoneVerifiedField" }, "Phone verified field (optional)"),
                    () => {
                        const options = [{ label: "- None -", value: "none" }].concat(
                            data.item.boolFields.map((f) => ({ label: f, value: f })),
                        );

                        return app.components.select({
                            id: uid + ".phoneVerifiedField",
                            options,
                            value: () => data.config.phoneVerifiedField || "none",
                            onchange: (selected) => {
                                const v = selected?.[0]?.value || "none";
                                data.config.phoneVerifiedField = v == "none" ? "" : v;
                            },
                        });
                    },
                ),
                fieldError(() => data.errors, "phoneVerifiedField"),
            ),
            () => {
                const issue = data.item.diagnostics.phoneFieldIssue;
                if (!issue) {
                    return;
                }

                return t.div(
                    { className: "col-sm-12" },
                    t.div(
                        { className: "alert warning" },
                        t.div({ className: "content" }, issue),
                        t.button(
                            {
                                type: "button",
                                className: () => `btn sm ${data.isSettingUp ? "loading" : ""}`,
                                onclick: () =>
                                    setup(
                                        {
                                            createPhoneField: true,
                                            createPhoneVerifiedField: !data.config.phoneVerifiedField,
                                        },
                                        "Created the phone field.",
                                    ),
                            },
                            t.span({ className: "txt" }, "Create phone field"),
                        ),
                    ),
                );
            },
            t.div(
                { className: "col-sm-12" },
                switchField("allowSignup", "Allow signup (create accounts for new phone numbers)"),
            ),
            () => {
                if (!data.config.allowSignup) {
                    return;
                }

                const notes = [];

                if (data.item.diagnostics.emailRequired) {
                    notes.push(t.div(
                        { className: "alert warning" },
                        t.div(
                            { className: "content" },
                            "Phone-only accounts have no email so the email field must be optional.",
                        ),
                        t.button(
                            {
                                type: "button",
                                className: () => `btn sm ${data.isSettingUp ? "loading" : ""}`,
                                onclick: () => setup({ makeEmailOptional: true }, "The email field is now optional."),
                            },
                            t.span({ className: "txt" }, "Make email optional"),
                        ),
                    ));
                }

                if (data.item.diagnostics.createRuleLocked) {
                    notes.push(t.div(
                        { className: "alert warning" },
                        t.div(
                            { className: "content" },
                            "Signup requires the collection Create API rule to not be locked (superusers only).",
                        ),
                    ));
                }

                if (notes.length) {
                    return t.div({ className: "col-sm-12" }, notes);
                }
            },
            t.div({ className: "col-sm-4" }, numberField("codeLength", "Code length")),
            t.div({ className: "col-sm-4" }, numberField("duration", "Code duration (in seconds)")),
            t.div(
                { className: "col-sm-4" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uid + ".countries" }, "Allowed country codes"),
                    t.input({
                        id: uid + ".countries",
                        type: "text",
                        placeholder: "All countries",
                        value: () => (data.config.allowedCountryCodes || []).join(", "),
                        onchange: (e) => {
                            data.config.allowedCountryCodes = e.target.value
                                .split(",")
                                .map((v) => v.trim())
                                .filter(Boolean);
                        },
                    }),
                ),
                // the per item errors are keyed as allowedCountryCodes.{index}
                () => {
                    const msg = Object.keys(data.errors)
                        .filter((k) => k.startsWith("allowedCountryCodes"))
                        .map((k) => data.errors[k])[0];
                    if (msg) {
                        return t.div({ className: "field-help error" }, msg);
                    }
                },
                t.div(
                    { className: "field-help" },
                    "Calling codes, eg. 1, 52, 244. Messages to other countries can be up to 20x more expensive.",
                ),
            ),
            () => {
                if (data.item.diagnostics.mfaEnabled) {
                    return t.div(
                        { className: "col-sm-12" },
                        t.p(
                            { className: "txt-hint txt-sm" },
                            "MFA is enabled: WhatsApp counts as its own factor (eg. password + WhatsApp).",
                        ),
                    );
                }
            },
            t.div(
                { className: "col-sm-12 flex" },
                t.div({ className: "m-r-auto" }),
                () => {
                    if (!data.hasChanges) {
                        return;
                    }

                    return [
                        t.button(
                            {
                                type: "button",
                                className: "btn sm transparent secondary",
                                onclick: () => {
                                    data.config = JSON.parse(data.initSerialized);
                                    data.errors = {};
                                },
                            },
                            t.span({ className: "txt" }, "Cancel"),
                        ),
                        t.button(
                            { className: () => `btn sm ${data.isSaving ? "loading" : ""}` },
                            t.span({ className: "txt" }, "Save"),
                        ),
                    ];
                },
            ),
        ),
    );
}

// -------------------------------------------------------------------
// collection Options tab summary
// -------------------------------------------------------------------

function whatsappSummaryAccordion(modal) {
    const data = store({
        item: null,
        isLoading: true,
    });

    const collectionId = modal?.dataset.collectionid;

    if (collectionId) {
        app.pb.send(API + "/collections", { requestKey: "whatsapp.summary" })
            .then((items) => {
                data.item = items.find((i) => i.id == collectionId) || null;
                data.isLoading = false;
            })
            .catch(() => {
                data.isLoading = false;
            });
    } else {
        data.isLoading = false;
    }

    function openSettings(e) {
        e.preventDefault();
        app.modals.close(modal);
        window.location.hash = "#/settings/whatsapp";
    }

    return t.details(
        { name: "auth-methods", className: "accordion whatsapp-accordion" },
        t.summary(
            null,
            t.i({ className: "ri-whatsapp-line", ariaHidden: true }),
            t.span({ className: "txt", textContent: "WhatsApp" }),
            t.span({
                className: () => `label m-l-auto ${data.item?.config?.enabled ? "success" : ""}`,
                textContent: () => (data.item?.config?.enabled ? "Enabled" : "Disabled"),
            }),
        ),
        t.div(
            { className: "grid sm" },
            t.div(
                { className: "col-sm-12" },
                () => {
                    if (data.isLoading) {
                        return t.span({ className: "loader sm" });
                    }

                    if (!collectionId) {
                        return t.p({ className: "txt-hint" }, "Save the collection first to configure WhatsApp auth.");
                    }

                    return t.p(
                        null,
                        "One-time codes sent to the user's WhatsApp. ",
                        data.item?.config?.enabled
                            ? "Users are identified by the \"" + data.item.config.phoneField + "\" field. "
                            : "",
                        t.a(
                            { href: "#/settings/whatsapp", className: "link-hint", onclick: openSettings },
                            "Configure in Settings > WhatsApp",
                        ),
                        ".",
                    );
                },
            ),
        ),
    );
}
