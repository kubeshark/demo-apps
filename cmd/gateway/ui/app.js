"use strict";

const VARIANTS = [
  { id: "ok", label: "Run", cls: "primary" },
  { id: "slow", label: "Run slow", cls: "" },
  { id: "error", label: "Run error", cls: "danger" },
];

const KINDS = {
  keep: { title: "Stays visible", note: "with or without redaction" },
  redact: { title: "Redacted", note: "replaced with [REDACTED] when redaction is on" },
  masked: { title: "Masked form", note: "what a redacted card number looks like" },
};

const state = { flows: [], details: {}, running: {}, lastOK: 0 };

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

const ms = (ns) => {
  const v = ns / 1e6;
  return v >= 1000 ? (v / 1000).toFixed(1) + " s" : v.toFixed(0) + " ms";
};

function ago(iso) {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  return Math.floor(s / 3600) + "h ago";
}

function status(run) {
  if (!run) return "idle";
  return run.error ? "failed" : "ok";
}

// An error run that produced its designed error succeeded; say so rather than
// a bare "ok" next to an error.
function label(run) {
  const st = status(run);
  return st === "ok" && run.variant === "error" ? "error as designed" : st;
}

function hops(path) {
  return path.split("->").map((p) => p.trim()).filter(Boolean);
}

// The run output as plain text lines, so the "All" grid and the detail page
// show exactly the same thing.
function outputLines(run) {
  if (!run) return ["No runs yet.", "Press Run to send traffic through this flow."];
  const lines = [
    `${run.variant.padEnd(5)} via ${run.source}  ${ms(run.duration)}  ${new Date(run.started).toLocaleTimeString()}`,
    "",
  ];
  const width = Math.max(4, ...(run.steps || []).map((s) => s.name.length));
  for (const s of run.steps || []) {
    const head = `${s.protocol.padEnd(5)} ${s.name.padEnd(width)} ${ms(s.duration).padStart(7)}`;
    lines.push(s.error ? `${head}  ERROR ${s.error}` : `${head}  ${s.detail || ""}`);
  }
  if (run.error) lines.push("", `FAILED ${run.error}`);
  return lines;
}

function runButtons(name, big) {
  return h("div", { class: "actions" + (big ? " big" : "") },
    VARIANTS.map((v) => {
      const busy = state.running[name] === v.id;
      return h("button", {
        class: `btn ${v.cls}${busy ? " busy" : ""}`,
        disabled: Boolean(state.running[name]),
        onclick: () => run(name, v.id),
      }, busy ? "Running" : v.label);
    }));
}

function statusPill(run, name) {
  if (state.running[name]) return h("span", { class: "pill running" }, "running");
  const st = status(run);
  if (st === "idle") return h("span", { class: "pill idle" }, "not run");
  return h("span", { class: `pill ${st}` }, `${label(run)} · ${ago(run.started)}`);
}

function renderNav(route) {
  const nav = document.getElementById("nav");
  const items = [
    h("a", { href: "#/", class: "tab" + (route.view === "all" ? " active" : ""), "aria-current": route.view === "all" ? "page" : null },
      h("span", { class: "tab-icon grid", "aria-hidden": "true" }),
      h("span", { class: "tab-name" }, "All flows"),
      h("span", { class: "tab-count" }, state.flows.length)),
    h("div", { class: "nav-label" }, "Flows"),
  ];
  for (const f of state.flows) {
    const active = route.view === "flow" && route.name === f.name;
    const st = state.running[f.name] ? "running" : status(f.recent[0]);
    items.push(h("a", { href: `#/flow/${encodeURIComponent(f.name)}`, class: "tab" + (active ? " active" : ""), "aria-current": active ? "page" : null },
      h("span", { class: `dot ${st}`, title: st }),
      h("span", { class: "tab-name" }, f.name),
      h("span", { class: "tab-meta" }, hops(f.path).length + " hops")));
  }
  nav.replaceChildren(...items);
}

function renderAll() {
  const cards = state.flows.map((f) => {
    const last = f.recent[0];
    return h("article", { class: `card ${state.running[f.name] ? "running" : status(last)}` },
      h("header", { class: "card-head" },
        h("a", { class: "card-title", href: `#/flow/${encodeURIComponent(f.name)}` }, f.name),
        statusPill(last, f.name)),
      h("pre", { class: "output" }, outputLines(last).join("\n")),
      runButtons(f.name, false));
  });
  return [
    h("header", { class: "page-head" },
      h("div", {},
        h("h1", {}, "All flows"),
        h("p", { class: "lede" }, "Each flow is one path through the services. Run it, then follow its hops in Kubeshark."))),
    h("section", { class: "grid" }, cards),
  ];
}

function renderDeclared(name) {
  const d = state.details[name];
  if (!d) return h("p", { class: "muted" }, "Loading");
  const groups = {};
  for (const m of d.declared || []) (groups[m.kind] ||= []).push(m);
  return Object.keys(KINDS).filter((k) => groups[k]).map((k) =>
    h("div", { class: "kind-group" },
      h("div", { class: "kind-head" },
        h("span", { class: `kind ${k}` }, KINDS[k].title),
        h("span", { class: "muted" }, `${groups[k].length} · ${KINDS[k].note}`)),
      h("ul", { class: "declared" }, groups[k].map((m) =>
        h("li", {}, m.what, m.expect === "wire" ? h("span", { class: "wire", title: "Sent on the wire but not carried into a streamed entry" }, "wire only") : null)))));
}

function renderRun(run, open) {
  return h("details", { class: `run ${status(run)}`, open },
    h("summary", {},
      h("span", { class: `variant ${run.variant}` }, run.variant),
      h("span", { class: "run-when" }, new Date(run.started).toLocaleTimeString()),
      h("span", { class: "muted" }, `via ${run.source}`),
      h("span", { class: "run-dur" }, ms(run.duration)),
      h("span", { class: `pill ${status(run)}` }, label(run))),
    h("table", { class: "steps" },
      h("thead", {}, h("tr", {}, ["Step", "Protocol", "Target", "Time", "Result"].map((c) => h("th", {}, c)))),
      h("tbody", {}, (run.steps || []).map((s) =>
        h("tr", { class: s.error ? "failed" : "" },
          h("td", {}, s.name),
          h("td", {}, h("span", { class: "proto" }, s.protocol)),
          h("td", { class: "mono" }, s.target),
          h("td", { class: "num" }, ms(s.duration)),
          h("td", { class: "detail" }, s.error ? s.error : s.detail || ""))))),
    run.error ? h("p", { class: "run-error" }, run.error) : null);
}

function renderFlow(name) {
  const f = state.flows.find((x) => x.name === name);
  if (!f) return [h("p", { class: "muted" }, `No flow named ${name}.`)];
  if (!(name in state.details)) loadDetail(name);
  const path = hops(f.path);
  return [
    h("header", { class: "page-head" },
      h("div", {},
        h("h1", {}, f.name, " ", statusPill(f.recent[0], f.name)),
        h("p", { class: "lede" }, f.description)),
      runButtons(f.name, true)),
    h("ol", { class: "hops" }, path.map((p, i) => [i ? h("li", { class: "arrow", "aria-hidden": "true" }, "\u2192") : null, h("li", { class: "hop" }, p)])),
    h("div", { class: "split" },
      h("section", { class: "panel" },
        h("h2", {}, "Latest output"),
        h("pre", { class: "output tall" }, outputLines(f.recent[0]).join("\n")),
        h("h2", {}, "Recent runs"),
        f.recent.length ? f.recent.map((r, i) => renderRun(r, i === 0)) : h("p", { class: "muted" }, "No runs yet.")),
      h("section", { class: "panel" },
        h("h2", {}, "What this flow sends"),
        h("p", { class: "muted" }, `${f.markers} declared values. Look for them in Kubeshark; with sensitive-data redaction on, the redacted ones must be gone.`),
        renderDeclared(name))),
  ];
}

function route() {
  const m = location.hash.match(/^#\/flow\/(.+)$/);
  return m ? { view: "flow", name: decodeURIComponent(m[1]) } : { view: "all" };
}

function render() {
  const r = route();
  renderNav(r);
  const main = document.getElementById("main");
  const scroll = main.scrollTop;
  const openRuns = [...main.querySelectorAll("details.run")].map((d) => d.open);
  main.replaceChildren(...(r.view === "flow" ? renderFlow(r.name) : renderAll()));
  main.querySelectorAll("details.run").forEach((d, i) => { if (i < openRuns.length) d.open = openRuns[i]; });
  main.scrollTop = scroll;
  const age = state.lastOK ? Math.round((Date.now() - state.lastOK) / 1000) : null;
  document.getElementById("refresh-state").textContent =
    age == null ? "Connecting" : age > 10 ? `Gateway unreachable for ${age}s` : "Live, refreshes every 3s";
}

async function loadDetail(name) {
  state.details[name] = null;
  try {
    const res = await fetch(`/api/flows/${encodeURIComponent(name)}`);
    if (!res.ok) throw new Error(res.statusText);
    state.details[name] = await res.json();
  } catch (_) {
    delete state.details[name];
    return;
  }
  render();
}

async function refresh() {
  try {
    const res = await fetch("/api/flows");
    if (res.ok) {
      state.flows = await res.json();
      state.lastOK = Date.now();
    }
  } catch (_) {
    // Shown in the sidebar footer by render.
  }
  render();
}

async function run(name, variant) {
  state.running[name] = variant;
  render();
  try {
    await fetch(`/api/flows/${encodeURIComponent(name)}/run?variant=${variant}&source=ui`, { method: "POST" });
  } finally {
    delete state.running[name];
    await refresh();
  }
}

window.addEventListener("hashchange", () => {
  document.getElementById("main").scrollTop = 0;
  render();
});
refresh();
setInterval(refresh, 3000);
