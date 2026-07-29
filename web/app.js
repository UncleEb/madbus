// Madbus home screen: list registered devices + connection status.
// Reads GET /api/v1/devices only — identity and reachability, no telemetry.
const REFRESH_MS = 4000;

const list = document.getElementById("device-list");
const empty = document.getElementById("empty");
const banner = document.getElementById("banner");

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function fmtTime(iso) {
  if (!iso) return "never";
  const d = new Date(iso);
  return isNaN(d) ? "unknown" : d.toLocaleTimeString();
}

function renderDevices(devices) {
  list.textContent = "";
  if (!devices || devices.length === 0) {
    empty.hidden = false;
    return;
  }
  empty.hidden = true;

  for (const d of devices) {
    const online = !!d.online;
    const card = el("div", "device");
    const row = el("div", "device-row");

    const left = el("div", "device-left");
    left.appendChild(el("span", "status-dot " + (online ? "is-online" : "is-offline")));
    const info = el("div");
    info.appendChild(el("div", "device-name", d.name || d.id));
    const meta = el("div", "device-meta");
    if (d.category) meta.appendChild(el("span", "badge", d.category));
    if (d.profile) meta.appendChild(el("span", null, d.profile));
    info.appendChild(meta);
    left.appendChild(info);

    const right = el("div", "device-right");
    right.appendChild(el("div", "status-label " + (online ? "is-online" : "is-offline"), online ? "Online" : "Offline"));
    right.appendChild(el("div", "status-sub", (online ? "updated " : "last seen ") + fmtTime(d.last_read)));

    row.appendChild(left);
    row.appendChild(right);
    card.appendChild(row);

    if (!online && d.last_error) {
      const err = el("div", "device-error");
      err.appendChild(el("span", "device-error-label", "offline: "));
      err.appendChild(document.createTextNode(d.last_error));
      card.appendChild(err);
    }

    list.appendChild(card);
  }
}

async function refresh() {
  try {
    const res = await fetch("/api/v1/devices", { cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    banner.hidden = true;
    renderDevices(data.devices || []);
  } catch (e) {
    banner.hidden = false;
    banner.textContent = "Cannot reach the Madbus server. Retrying…";
  }
}

refresh();
setInterval(refresh, REFRESH_MS);
