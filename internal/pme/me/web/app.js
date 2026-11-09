"use strict";
(() => {
  const $ = (s) => document.querySelector(s);
  const el = (tag, attrs = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === "class") n.className = v; else if (k === "text") n.textContent = v; else n.setAttribute(k, v);
    }
    for (const k of kids) if (k != null && k !== false) n.append(k);
    return n;
  };
  const list = (v) => (Array.isArray(v) ? v : []);
  const money = (v) => {
    v = Number(v) || 0;
    return v >= 1000 ? "$" + Math.round(v).toLocaleString("en-US") : "$" + v.toFixed(2);
  };
  const cost = (v) => (v > 0 && v < 0.01 ? "under $0.01" : money(v));
  const pct = (v) => Math.round((v || 0) * 100) + "%";
  const short = (id) => (id || "").split("/").pop();
  const cap = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : "");
  const span = (secs) => {
    secs = Math.max(0, Math.round(secs));
    if (secs < 60) return secs + " sec";
    const m = Math.round(secs / 60);
    if (m < 60) return m + " min";
    const h = Math.floor(m / 60), r = m % 60;
    if (h < 24) return h + " hr" + (r ? " " + r + " min" : "");
    const d = Math.round(h / 24);
    return d + (d === 1 ? " day" : " days");
  };
  const ago = (iso) => {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    if (s < 60) return "Just now";
    if (s < 86400) return span(s) + " ago";
    return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  };
  const when = (d) => d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  const meter = (frac, near, label) => {
    const m = el("div", { class: "meter" + (near ? " near" : ""), role: "img", "aria-label": label }, el("span"));
    m.firstElementChild.style.width = (Math.min(1, Math.max(0, frac)) * 100).toFixed(1) + "%";
    return m;
  };
  let token = "";
  let dirty = false;
  const showAll = { recent: false, runs: false };
  const FEW = 8;

  async function post(path, body) {
    const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", "X-P0ssess-Token": token }, body: JSON.stringify(body) });
    let j = {};
    try { j = await r.json(); } catch (e) { /* not JSON */ }
    if (!r.ok) throw new Error(j.error || r.statusText || "Request failed");
    return j;
  }

  function renderSpend(st) {
    const b = st.budget, sp = st.spend;
    const monthly = b.monthly_usd;
    const frac = monthly > 0 ? sp.month_usd / monthly : 0;
    const m = $("#month-meter");
    m.classList.toggle("near", !!st.near_budget);
    m.firstElementChild.style.width = (Math.min(1, frac) * 100).toFixed(1) + "%";
    m.setAttribute("aria-label", monthly > 0 ? "Monthly spend, " + pct(frac) + " of the cap" : "No monthly cap");
    $("#ghost").src = st.near_budget ? "ghost-amber.svg" : "ghost.svg";
    $("#spend-line").replaceChildren(
      el("b", { class: st.near_budget ? "near" : null, text: money(sp.month_usd) }),
      monthly > 0 ? "spent this month, of a " + money(monthly) + " cap" : "spent this month, with no cap set");
    const flag = $("#spend-flag");
    flag.hidden = !st.near_budget;
    flag.textContent = st.near_budget ? "Close to the monthly cap, with " + money(st.month_left_usd) + " left." : "";

    const daily = st.daily_limit_usd < 0 || b.daily_usd < 0 ? "Off" : money(st.daily_limit_usd);
    const facts = [
      ["Today", money(sp.today_usd)],
      ["Daily limit", daily],
      ["Left this month", money(st.month_left_usd)],
      ["Runs this month", String(sp.runs_this_month || 0)],
      ["Auto-approve", "Up to " + money(b.per_task_usd)],
      ["Privacy", b.privacy && b.privacy.mode === "zdr" ? "Zero retention" : "No training"],
      ["Shell internet", list(b.sandbox && b.sandbox.shell_internet).join(", ") || "Off"],
    ];
    const dl = $("#spend-facts");
    dl.replaceChildren(...facts.map(([k, v]) => el("div", {}, el("dt", { text: k }), el("dd", { text: v }))));
    const over = list(st.overrides);
    const old = dl.parentElement.querySelector(".overrides");
    if (old) old.remove();
    if (over.length) {
      dl.after(el("p", { class: "note overrides", text: (over.length === 1 ? "A rule changes this budget: " : "Rules change this budget: ") + over.join("; ") + "." }));
    }
  }

  function renderUsage(s) {
    const lam = s.status.lambda || {};
    const api = (lam.plan || s.status.plan) === "api";
    const wins = list(s.windows);
    $("#lambda-line").replaceChildren(el("b", { text: pct(lam.value) }),
      api ? "of list price. On an API key, Claude always counts in full." : "of list price is Claude's weight when p0ssess compares routes.");
    const box = $("#windows");
    box.replaceChildren(...wins.map((w) => {
      const near = w.projected >= 0.9;
      const m = meter(w.used, near, cap(w.name) + " window, " + pct(w.used) + " used, projected " + pct(w.projected) + " by reset");
      if (w.projected > 0) {
        const tick = el("i", { title: "Projected by reset" });
        tick.style.left = Math.min(100, w.projected * 100).toFixed(1) + "%";
        m.append(tick);
      }
      const reset = new Date(w.resets_at * 1000);
      const left = (reset.getTime() - Date.now()) / 1000;
      const resets = left > 0 && left < 86400 ? "Resets in " + span(left) + "." : "Resets " + reset.toLocaleString(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" }) + ".";
      return el("div", { class: "win" },
        el("div", { class: "win-head" }, el("h3", { text: cap(w.name) + " window" }), el("span", { class: "num" + (near ? " near" : ""), text: pct(w.used) + " used" })),
        m,
        el("p", { class: "win-foot" }, "Projected " + pct(w.projected) + " by reset. " + resets));
    }));
    const presetName = presetLabel(lam.preset || s.lambda_preset).split(",")[0];
    let note;
    if (api) note = "There are no usage windows to watch on an API-key plan.";
    else if (!wins.length) note = "No recent usage reading. Readings come in through the p0ssess status line; until one arrives, Claude counts at the low end of the " + presetName.toLowerCase() + " curve.";
    else note = presetName + " pressure curve. The tick on each bar marks where use is headed by reset" + (s.reading_age_seconds > 0 ? ". Last reading " + span(s.reading_age_seconds) + " ago." : ".");
    $("#lambda-note").textContent = note;
  }

  function renderRunning(st) {
    const jobs = list(st.running);
    $("#running-card").hidden = jobs.length === 0;
    $("#running").replaceChildren(...jobs.map((j) => el("li", {},
      el("span", { class: "grow" }, el("span", { class: "model", text: short(j.model) }), j.use_case ? el("span", { class: "sub", text: cap(j.use_case) }) : null),
      el("span", { class: "side", text: "Started " + ago(j.started).toLowerCase() }))));
  }

  function tendencyTag(t) {
    if (t.favored) return el("span", { class: "tag good", text: "Favored" });
    if (t.disfavored) return el("span", { class: "tag", text: "Avoided" });
    const c = t.counts || {};
    const runs = (c.accepted || 0) + (c.redo || 0);
    if (runs >= 3 && (c.redo || 0) / runs >= 0.3) return el("span", { class: "tag", text: "Often redone" });
    return el("span", { class: "tag", text: "Mixed" });
  }

  const OUTCOME = { accepted: "Accepted", redo: "Redone", overridden: "Overridden", declined: "Declined" };
  const ROUTE = { agent: "Agent", direct: "Direct", reported: "Reported" };

  // fillLedger shows the first few rows of a table, with a button for the rest.
  function fillLedger(name, rows, emptyText) {
    const table = $("#" + name);
    table.classList.toggle("is-empty", rows.length === 0);
    const shown = showAll[name] ? rows : rows.slice(0, FEW);
    table.tBodies[0].replaceChildren(...(rows.length ? shown : [el("tr", {}, el("td", { class: "empty", colspan: "5", text: emptyText }))]));
    const more = $("#" + name + "-more");
    more.hidden = rows.length <= FEW;
    more.textContent = showAll[name] ? "Show fewer" : "Show all " + rows.length;
    more.setAttribute("aria-expanded", String(showAll[name]));
  }

  function renderLearned(l, s) {
    const t = list(l.tendencies);
    $("#tendencies").replaceChildren(...(t.length
      ? t.map((x) => el("li", {}, el("span", { class: "grow", text: cap(x.text) }), tendencyTag(x)))
      : [el("li", { class: "empty", text: "Nothing learned yet." })]));

    $("#suggestions").replaceChildren(...list(l.suggestions).map((sg) => {
      const b = el("button", { class: "btn quiet", type: "button", text: "Save as rule" });
      const msg = el("p", { text: cap(sg.text) });
      b.addEventListener("click", async () => {
        b.disabled = true;
        try { await post("api/rules/add", { rule: sg.rule }); load(); } catch (e) { b.disabled = false; msg.textContent = "Couldn't save the rule: " + e.message; }
      });
      return el("div", { class: "suggest" }, el("div", {}, el("h3", { text: "Suggested rule" }), msg), b);
    }));

    const rows = list(l.recent).map((r) => el("tr", {},
      el("td", { class: "when", text: ago(r.ts) }),
      el("td", { class: "task", text: cap(r.use_case || r.kind || "Untitled task") }),
      el("td", { class: "model", text: short(r.model) }),
      el("td", { class: "out" }, el("span", { class: "tag" + (r.outcome === "accepted" ? " good" : ""), text: OUTCOME[r.outcome] || cap(r.outcome) })),
      r.cash_usd ? el("td", { class: "num", text: cost(r.cash_usd) }) : el("td", { class: "num dash", "aria-label": "No cost", text: "—" })));
    fillLedger("recent", rows, "No delegations recorded yet.");

    // Measured runs, when the engine sends them.
    const runs = Array.isArray(s.runs) ? s.runs : Array.isArray(s.status.spend && s.status.spend.runs) ? s.status.spend.runs : null;
    $("#runs-card").hidden = runs === null;
    if (runs) {
      const rr = runs.map((r) => {
        const total = r.cash_usd || 0;
        const td = el("td", { class: "num", text: cost(total) });
        if (r.upstream_usd) td.title = money(total - r.upstream_usd) + " OpenRouter and " + money(r.upstream_usd) + " billed to your provider key";
        return el("tr", {},
          el("td", { class: "when", text: ago(r.ts) }),
          el("td", { class: "task", text: cap(r.use_case || "Untitled task") + (r.status && r.status !== "done" ? ", " + r.status : "") }),
          el("td", { class: "model", text: short(r.model) }),
          el("td", { class: "out" }, el("span", { class: "tag", text: ROUTE[r.route] || cap(r.route || "Run") })),
          td);
      });
      fillLedger("runs", rr, "No runs measured yet.");
    }
  }

  const SCOPES = [["session", "This session"], ["project", "This project"], ["global", "Everywhere"]];

  function ruleUse(r) {
    if (!r.used || !(r.max_count || r.max_total_usd)) return "";
    const per = r.period === "day" ? " today" : "";
    const parts = [];
    if (r.max_count) parts.push(r.used.count + " of " + r.max_count + " runs");
    if (r.max_total_usd) parts.push(money(r.used.total_usd) + " of " + money(r.max_total_usd));
    return "Used " + parts.join(" and ") + per + ".";
  }

  function renderRules(rs) {
    rs = list(rs);
    const box = $("#rules");
    if (!rs.length) {
      box.replaceChildren(el("p", { class: "empty", text: "No rules yet. Every route is open within the budget." }));
      return;
    }
    box.replaceChildren(...SCOPES.filter(([k]) => rs.some((r) => r.scope === k)).map(([k, title]) => {
      const items = rs.filter((r) => r.scope === k).map((r) => {
        const text = cap(r.text.replace(/ \((session|project|global)[^)]*\)$/, ""));
        const rm = el("button", { class: "btn quiet", type: "button", text: "Remove", "aria-label": "Remove rule: " + text });
        rm.addEventListener("click", async () => {
          rm.disabled = true;
          try { await post("api/rules/remove", { id: r.id }); load(); } catch (e) { rm.disabled = false; rm.textContent = "Try again"; rm.title = e.message; }
        });
        const sub = [];
        if (r.expired) sub.push("Expired.");
        else if (r.expires) sub.push("Until " + when(new Date(r.expires)) + ".");
        const use = ruleUse(r);
        if (use) sub.push(use);
        if (r.note) sub.push("In your words: “" + r.note + "”");
        return el("li", {}, el("span", { class: "grow" }, text, sub.length ? el("span", { class: "sub", text: sub.join(" ") }) : null), rm);
      });
      return el("div", { class: "rule-group" }, el("h3", { text: title }), el("ul", { class: "rows" }, ...items));
    }));
  }

  const PRESETS = { list: "Always list price", gentle: "Gentle, cheap until a window is nearly spent", balanced: "Balanced, a smooth rise to list price", eager: "Eager, rises early to save Claude" };
  const presetLabel = (p) => PRESETS[p] || cap(p || "custom");

  function fillSettings(s) {
    if (dirty) return;
    const b = s.status.budget, f = $("#settings");
    const sel = $("#preset");
    const names = list(s.presets).slice();
    if (s.lambda_preset && !names.includes(s.lambda_preset)) names.push(s.lambda_preset);
    if (sel.options.length !== names.length) sel.replaceChildren(...names.map((p) => el("option", { value: p, text: presetLabel(p) })));
    const vals = {
      monthly_usd: b.monthly_usd, per_task_usd: b.per_task_usd, daily_usd: b.daily_usd, plan: b.plan,
      "lambda.preset": s.lambda_preset, save_claude: b.save_claude, usage_credits: b.usage_credits,
      "privacy.mode": b.privacy.mode, "privacy.allow_safety_retention": b.privacy.allow_safety_retention,
      "models.deny": list(b.models && b.models.deny).join(", "), "providers.deny": list(b.providers && b.providers.deny).join(", "),
      "statusline.command": (s.statusline && s.statusline.command) || "",
    };
    for (const [k, v] of Object.entries(vals)) {
      const input = f.elements[k];
      if (!input) continue;
      if (input.type === "checkbox") input.checked = !!v; else input.value = v == null ? "" : v;
      input.dataset.orig = input.type === "checkbox" ? String(!!v) : String(v == null ? "" : v);
    }
  }

  const saved = (text, err) => { const o = $("#saved"); o.textContent = text; o.classList.toggle("err", !!err); };
  for (const name of ["recent", "runs"]) $("#" + name + "-more").addEventListener("click", () => { showAll[name] = !showAll[name]; load(); });
  $("#settings").addEventListener("input", () => { dirty = true; saved(""); });
  $("#settings").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const changes = {};
    for (const input of ev.target.elements) {
      if (!input.name) continue;
      const v = input.type === "checkbox" ? String(input.checked) : input.value.trim();
      if (v !== input.dataset.orig) changes[input.name] = v;
    }
    if (!Object.keys(changes).length) { saved("Nothing to save."); return; }
    for (const k of ["monthly_usd", "per_task_usd", "daily_usd"]) {
      if (k in changes && (changes[k] === "" || !Number.isFinite(Number(changes[k])))) {
        const input = ev.target.elements[k];
        saved(cap(input.labels[0].textContent.split(",")[0]) + " needs a number.", true);
        input.focus();
        return;
      }
    }
    const btn = ev.target.querySelector("button[type=submit]");
    btn.disabled = true;
    try {
      await post("api/settings", { changes });
      saved("Saved.");
      dirty = false;
      load();
    } catch (e) {
      saved("Not saved: " + e.message, true);
    } finally {
      btn.disabled = false;
    }
  });

  async function load() {
    const up = $("#updated");
    try {
      const r = await fetch("api/state", { cache: "no-store" });
      if (!r.ok) throw new Error(r.statusText);
      const s = await r.json();
      token = s.token;
      $("#where").textContent = location.host;
      renderSpend(s.status);
      renderUsage(s);
      renderRunning(s.status);
      renderLearned(s.learned || {}, s);
      renderRules(s.rules);
      fillSettings(s);
      up.classList.remove("off");
      up.textContent = "Updated " + new Date(s.now).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
    } catch (e) {
      up.classList.add("off");
      up.textContent = "Lost contact with p0ssess. Run p0ssess me for a new page.";
    }
  }
  load();
  setInterval(load, 10000);
})();
