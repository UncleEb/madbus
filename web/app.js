// Madbus home screen: list registered devices + connection status, and manage
// them (add / edit / remove) via the /api/v1/devices endpoints.
const REFRESH_MS = 4000;

const list = document.getElementById("device-list");
const empty = document.getElementById("empty");
const banner = document.getElementById("banner");

const dialog = document.getElementById("device-dialog");
const form = document.getElementById("device-form");
const formMsg = document.getElementById("form-msg");
const dialogTitle = document.getElementById("dialog-title");
const f = {
  id: document.getElementById("d-id"),
  name: document.getElementById("d-name"),
  profile: document.getElementById("d-profile"),
  unit: document.getElementById("d-unit"),
  poll: document.getElementById("d-poll"),
  port: document.getElementById("d-port"),
  baud: document.getElementById("d-baud"),
  parity: document.getElementById("d-parity"),
  databits: document.getElementById("d-databits"),
  stopbits: document.getElementById("d-stopbits"),
};

let devicesById = {};
let editingId = null; // null = creating

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

// ---- device list ----

function renderDevices(devices) {
  devicesById = {};
  list.textContent = "";
  if (!devices || devices.length === 0) {
    empty.hidden = false;
    return;
  }
  empty.hidden = true;

  for (const d of devices) {
    devicesById[d.id] = d;
    const online = !!d.online;

    const card = el("div", "device");
    const row = el("div", "device-row");

    const left = el("div", "device-left");
    left.appendChild(el("span", "status-dot " + (online ? "is-online" : "is-offline")));
    const info = el("div");
    info.appendChild(el("div", "device-name", d.name || d.id));
    const meta = el("div", "device-meta");
    if (d.category) meta.appendChild(el("span", "badge", d.category));
    meta.appendChild(el("span", null, d.serial && d.serial.port ? d.serial.port + " · unit " + d.unit_id : d.profile));
    info.appendChild(meta);
    left.appendChild(info);

    const status = el("div", "device-status");
    status.appendChild(el("div", "status-label " + (online ? "is-online" : "is-offline"), online ? "Online" : "Offline"));
    status.appendChild(el("div", "status-sub", (online ? "updated " : "last seen ") + fmtTime(d.last_read)));

    const actions = el("div", "device-actions");
    const edit = el("button", "icon-btn", "Edit");
    edit.addEventListener("click", () => openDialog(d.id));
    const del = el("button", "icon-btn is-danger", "Delete");
    del.addEventListener("click", () => removeDevice(d));
    actions.appendChild(edit);
    actions.appendChild(del);

    row.appendChild(left);
    row.appendChild(status);
    row.appendChild(actions);
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

// ---- add / edit dialog ----

async function loadProfileOptions(selected) {
  f.profile.textContent = "";
  try {
    const res = await fetch("/api/v1/profiles", { cache: "no-store" });
    const data = await res.json();
    for (const p of data.profiles || []) {
      const opt = el("option", null, p.name ? p.name + " (" + p.id + ")" : p.id);
      opt.value = p.id;
      if (p.id === selected) opt.selected = true;
      f.profile.appendChild(opt);
    }
  } catch (e) {
    /* leave empty; save will surface the error */
  }
}

async function openDialog(id) {
  editingId = id || null;
  formMsg.hidden = true;
  const d = id ? devicesById[id] : null;

  dialogTitle.textContent = d ? "Edit device" : "Add device";
  f.id.value = d ? d.id : "";
  f.id.disabled = !!d; // id is immutable once created
  f.name.value = d ? d.name : "";
  f.unit.value = d ? d.unit_id : 1;
  f.poll.value = d && d.poll_interval_seconds ? d.poll_interval_seconds : "";
  const s = (d && d.serial) || {};
  f.baud.value = s.baud || 9600;
  f.parity.value = s.parity || "none";
  f.databits.value = s.data_bits || 8;
  f.stopbits.value = s.stop_bits || 1;

  await loadProfileOptions(d ? d.profile : null);
  await loadSerialPorts(s.port || null);
  dialog.showModal();
}

function addOption(sel, value, text) {
  const o = document.createElement("option");
  o.value = value;
  o.textContent = text;
  sel.appendChild(o);
}

function toggleSerialSettings() {
  document.getElementById("serial-settings").hidden = f.port.value === "mock";
}

async function loadSerialPorts(selected) {
  f.port.textContent = "";
  let ports = [];
  try {
    const res = await fetch("/api/v1/serial-ports", { cache: "no-store" });
    ports = (await res.json()).ports || [];
  } catch (e) {
    /* no hardware detected; Mock stays available */
  }
  addOption(f.port, "mock", "Mock (no hardware)");
  for (const p of ports) addOption(f.port, p.path, p.description || p.path);
  // Keep a saved-but-unplugged interface selectable so editing doesn't drop it.
  if (selected && selected !== "mock" && !ports.some((p) => p.path === selected)) {
    addOption(f.port, selected, selected + " (not detected)");
  }
  f.port.value = selected || (ports[0] ? ports[0].path : "mock");
  toggleSerialSettings();
}

document.getElementById("add-device").addEventListener("click", () => openDialog(null));
document.getElementById("dialog-cancel").addEventListener("click", () => dialog.close());
document.getElementById("rescan-ports").addEventListener("click", () => loadSerialPorts(f.port.value));
f.port.addEventListener("change", toggleSerialSettings);

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = {
    id: f.id.value.trim(),
    name: f.name.value.trim(),
    profile: f.profile.value,
    unit_id: parseInt(f.unit.value, 10) || 0,
    poll_interval_seconds: f.poll.value ? parseInt(f.poll.value, 10) : 0,
    serial: {
      port: f.port.value.trim(),
      baud: parseInt(f.baud.value, 10) || 0,
      data_bits: parseInt(f.databits.value, 10) || 0,
      parity: f.parity.value,
      stop_bits: parseInt(f.stopbits.value, 10) || 0,
    },
  };

  const url = editingId ? "/api/v1/devices/" + encodeURIComponent(editingId) : "/api/v1/devices";
  const method = editingId ? "PUT" : "POST";

  let res, data;
  try {
    res = await fetch(url, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    data = await res.json().catch(() => null);
  } catch (err) {
    showFormMsg("Couldn't reach the Madbus server.");
    return;
  }
  if (!res.ok) {
    showFormMsg((data && data.error) || "Save failed (HTTP " + res.status + ")");
    return;
  }
  dialog.close();
  refresh();
});

function showFormMsg(text) {
  formMsg.textContent = text;
  formMsg.className = "form-msg is-err";
  formMsg.hidden = false;
}

async function removeDevice(d) {
  if (!confirm('Remove device "' + (d.name || d.id) + '"?')) return;
  try {
    const res = await fetch("/api/v1/devices/" + encodeURIComponent(d.id), { method: "DELETE" });
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      alert((data && data.error) || "Delete failed (HTTP " + res.status + ")");
      return;
    }
    refresh();
  } catch (e) {
    alert("Couldn't reach the Madbus server.");
  }
}

refresh();
setInterval(refresh, REFRESH_MS);
