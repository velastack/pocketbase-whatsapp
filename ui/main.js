(() => {
  // web/settings.js
  var API = "/api/whatsapp";
  var MODE_OPTIONS = [
    { label: "Off", value: "off" },
    { label: "Development (log the codes)", value: "dev" },
    { label: "VelaStack", value: "velastack" },
    { label: "WhatsApp Cloud API (direct)", value: "direct" }
  ];
  var MODE_HELP = {
    "": "WhatsApp auth is disabled until a sender is configured.",
    dev: "The codes are written to the app logs (Logs page and console) instead of being sent. Use it only for local development.",
    velastack: "The codes are sent through VelaStack. No Meta business verification is required: VelaStack sends from its shared verified number, or from your own number once it is connected in the VelaStack dashboard.",
    direct: "The codes are sent with your own WhatsApp Business Account. Meta requires a verified business and an APPROVED authentication template with a copy code button."
  };
  app.store.settingsNavGroups.System.push({
    href: "#/settings/whatsapp",
    icon: "ri-whatsapp-line",
    label: "WhatsApp"
  });
  app.routes.superuserOnly("#/settings/whatsapp", pageWhatsAppSettings);
  document.addEventListener("mount:mfaAccordion", (e) => {
    const modal = e.detail.closest("[data-pb='collectionUpsertModal']");
    if (modal?.dataset.collectionname == "_superusers") {
      return;
    }
    e.detail.before(whatsappSummaryAccordion(modal));
  });
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
              groupName
            ),
            () => children.map((link) => {
              const isLocal = link.href.startsWith("#/");
              return t.a(
                {
                  href: () => link.href,
                  target: () => !isLocal ? "_blank" : void 0,
                  rel: () => !isLocal ? "noopener noreferrer" : void 0,
                  className: (el) => {
                    const isActive = link.isActive?.(el) || app.utils.isActivePath(link.href, false);
                    return `nav-item ${isActive ? "active" : ""}`;
                  }
                },
                () => link.icon ? t.i({ className: link.icon, ariaHidden: true }) : void 0,
                t.span({ className: "txt" }, () => link.label)
              );
            })
          ));
        }
        return result;
      })
    );
  }
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
      }
    });
    load();
    async function load() {
      data.isLoading = true;
      try {
        const [settings, collections] = await Promise.all([
          app.pb.send(API + "/settings", { requestKey: "whatsapp.settings" }),
          app.pb.send(API + "/collections", { requestKey: "whatsapp.collections" })
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
          body: app.utils.filterRedactedProps(data.form)
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
        if (data.mode == "dev") {
          app.toasts.success("Test code for " + data.testPhone + " written to the app logs.");
        } else {
          app.toasts.success(
            "Test code for " + data.testPhone + " accepted for delivery. Delivery failures are reported only through the WhatsApp webhooks, not here."
          );
        }
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
            () => isLocked(key) ? t.span({ className: "label sm m-l-5" }, "env") : void 0
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
            oninput: (e) => data.form[group][prop] = e.target.value,
            onfocus: () => {
              if (opts.secret && data.form[group][prop] === "******") {
                delete data.form[group][prop];
              }
            }
          })
        ),
        fieldError(() => data.errors, key),
        opts.help ? t.div({ className: "field-help" }, opts.help) : void 0
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
          }
        },
        () => {
          if (data.customTransport) {
            return t.div(
              { className: "col-lg-12" },
              t.div(
                { className: "alert info" },
                t.div(
                  { className: "content" },
                  "A custom transport is registered in the app code and it takes precedence over the settings below."
                )
              )
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
              () => isLocked("mode") ? t.span({ className: "label sm m-l-5" }, "env") : void 0
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
              }
            })
          ),
          fieldError(() => data.errors, "mode")
        ),
        t.div(
          { className: "col-lg-12" },
          t.p({ className: "txt-hint" }, () => MODE_HELP[data.form.mode || ""])
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
                help: "Create a key for this instance in the VelaStack dashboard. Messages people send to the project's number show in the Inbox."
              })
            ),
            t.div(
              { className: "col-lg-6" },
              textInput("velastack.relayURL", "Relay URL", {
                placeholder: "https://velastack.dev/api/whatsapp/v1"
              })
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
                  onchange: (e) => data.form.velastack.forwardClientIP = e.target.checked
                }),
                t.label(
                  { htmlFor: "wa.velastack.forwardClientIP" },
                  "Forward the end user IP to VelaStack (cross-app fraud detection)"
                )
              )
            )
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
                help: "Recommended. Without it the template approval status can't be checked."
              })
            ),
            t.div(
              { className: "col-lg-12" },
              textInput("direct.accessToken", "Access token", {
                secret: true,
                help: "A system user token with the whatsapp_business_messaging and whatsapp_business_management permissions. Assign the Meta app to the system user first, otherwise Generate token lists no permissions."
              })
            ),
            t.div(
              { className: "col-lg-6" },
              textInput("direct.templateName", "Authentication template name", {
                placeholder: "eg. login_code"
              })
            ),
            t.div(
              { className: "col-lg-3" },
              t.div(
                { className: "field" },
                t.label(
                  { htmlFor: "wa.direct.languages" },
                  t.span({ className: "txt" }, "Template languages"),
                  () => isLocked("direct.languages") ? t.span({ className: "label sm m-l-5" }, "env") : void 0
                ),
                t.input({
                  id: "wa.direct.languages",
                  type: "text",
                  placeholder: "en_US, es_MX",
                  disabled: () => isLocked("direct.languages"),
                  value: () => (data.form.direct.languages || []).join(", "),
                  onchange: (e) => {
                    data.form.direct.languages = e.target.value.split(",").map((v) => v.trim()).filter(Boolean);
                  }
                })
              ),
              fieldError(() => data.errors, "direct.languages")
            ),
            t.div(
              { className: "col-lg-3" },
              textInput("direct.graphVersion", "Graph API version", { placeholder: "v24.0" })
            )
          ];
        },
        () => {
          if (!data.encrypted && (data.form.mode == "velastack" || data.form.mode == "direct")) {
            return t.div(
              { className: "col-lg-12" },
              t.p(
                { className: "txt-hint txt-sm" },
                "Tip: start the app with an encryption key (--encryptionEnv) to store the secrets encrypted."
              )
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
                    }
                  },
                  t.span({ className: "txt" }, "Cancel")
                ),
                t.button(
                  {
                    className: () => `btn expanded-lg ${data.isSaving ? "loading" : ""}`,
                    disabled: () => data.isSaving
                  },
                  t.span({ className: "txt" }, "Save changes")
                )
              ];
            }
          )
        )
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
              { className: () => `alert flex gap-sm ${status.ok ? "success" : "warning"}` },
              t.div(
                { className: "content flex-fill" },
                t.p(null, t.strong(null, status.ok ? "Ready" : "Not ready"), " ", status.message || ""),
                sender ? t.p(
                  null,
                  "Sender: ",
                  t.strong(null, sender.displayName || "-"),
                  sender.phone ? " (" + sender.phone + ")" : "",
                  sender.kind ? " \xB7 " + (sender.kind == "shared" ? "shared number" : "own number") : ""
                ) : void 0,
                status.languages?.length ? t.p(null, "Approved languages: " + status.languages.join(", ")) : void 0
              ),
              t.button(
                {
                  type: "button",
                  className: () => `btn sm secondary transparent circle m-l-auto ${data.isStatusLoading ? "loading" : ""}`,
                  ariaLabel: app.attrs.tooltip("Refresh", "left"),
                  onclick: loadStatus
                },
                t.i({ className: "ri-refresh-line", ariaHidden: true })
              )
            );
          }
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
              oninput: (e) => data.testPhone = e.target.value
            })
          )
        ),
        t.div(
          { className: "col-lg-6 flex" },
          t.button(
            {
              type: "button",
              className: () => `btn outline m-t-auto ${data.isTesting ? "loading" : ""}`,
              disabled: () => !data.testPhone || data.isTesting || data.hasChanges || !data.status?.ok,
              onclick: sendTest
            },
            t.i({ className: "ri-whatsapp-line", ariaHidden: true }),
            t.span({ className: "txt" }, "Send test code")
          )
        )
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
            t.div({ className: "breadcrumb-item" }, () => app.store.title)
          )
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
              ...data.collections.length ? data.collections.map((item) => collectionAccordion(item)) : [t.p({ className: "txt-hint" }, "There are no auth collections.")]
            ];
          }
        ),
        t.footer({ className: "page-footer" }, app.components.credits())
      )
    );
  }
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
      }
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
          body: data.config
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
          body
        });
        const pending = data.config;
        init(item);
        data.config = Object.assign(pending, {
          phoneField: item.config.phoneField,
          phoneVerifiedField: item.config.phoneVerifiedField
        });
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
            onchange: (e) => data.config[key] = e.target.checked
          }),
          t.label({ htmlFor: uid + "." + key }, label)
        ),
        fieldError(() => data.errors, key)
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
            oninput: (e) => data.config[key] = parseInt(e.target.value, 10) || 0
          })
        ),
        fieldError(() => data.errors, key)
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
          textContent: () => data.item.config.enabled ? "Enabled" : "Disabled"
        })
      ),
      t.form(
        {
          className: "grid sm",
          inert: () => data.isSaving || data.isSettingUp,
          onsubmit: (e) => {
            e.preventDefault();
            save();
          }
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
                onchange: (selected) => data.config.phoneField = selected?.[0]?.value || ""
              });
            }
          ),
          fieldError(() => data.errors, "phoneField")
        ),
        t.div(
          { className: "col-sm-6" },
          t.div(
            { className: "field" },
            t.label({ htmlFor: uid + ".phoneVerifiedField" }, "Phone verified field (optional)"),
            () => {
              const options = [{ label: "- None -", value: "none" }].concat(
                data.item.boolFields.map((f) => ({ label: f, value: f }))
              );
              return app.components.select({
                id: uid + ".phoneVerifiedField",
                options,
                value: () => data.config.phoneVerifiedField || "none",
                onchange: (selected) => {
                  const v = selected?.[0]?.value || "none";
                  data.config.phoneVerifiedField = v == "none" ? "" : v;
                }
              });
            }
          ),
          fieldError(() => data.errors, "phoneVerifiedField")
        ),
        () => {
          const issue = data.item.diagnostics.phoneFieldIssue;
          if (!issue) {
            return;
          }
          return t.div(
            { className: "col-sm-12" },
            t.div(
              { className: "alert warning flex gap-sm" },
              t.div({ className: "content flex-fill" }, issue),
              t.button(
                {
                  type: "button",
                  className: () => `btn sm m-l-auto ${data.isSettingUp ? "loading" : ""}`,
                  onclick: () => setup(
                    {
                      createPhoneField: true,
                      createPhoneVerifiedField: !data.config.phoneVerifiedField
                    },
                    "Created the phone field."
                  )
                },
                t.span({ className: "txt" }, "Create phone field")
              )
            )
          );
        },
        t.div(
          { className: "col-sm-12" },
          switchField("allowSignup", "Allow signup (create accounts for new phone numbers)")
        ),
        () => {
          if (!data.config.allowSignup) {
            return;
          }
          const notes = [];
          if (data.item.diagnostics.emailRequired) {
            notes.push(t.div(
              { className: "alert warning flex gap-sm" },
              t.div(
                { className: "content flex-fill" },
                "Phone-only accounts have no email so the email field must be optional."
              ),
              t.button(
                {
                  type: "button",
                  className: () => `btn sm m-l-auto ${data.isSettingUp ? "loading" : ""}`,
                  onclick: () => setup({ makeEmailOptional: true }, "The email field is now optional.")
                },
                t.span({ className: "txt" }, "Make email optional")
              )
            ));
          }
          if (data.item.diagnostics.createRuleLocked) {
            notes.push(t.div(
              { className: "alert warning" },
              t.div(
                { className: "content" },
                "Signup requires the collection Create API rule to not be locked (superusers only)."
              )
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
                data.config.allowedCountryCodes = e.target.value.split(",").map((v) => v.trim()).filter(Boolean);
              }
            })
          ),
          // the per item errors are keyed as allowedCountryCodes.{index}
          () => {
            const msg = Object.keys(data.errors).filter((k) => k.startsWith("allowedCountryCodes")).map((k) => data.errors[k])[0];
            if (msg) {
              return t.div({ className: "field-help error" }, msg);
            }
          },
          t.div(
            { className: "field-help" },
            "Calling codes, eg. 1, 52, 244. Messages to other countries can be up to 20x more expensive."
          )
        ),
        () => {
          if (data.item.diagnostics.mfaEnabled) {
            return t.div(
              { className: "col-sm-12" },
              t.p(
                { className: "txt-hint txt-sm" },
                "MFA is enabled: WhatsApp counts as its own factor (eg. password + WhatsApp)."
              )
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
                  }
                },
                t.span({ className: "txt" }, "Cancel")
              ),
              t.button(
                { className: () => `btn sm ${data.isSaving ? "loading" : ""}` },
                t.span({ className: "txt" }, "Save")
              )
            ];
          }
        )
      )
    );
  }
  function whatsappSummaryAccordion(modal) {
    const data = store({
      item: null,
      isLoading: true
    });
    const collectionId = modal?.dataset.collectionid;
    if (collectionId) {
      app.pb.send(API + "/collections", { requestKey: "whatsapp.summary" }).then((items) => {
        data.item = items.find((i) => i.id == collectionId) || null;
        data.isLoading = false;
      }).catch(() => {
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
          textContent: () => data.item?.config?.enabled ? "Enabled" : "Disabled"
        })
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
              data.item?.config?.enabled ? 'Users are identified by the "' + data.item.config.phoneField + '" field. ' : "",
              t.a(
                { href: "#/settings/whatsapp", className: "link-hint", onclick: openSettings },
                "Configure in Settings > WhatsApp"
              ),
              "."
            );
          }
        )
      )
    );
  }

  // web/inboxUtils.js
  function startPolling(fn, interval) {
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
  function parseDate(value) {
    if (!value) {
      return null;
    }
    const date = new Date(value.replace(" ", "T"));
    return isNaN(date) ? null : date;
  }
  function shortTime(value) {
    const date = parseDate(value);
    if (!date) {
      return "";
    }
    const now = /* @__PURE__ */ new Date();
    if (date.toDateString() == now.toDateString()) {
      return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    }
    return date.toLocaleDateString([], {
      month: "short",
      day: "numeric",
      year: date.getFullYear() == now.getFullYear() ? void 0 : "numeric"
    });
  }
  function fullTime(value) {
    const date = parseDate(value);
    return date ? date.toLocaleString() : "";
  }
  var mediaCache = /* @__PURE__ */ new Map();
  async function mediaURL(mediaId) {
    if (!mediaCache.has(mediaId)) {
      const promise = fetch(app.pb.buildURL("/api/whatsapp/inbox/media/" + encodeURIComponent(mediaId)), {
        headers: { Authorization: app.pb.authStore.token }
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
  var STATUS_ICONS = {
    accepted: "ri-time-line",
    sent: "ri-check-line",
    delivered: "ri-check-double-line",
    read: "ri-check-double-line",
    failed: "ri-error-warning-line"
  };

  // web/pageInbox.js
  var API2 = "/api/whatsapp/inbox";
  var POLL_INTERVAL = 5e3;
  var CONTACT_QUERY_KEY = "contact";
  function displayName(c) {
    return c.savedName || c.name || c.phone;
  }
  function otherName(c) {
    return c.savedName && c.name && c.name != c.savedName ? c.name : "";
  }
  function bizAppNotice(incoming) {
    if (!incoming?.onBizApp) {
      return "";
    }
    let text = "This number is also used in the WhatsApp Business app. Replies sent there show up here.";
    const history = incoming.history;
    if (history?.error == "declined") {
      text += " The chat history wasn't shared from the app.";
    } else if (history && history.progress < 100) {
      text += ` Importing the chat history (${history.progress}%).`;
    }
    return text;
  }
  var INCOMING_HINTS = {
    webhook: "VelaStack delivers new messages to this app's webhook.",
    polling: "This app isn't reachable over public https, so new messages are fetched from VelaStack every few seconds.",
    "": "Incoming messages need the VelaStack sender. Set it up in Settings > WhatsApp."
  };
  function pageInbox(route) {
    app.store.title = "Inbox";
    const data = store({
      contacts: [],
      incoming: null,
      loading: true,
      activeId: route.query[CONTACT_QUERY_KEY]?.[0] || "",
      thread: null,
      // {contact, messages}
      threadLoading: false,
      templates: null,
      reply: {
        mode: "text",
        // text | template
        text: "",
        template: "",
        params: "",
        sending: false,
        error: ""
      },
      get active() {
        return data.contacts.find((c) => c.id == data.activeId) || data.thread?.contact || null;
      }
    });
    const uid = app.utils.randomString(6);
    const contactsKey = "wa_inbox_" + uid;
    const threadKey = "wa_thread_" + uid;
    async function loadContacts(silent = false) {
      try {
        const res = await app.pb.send(API2, { method: "GET", requestKey: contactsKey });
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
        const res = await app.pb.send(`${API2}/${encodeURIComponent(id)}`, { method: "GET", requestKey: threadKey });
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
        await app.pb.send(`${API2}/${encodeURIComponent(contact.id)}/read`, { method: "POST" });
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
        const res = await app.pb.send(`${API2}/templates`, { method: "GET" });
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
        const params = data.reply.params.split("\n").map((line) => line.trim()).filter(Boolean);
        body = { template: { name, language, params } };
      }
      data.reply.sending = true;
      data.reply.error = "";
      try {
        await app.pb.send(`${API2}/${encodeURIComponent(contact.id)}`, { method: "POST", body });
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
        }
      },
      t.div(
        { className: "page-content full-height" },
        t.header(
          { className: "page-header" },
          t.nav({ className: "breadcrumbs" }, t.div({ className: "breadcrumb-item" }, "WhatsApp inbox")),
          t.div({ className: "flex-fill" }),
          () => data.incoming ? t.span(
            {
              className: () => "label " + (data.incoming.error ? "warning" : data.incoming.mode ? "success" : ""),
              ariaLabel: app.attrs.tooltip(data.incoming.error || INCOMING_HINTS[data.incoming.mode] || "", "left")
            },
            data.incoming.mode == "webhook" ? "Webhook" : data.incoming.mode == "polling" ? "Polling" : "Not receiving"
          ) : t.span({}),
          t.button(
            {
              type: "button",
              className: "btn secondary",
              onclick: () => {
                loadContacts();
                loadThread();
              }
            },
            t.i({ className: "ri-refresh-line" }),
            t.span({ className: "txt" }, "Refresh")
          )
        ),
        () => data.incoming && !data.incoming.mode ? t.div({ className: "alert warning m-b-sm" }, t.div({ className: "content" }, INCOMING_HINTS[""])) : data.incoming?.error ? t.div({ className: "alert warning m-b-sm" }, t.div({ className: "content" }, data.incoming.error)) : t.span({}),
        () => bizAppNotice(data.incoming) ? t.div(
          { className: "alert info m-b-sm" },
          t.div({ className: "icon" }, t.i({ className: "ri-smartphone-line" })),
          t.div({ className: "content" }, bizAppNotice(data.incoming))
        ) : t.span({}),
        t.div(
          { className: () => "wa-inbox " + (data.activeId ? "wa-inbox-thread-open" : "") },
          contactList(),
          threadView()
        )
      )
    );
    function contactList() {
      return t.div(
        { className: "wa-contacts" },
        () => {
          if (!data.contacts.length) {
            return t.p(
              { className: "wa-empty txt-hint" },
              data.loading ? "Loading..." : "No conversations yet. Messages people send to the number show up here."
            );
          }
          return data.contacts.map(
            (c) => t.button(
              {
                type: "button",
                className: () => "wa-contact " + (c.id == data.activeId ? "active" : ""),
                onclick: () => openContact(c.id)
              },
              t.div(
                { className: "wa-contact-top" },
                t.span({ className: "wa-contact-name" }, displayName(c)),
                t.span({ className: "wa-contact-time txt-hint" }, shortTime(c.lastMessageAt))
              ),
              t.div(
                { className: "wa-contact-bottom" },
                c.windowOpen ? t.span({ className: "wa-window-dot", ariaLabel: app.attrs.tooltip("Reply window open", "right") }) : void 0,
                t.span({ className: "wa-contact-last txt-hint" }, c.lastMessage),
                c.unread ? t.span({ className: "label success wa-unread" }, "" + c.unread) : void 0
              )
            )
          );
        }
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
                onclick: () => openContact("")
              },
              t.i({ className: "ri-arrow-left-line" })
            ),
            t.div(
              { className: "flex-fill" },
              t.strong(null, displayName(contact)),
              otherName(contact) ? t.span({ className: "txt-hint m-l-5" }, "~" + otherName(contact)) : void 0,
              displayName(contact) != contact.phone ? t.span({ className: "txt-hint m-l-5" }, contact.phone) : void 0,
              contact.user && contact.userCollection ? t.a(
                {
                  className: "link-hint m-l-10",
                  href: `#/collections?collection=${encodeURIComponent(contact.userCollection)}&record=${encodeURIComponent(contact.user)}`
                },
                t.i({ className: "ri-user-line" }),
                " " + contact.userCollection + " record"
              ) : void 0
            ),
            t.span(
              {
                className: "label " + (contact.windowOpen ? "success" : ""),
                ariaLabel: app.attrs.tooltip(
                  contact.windowOpen ? "The customer wrote in the last 24 hours, so any message can be sent." : "More than 24 hours since the customer last wrote: only templates can be sent.",
                  "left"
                )
              },
              contact.windowOpen ? "Window open" : "Templates only"
            )
          ),
          t.div({ className: "wa-messages" }, () => {
            if (!data.thread) {
              return t.p({ className: "wa-empty txt-hint" }, data.threadLoading ? "Loading..." : "");
            }
            const byMessageId = new Map(data.thread.messages.map((m) => [m.messageId, m]));
            const items = [];
            let inHistory = false;
            for (const m of data.thread.messages) {
              const isHistory = m.origin == "history";
              if (isHistory && !inHistory) {
                items.push(t.div({ className: "wa-divider txt-hint" }, "Imported from the WhatsApp Business app"));
              } else if (!isHistory && inHistory) {
                items.push(t.div({ className: "wa-divider txt-hint" }, "Since connecting"));
              }
              inHistory = isHistory;
              items.push(messageBubble(m, byMessageId));
            }
            return items;
          }),
          replyBox(contact)
        ];
      });
    }
    function messageBubble(m, byMessageId) {
      const quoted = m.replyTo ? byMessageId.get(m.replyTo) : null;
      return t.div(
        { className: "wa-message wa-" + m.direction + (m.origin == "history" ? " wa-history" : "") },
        quoted ? t.div({ className: "wa-quote txt-hint" }, quoted.text || "[" + quoted.type + "]") : void 0,
        m.mediaId ? mediaView(m) : void 0,
        m.template ? t.div({ className: "wa-template txt-hint" }, t.i({ className: "ri-file-list-3-line" }), " " + m.template.split("|")[0]) : void 0,
        m.text ? t.div({ className: "wa-text" }, m.text) : !m.mediaId && !m.template ? t.div({ className: "wa-text txt-hint" }, "[" + (m.type || "message") + "]") : void 0,
        t.div(
          { className: "wa-meta txt-hint", title: fullTime(m.sentAt || m.created) },
          m.origin == "app" ? t.i({
            className: "ri-smartphone-line",
            ariaLabel: app.attrs.tooltip("Sent from the WhatsApp Business app", "left")
          }) : void 0,
          shortTime(m.sentAt || m.created),
          m.direction == "out" && STATUS_ICONS[m.status] ? t.i({
            className: STATUS_ICONS[m.status] + " wa-status-" + m.status,
            ariaLabel: app.attrs.tooltip(m.status + (m.error ? ": " + m.error : ""), "left")
          }) : void 0
        ),
        m.error ? t.div({ className: "wa-error" }, m.error) : void 0
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
        return t.div({ className: "wa-media" }, () => media.url ? t.img({ src: media.url, alt: m.text || "image" }) : t.span({ className: "txt-hint" }, media.error || "Loading image..."));
      }
      return t.div({ className: "wa-media" }, () => media.url ? t.a(
        { href: media.url, download: m.mediaName || m.type, className: "link-hint" },
        t.i({ className: "ri-download-line" }),
        " " + (m.mediaName || "Download " + m.type)
      ) : t.button(
        { type: "button", className: () => "btn sm secondary " + (media.loading ? "loading" : ""), onclick: load },
        t.i({ className: "ri-attachment-2" }),
        t.span({ className: "txt" }, media.error || "Load " + (m.mediaName || m.type))
      ));
    }
    function replyBox(contact) {
      return t.form(
        {
          className: "wa-reply",
          onsubmit: (e) => {
            e.preventDefault();
            send();
          }
        },
        t.div(
          { className: "wa-reply-modes" },
          t.button(
            {
              type: "button",
              className: () => "btn sm " + (data.reply.mode == "text" ? "" : "secondary transparent"),
              disabled: !contact.windowOpen,
              onclick: () => setReplyMode("text")
            },
            "Message"
          ),
          t.button(
            {
              type: "button",
              className: () => "btn sm " + (data.reply.mode == "template" ? "" : "secondary transparent"),
              onclick: () => setReplyMode("template")
            },
            "Template"
          )
        ),
        () => data.reply.mode == "text" ? t.div(
          { className: "field" },
          t.textarea({
            rows: 2,
            placeholder: "Write a reply",
            value: () => data.reply.text,
            oninput: (e) => data.reply.text = e.target.value,
            onkeydown: (e) => {
              if (e.key == "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                send();
              }
            }
          })
        ) : t.div(
          { className: "wa-template-fields" },
          t.div(
            { className: "field" },
            () => data.templates == null ? t.span({ className: "loader sm" }) : app.components.select({
              options: data.templates.map((tpl) => ({
                value: tpl.name + "|" + tpl.language,
                label: tpl.name + " (" + tpl.language + ")"
              })),
              placeholder: data.templates.length ? "Pick a template" : "No approved templates",
              value: () => data.reply.template,
              onchange: (selected) => data.reply.template = selected?.[0]?.value || ""
            })
          ),
          t.div(
            { className: "field" },
            t.textarea({
              rows: 2,
              placeholder: "Values for {{1}}, {{2}}\u2026 one per line",
              value: () => data.reply.params,
              oninput: (e) => data.reply.params = e.target.value
            })
          )
        ),
        () => data.reply.error ? t.div({ className: "field-help error" }, data.reply.error) : t.span({}),
        t.div(
          { className: "wa-reply-actions" },
          t.span(
            { className: "txt-hint" },
            () => data.reply.mode == "text" ? "\u2318/Ctrl + Enter to send" : "Templates can go out any time."
          ),
          t.button(
            { type: "submit", className: () => "btn " + (data.reply.sending ? "loading" : ""), disabled: () => data.reply.sending },
            t.i({ className: "ri-send-plane-2-line" }),
            t.span({ className: "txt" }, "Send")
          )
        )
      );
    }
  }

  // web/main.js
  document.head.appendChild(t.link({
    rel: "stylesheet",
    href: app.pb.buildURL("/_/extensions/whatsapp/inbox.css")
  }));
  var collectionsIndex = app.store.headerLinks.findIndex((link) => link.href == "#/collections");
  app.store.headerLinks.splice(collectionsIndex + 1, 0, {
    href: "#/whatsapp",
    icon: "ri-whatsapp-line",
    label: "Inbox"
  });
  app.routes.superuserOnly("#/whatsapp", pageInbox);
})();
