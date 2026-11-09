"use strict";
(() => {
  const $ = (s) => document.querySelector(s);
  const el = (tag, attrs = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === "class") n.className = v; else if (k === "text") n.textContent = v; else n.setAttribute(k, v);
    }
    for (const k of kids) n.append(k);
    return n;
  };
  const money = (v) => (v >= 100 ? "$" + Math.round(v) : "$" + (v || 0).toFixed(2));
  const pct = (v) => Math.round((v || 0) * 100) + "%";
  const short = (id) => (id || "").split("/").pop();
  const ago = (iso) => {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    if (s < 90) return "just now";
    if (s < 5400) return Math.round(s / 60) + " min ago";
    if (s < 129600) return Math.round(s / 3600) + " h ago";
    return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  };
  let token = "";
  let dirty = false;

  async function post(path, body) {
    const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", "X-P0ssess-Token": token }, body: JSON.stringify(body) });
    const j = await r.json();
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

  function renderSpend(st) {
    const b = st.budget, sp = st.spend;
    const cap = b.monthly_usd;
    const frac = cap > 0 ? Math.min(1, sp.month_usd / cap) : 0;
    const meter = $("#month-meter");
    meter.classList.toggle("near", st.near_budget);
    meter.firstElementChild.style.width = (frac * 100).toFixed(1) + "%";
    $("#ghost").src = st.near_budget ? "ghost-amber.svg" : "ghost.svg";
    const line = $("#spend-line");
    line.replaceChildren(el("b", { text: money(sp.month_usd) }), " of " + money(cap) + " this month");
    const daily = st.daily_limit_usd < 0 ? "off" : money(st.daily_limit_usd);
    const facts = [["Today", money(sp.today_usd)], ["Daily limit", daily], ["Per task", money(b.per_task_usd)], ["Runs", String(sp.runs_this_month)], ["Left", money(st.month_left_usd)], ["Privacy", b.privacy.mode === "zdr" ? "zero retention" : "no training"]];
    $("#spend-facts").replaceChildren(...facts.map(([k, v]) => el("div", {}, el("dt", { text: k }), el("dd", { text: v }))));
  }

  function renderUsage(s) {
    const lam = s.status.lambda;
    const line = $("#lambda-line");
    line.replaceChildren("Claude counts at ", el("b", { text: Math.round(lam.value * 100) + "%" }), " of list price");
    const box = $("#windows");
    box.replaceChildren();
    if (!s.windows || !s.windows.length) {
      box.append(el("p", { class: "note", text: s.status.plan === "api" ? "API-key plan: Claude is priced at list." : "No fresh usage reading. Readings come from the status line wrapper." }));
    }
    for (const w of s.windows || []) {
      const m = el("div", { class: "meter" + (w.projected >= 0.9 ? " near" : "") }, el("span"));
      m.firstElementChild.style.width = Math.min(100, w.used * 100).toFixed(1) + "%";
      if (w.projected > 0) {
        const tick = el("i", { title: "projected at reset" });
        tick.style.left = "calc(" + Math.min(100, w.projected * 100).toFixed(1) + "% - 1px)";
        m.append(tick);
      }
      const resets = new Date(w.resets_at * 1000).toLocaleString(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" });
      box.append(el("div", { class: "win" }, el("header", {}, el("span", { text: w.name + " · " + pct(w.used) + " used" }), el("span", { class: "meta", text: "projected " + pct(w.projected) + " · resets " + resets })), m));
    }
    $("#lambda-note").textContent = lam.preset + " curve · " + lam.note;
  }

  function renderRunning(st) {
    const jobs = st.running || [];
    $("#running-card").hidden = jobs.length === 0;
    $("#running").replaceChildren(...jobs.map((j) => el("li", {}, el("span", { class: "led lav" }), el("span", { class: "grow", text: short(j.model) + (j.use_case ? " · " + j.use_case : "") }), el("span", { class: "meta", text: ago(j.started) }))));
  }

  function renderLearned(l) {
    const t = l.tendencies || [];
    $("#tendencies").replaceChildren(...(t.length ? t.map((x) => el("li", {}, el("span", { class: "led " + (x.favored ? "ok" : x.disfavored ? "wait" : "") }), el("span", { class: "grow", text: x.text }))) : [el("li", { class: "empty", text: "Nothing learned yet. Tendencies appear after a few delegations of one kind." })]));
    const box = $("#suggestions");
    box.replaceChildren(...(l.suggestions || []).map((sg) => {
      const b = el("button", { class: "btn quiet", type: "button", text: "Make it a rule" });
      b.addEventListener("click", async () => {
        b.disabled = true;
        try { await post("api/rules/add", { rule: sg.rule }); load(); } catch (e) { b.textContent = e.message; }
      });
      return el("div", { class: "suggest" }, el("p", { text: sg.text }), b);
    }));
    const rows = (l.recent || []).map((r) => el("tr", {}, el("td", { class: "meta", text: ago(r.ts) }), el("td", { class: "task", text: r.use_case || "—" }), el("td", { text: short(r.model) }), el("td", {}, el("span", { class: "chip", text: r.outcome })), el("td", { class: "num", text: r.cash_usd ? money(r.cash_usd) : "—" })));
    $("#recent tbody").replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { class: "empty", colspan: "5", text: "No delegations recorded yet." }))]));
  }

  function renderRules(rs) {
    const ul = $("#rules");
    ul.replaceChildren(...(rs && rs.length ? rs.map((r) => {
      const rm = el("button", { class: "btn quiet", type: "button", text: "Remove", "aria-label": "Remove rule " + r.text });
      rm.addEventListener("click", async () => {
        rm.disabled = true;
        try { await post("api/rules/remove", { id: r.id }); load(); } catch (e) { rm.textContent = e.message; }
      });
      const used = r.used && (r.max_count || r.max_total_usd) ? " · used " + r.used.count + (r.max_total_usd ? ", " + money(r.used.total_usd) : "") : "";
      return el("li", {}, el("span", { class: "chip", text: r.scope }), el("span", { class: "grow", text: r.text.replace(/ \((session|project|global)[^)]*\)$/, "") + used + (r.expired ? " · expired" : "") }), rm);
    }) : [el("li", { class: "empty", text: "No rules. Every route is open within the budget." })]));
  }

  function fillSettings(s) {
    if (dirty) return;
    const b = s.status.budget, f = $("#settings");
    const sel = $("#preset");
    if (!sel.options.length) for (const p of s.presets) sel.append(el("option", { value: p, text: p }));
    const vals = {
      monthly_usd: b.monthly_usd, per_task_usd: b.per_task_usd, daily_usd: b.daily_usd, plan: b.plan,
      "lambda.preset": s.lambda_preset, save_claude: b.save_claude, usage_credits: b.usage_credits,
      "privacy.mode": b.privacy.mode, "privacy.allow_safety_retention": b.privacy.allow_safety_retention,
      "models.deny": (b.models.deny || []).join(", "), "providers.deny": (b.providers.deny || []).join(", "),
      "statusline.command": s.statusline.command || "",
    };
    for (const [k, v] of Object.entries(vals)) {
      const input = f.elements[k];
      if (!input) continue;
      if (input.type === "checkbox") input.checked = !!v; else input.value = v;
      input.dataset.orig = input.type === "checkbox" ? String(!!v) : String(v);
    }
  }

  $("#settings").addEventListener("input", () => { dirty = true; });
  $("#settings").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const changes = {};
    for (const input of ev.target.elements) {
      if (!input.name) continue;
      const v = input.type === "checkbox" ? String(input.checked) : input.value.trim();
      if (v !== input.dataset.orig) changes[input.name] = v;
    }
    const out = $("#saved");
    if (!Object.keys(changes).length) { out.textContent = "Nothing changed."; return; }
    try {
      await post("api/settings", { changes });
      out.textContent = "Saved.";
      dirty = false;
      load();
    } catch (e) { out.textContent = e.message; }
  });

  async function load() {
    try {
      const r = await fetch("api/state", { cache: "no-store" });
      const s = await r.json();
      token = s.token;
      $("#where").textContent = location.host;
      renderSpend(s.status);
      renderUsage(s);
      renderRunning(s.status);
      renderLearned(s.learned);
      renderRules(s.rules);
      fillSettings(s);
      $("#updated").textContent = "updated " + new Date(s.now).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
    } catch (e) {
      $("#updated").textContent = "engine not reachable";
    }
  }
  load();
  setInterval(load, 10000);
})();
