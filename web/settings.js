// Madbus settings page: read/write GET+PUT /api/v1/settings.
const form = document.getElementById("settings-form");
const poll = document.getElementById("poll");
const debug = document.getElementById("debug");
const addr = document.getElementById("addr");
const msg = document.getElementById("msg");

function show(text, ok) {
  msg.textContent = text;
  msg.className = "form-msg " + (ok ? "is-ok" : "is-err");
  msg.hidden = false;
}

function fill(s) {
  poll.value = s.poll_interval_seconds;
  debug.checked = !!s.debug;
  addr.value = s.http_addr || "";
}

async function loadSettings() {
  try {
    const res = await fetch("/api/v1/settings", { cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    fill(await res.json());
  } catch (e) {
    show("Couldn't load settings: " + e.message, false);
  }
}

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = {
    poll_interval_seconds: parseInt(poll.value, 10),
    debug: debug.checked,
    http_addr: addr.value.trim(),
  };

  let res, data;
  try {
    res = await fetch("/api/v1/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    data = await res.json().catch(() => null);
  } catch (err) {
    show("Couldn't reach the Madbus server.", false);
    return;
  }

  if (!res.ok) {
    // Server rejected (e.g. listen port already in use) — nothing was saved.
    show((data && data.error) || "Save failed (HTTP " + res.status + ")", false);
    return;
  }

  fill(data);
  show("Settings saved.", true);
});

loadSettings();
