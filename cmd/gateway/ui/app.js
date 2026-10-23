const ms = (ns) => (ns / 1e6).toFixed(0) + " ms";

function render(flows) {
  for (const f of flows) {
    const list = document.querySelector(`section[data-flow="${f.name}"] .recent`);
    if (!list) continue;
    list.replaceChildren(...f.recent.map((r) => {
      const li = document.createElement("li");
      li.className = r.error ? "failed" : "ok";
      const head = document.createElement("div");
      head.textContent = `${new Date(r.started).toLocaleTimeString()} ${r.variant} via ${r.source} in ${ms(r.duration)}${r.error ? " failed: " + r.error : ""}`;
      li.appendChild(head);
      const steps = document.createElement("ul");
      for (const s of r.steps || []) {
        const si = document.createElement("li");
        si.textContent = `${s.protocol} ${s.name} -> ${s.target} ${ms(s.duration)}${s.detail ? ": " + s.detail : ""}${s.error ? " error: " + s.error : ""}`;
        steps.appendChild(si);
      }
      li.appendChild(steps);
      return li;
    }));
  }
}

async function refresh() {
  const res = await fetch("/api/flows");
  if (res.ok) render(await res.json());
}

document.querySelectorAll("section.flow button").forEach((b) => {
  b.addEventListener("click", async () => {
    const flow = b.closest("section").dataset.flow;
    b.disabled = true;
    try {
      await fetch(`/api/flows/${flow}/run?variant=${b.dataset.variant}&source=ui`, { method: "POST" });
      await refresh();
    } finally {
      b.disabled = false;
    }
  });
});

refresh();
setInterval(refresh, 3000);
