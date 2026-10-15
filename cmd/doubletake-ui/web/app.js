// Doubletake UI: polls the local server for the doubletake session state and
// sends the user's choices (receiver, PIN, capture source) back.
"use strict";

const POLL_MS = 700;
const STATE_LABELS = {
  idle: "Idle",
  discovering: "Searching",
  choose: "Choose",
  pin: "PIN",
  connecting: "Connecting",
  streaming: "Casting",
  error: "Error",
};

const stateEl = document.getElementById("state");
const statusEl = document.getElementById("status");
const devicesEl = document.getElementById("devices");
const pinForm = document.getElementById("pin-form");
const pinInput = document.getElementById("pin-input");
const refreshBtn = document.getElementById("refresh");
const stopBtn = document.getElementById("stop");
const sourceBtns = [...document.querySelectorAll(".source")];

let lastDevicesKey = "";

async function post(path, body = {}) {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok && response.status !== 409) throw new Error(await response.text());
  return response.json();
}

function renderDevices(status) {
  const choosable = status.state === "choose";
  const key = `${choosable}|${status.devices.map((d) => `${d.index}:${d.name}:${d.ip}`).join(",")}`;
  if (key === lastDevicesKey) return;
  lastDevicesKey = key;
  devicesEl.innerHTML = "";
  // doubletake lists by IP; names are easier to scan.
  const sorted = [...status.devices].sort((a, b) =>
    a.name.localeCompare(b.name, undefined, { numeric: true }),
  );
  for (const device of sorted) {
    const li = document.createElement("li");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn";
    btn.disabled = !choosable;
    const name = document.createElement("span");
    name.textContent = device.name;
    const meta = document.createElement("span");
    meta.className = "device-meta";
    meta.textContent = `${device.model} · ${device.ip}`;
    btn.append(name, meta);
    btn.addEventListener("click", () => run(() => post("/api/select", { index: device.index })));
    li.appendChild(btn);
    devicesEl.appendChild(li);
  }
}

function render(status) {
  const busy = ["pin", "connecting", "streaming"].includes(status.state);
  const active = status.state !== "idle" && status.state !== "error";
  stateEl.textContent = STATE_LABELS[status.state] ?? status.state;
  stateEl.className = `badge ${status.state}`;
  statusEl.textContent = status.message || (active ? "Working…" : "Press Refresh to search for receivers.");
  document.title = status.state === "streaming" ? "Doubletake – Casting" : "Doubletake";
  renderDevices(status);

  for (const btn of sourceBtns) {
    btn.setAttribute("aria-checked", String(btn.dataset.source === status.source));
    btn.disabled = busy;
  }

  const wantsPin = status.state === "pin";
  if (wantsPin && pinForm.classList.contains("hidden")) {
    pinForm.classList.remove("hidden");
    pinInput.value = "";
    pinInput.focus();
  } else if (!wantsPin) {
    pinForm.classList.add("hidden");
  }
  stopBtn.disabled = !active;
}

async function run(action) {
  try {
    render(await action());
  } catch (err) {
    statusEl.textContent = `Error: ${err.message || err}`;
  }
}

async function poll() {
  try {
    const response = await fetch("/api/status", { cache: "no-store" });
    if (response.ok) render(await response.json());
  } catch {
    statusEl.textContent = "Doubletake UI is not running. Close this window.";
  }
  window.setTimeout(poll, POLL_MS);
}

refreshBtn.addEventListener("click", () => {
  lastDevicesKey = "";
  run(() => post("/api/refresh"));
});
stopBtn.addEventListener("click", () => run(() => post("/api/stop")));
for (const btn of sourceBtns) {
  btn.addEventListener("click", () => {
    lastDevicesKey = "";
    run(() => post("/api/source", { source: btn.dataset.source }));
  });
}
pinForm.addEventListener("submit", (ev) => {
  ev.preventDefault();
  const code = pinInput.value.trim();
  if (code) run(() => post("/api/pin", { code }));
});

poll();
