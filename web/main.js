// WhatsApp superuser UI extension entry point (bundled into ui/main.js).
//
// Adds:
// - "Settings > WhatsApp" and the collection modal summary (settings.js);
// - the "Inbox" header link (right after "Collections") and the #/whatsapp
//   page with the incoming messages (pageInbox.js).

import "./settings";
import { pageInbox } from "./pageInbox";

document.head.appendChild(t.link({
    rel: "stylesheet",
    href: app.pb.buildURL("/_/extensions/whatsapp/inbox.css"),
}));

const collectionsIndex = app.store.headerLinks.findIndex((link) => link.href == "#/collections");
app.store.headerLinks.splice(collectionsIndex + 1, 0, {
    href: "#/whatsapp",
    icon: "ri-whatsapp-line",
    label: "Inbox",
});

app.routes.superuserOnly("#/whatsapp", pageInbox);
