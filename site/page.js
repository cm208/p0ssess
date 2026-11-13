// The home page's script: release links, copy buttons, the hairlines and chart marks that draw
// when they scroll into view, the island, the hand-off figure and the rule switch in the hero.
// Everything it animates has a complete still state in the markup; with scripts off or with
// reduced motion the page shows that.
(() => {
  const doc = document, root = doc.documentElement;
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const $ = (s, el = doc) => el.querySelector(s);
  const $$ = (s, el = doc) => [...el.querySelectorAll(s)];
  const hasIO = "IntersectionObserver" in window;

  // Motion only runs under html.anim, which follows the reader's setting.
  const syncMotion = () => root.classList.toggle("anim", !reduce.matches);
  syncMotion();
  reduce.addEventListener("change", syncMotion);

  // Release links. release.js holds the addresses; until it names a repository and a version,
  // the download controls stay disabled and no outside link shows.
  const rel = window.P0SSESS || {};
  const repo = String(rel.repo || "").replace(/\/+$/, ""), version = String(rel.version || "");
  const docs = String(rel.docs || "").replace(/\/+$/, "");
  const link = (a, href) => { a.href = href; a.removeAttribute("aria-disabled"); a.hidden = false; };
  if (docs) {
    $$('[data-link="docs"]').forEach((a) => link(a, docs));
    // A page of the documentation site, such as "media/#run-b-on-054".
    $$("[data-doc]").forEach((a) => link(a, docs + "/" + a.dataset.doc));
  }
  if (repo) {
    $$('[data-link="repo"]').forEach((a) => link(a, repo));
    $$("[data-repo-url]").forEach((el) => { el.textContent = repo + ".git"; });
  }
  if (repo && version) {
    const files = `${repo}/releases/download/v${version}/`;
    $$("[data-asset]").forEach((a) => link(a, files + a.dataset.asset.replace("{version}", version)));
    $$('[data-link="release"]').forEach((a) => link(a, `${repo}/releases/tag/v${version}`));
    $$("[data-archive-name]").forEach((el) => { el.textContent = `p0ssess-${version}.tar.gz`; });
    $$("[data-release-state]").forEach((el) => { el.textContent = `p0ssess ${version}, served from the repository's releases.`; });
    $$("[data-sum]").forEach((el) => {
      const sum = (rel.sha256 || {})[el.dataset.sum];
      if (sum) { el.textContent = sum; el.hidden = false; }
    });
  }

  // Copy buttons ship hidden; they only make sense with a script, and only for a command that is complete.
  const status = doc.getElementById("copy-status");
  $$(".copy-btn").forEach((btn) => {
    if (!repo && $("[data-repo-url]", btn.parentElement)) return;
    let timer = 0;
    btn.hidden = false;
    btn.addEventListener("click", async () => {
      const code = $("code", btn.parentElement);
      let copied = true;
      try {
        await navigator.clipboard.writeText(code.textContent);
      } catch {
        // No clipboard access (an insecure origin, or permission denied): select the command instead.
        copied = false;
        const range = doc.createRange();
        range.selectNodeContents(code);
        const sel = getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
      }
      btn.textContent = copied ? "Copied" : "Selected";
      if (status) status.textContent = copied ? "Copied." : "Selected. Copy it with your keyboard.";
      clearTimeout(timer);
      timer = setTimeout(() => { btn.textContent = "Copy"; if (status) status.textContent = ""; }, 1800);
    });
  });

  // Hairlines draw out of their violet start, a chart's marks land and the bars of a
  // side-by-side grow from the baseline, once each, when they scroll into view. Bars move at
  // one speed, so the shorter one lands first.
  const grow = (table) => {
    for (const bar of $$("rect.b", table)) bar.style.transitionDuration = 240 + parseFloat(bar.getAttribute("width")) * 9 + "ms";
    table.classList.add("in");
  };
  const lines = $$(".hl, .sec > .wrap, .land, .duel-t");
  if (hasIO) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        if (e.target.classList.contains("duel-t")) grow(e.target); else e.target.classList.add("in");
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
    lines.forEach((el) => seen.observe(el));
  } else {
    lines.forEach((el) => el.classList.add("in"));
  }

  // What a mark stands for, shown beside it while the pointer is on it. The same figures are in
  // the table under each chart, so nothing is readable only here.
  let tip = null, tipBox = null;
  const hideTip = () => { if (tip) tip.classList.remove("on"); };
  doc.addEventListener("pointerover", (e) => {
    const hit = e.target.closest && e.target.closest(".hit[data-tip]");
    if (!hit) return hideTip();
    const box = hit.closest(".plot-box") || hit.closest("li") || hit.parentElement.parentElement;
    if (box !== tipBox) {
      if (tip) tip.remove();
      tip = doc.createElement("span");
      tip.className = "tip";
      tip.setAttribute("aria-hidden", "true");
      if (getComputedStyle(box).position === "static") box.style.position = "relative";
      box.append(tip);
      tipBox = box;
    }
    tip.textContent = hit.dataset.tip;
    const b = box.getBoundingClientRect(), h = hit.getBoundingClientRect();
    const x = h.left + h.width / 2 - b.left, w = tip.offsetWidth;
    tip.style.left = Math.max(0, Math.min(b.width - w, x - w / 2)) + "px";
    tip.style.top = h.top - b.top - tip.offsetHeight - 4 + "px";
    tip.classList.add("on");
  });
  doc.addEventListener("pointerdown", (e) => { if (!(e.target.closest && e.target.closest(".hit[data-tip]"))) hideTip(); });

  // The island: shown once the top bar has scrolled away. Its violet dot slides to the section
  // in view (a slower twin makes the stretch) and the pill resizes to that section's name.
  const island = $(".island"), topbar = $(".topbar");
  if (island && topbar && hasIO) {
    const focus = $(".focus", island), label = $(".isl-label", island), text = $("span", label);
    const dots = $$("ol a", island);
    const mark = (a) => {
      dots.forEach((d) => d.setAttribute("aria-current", d === a ? "true" : "false"));
      focus.style.setProperty("--x", a.offsetLeft + a.offsetWidth / 2 + "px");
      island.classList.add("ready");
      island.classList.toggle("end", ["#pricing", "#try"].includes(a.getAttribute("href")));
      if (text.textContent !== a.getAttribute("aria-label")) {
        text.textContent = a.getAttribute("aria-label");
        label.style.setProperty("--lw", text.offsetWidth + "px");
      }
    };
    island.hidden = false;
    new IntersectionObserver(([e]) => island.classList.toggle("on", !e.isIntersecting)).observe(topbar);
    const owner = new Map();
    const spy = new IntersectionObserver((entries) => {
      for (const e of entries) if (e.isIntersecting) mark(owner.get(e.target));
    }, { rootMargin: "-45% 0px -50% 0px" });
    dots.forEach((d) => {
      const t = doc.getElementById(d.getAttribute("href").slice(1));
      if (!t) return;
      const box = t.closest(".sec, .hero") || t;
      owner.set(box, d);
      spy.observe(box);
    });
    mark(dots[0]);
    doc.fonts?.ready.then(() => { const a = dots.find((d) => d.getAttribute("aria-current") === "true"); if (a) { text.textContent = ""; mark(a); } });
  }

  // The hand-off. The timeline is CSS; this finds where the riding ghost lands (the seat in the
  // Haiku pill), plays it once when it scrolls into view, and names the next measured job on each replay.
  const relay = $(".relay");
  if (relay) {
    const rider = $(".rider", relay), seat = $(".seat", relay);
    const name = $(".name", relay), job = $(".job", relay), back = $(".back", relay), replay = $(".replay", relay);
    let jobs = [], n = 0;
    try { jobs = JSON.parse(relay.dataset.jobs); } catch { /* keeps the job in the markup */ }
    const center = (el) => {
      let x = el.offsetWidth / 2, y = el.offsetHeight / 2;
      for (let e = el; e && e !== relay; e = e.offsetParent) { x += e.offsetLeft; y += e.offsetTop; }
      return [x, y];
    };
    const place = () => {
      if (!rider.offsetParent) return;
      const [rx, ry] = center(rider), [sx, sy] = center(seat);
      relay.style.setProperty("--rx", sx - rx + "px");
      relay.style.setProperty("--ry", sy - ry + "px");
      relay.style.setProperty("--rs", (seat.offsetWidth / rider.offsetWidth).toFixed(4));
    };
    const play = () => {
      relay.classList.remove("play", "done");
      void relay.offsetWidth;
      place();
      relay.classList.add("play");
    };
    rider.addEventListener("animationend", (e) => {
      if (e.animationName !== "ride") return;
      relay.classList.add("done");
      relay.classList.remove("play");
    });
    replay.addEventListener("click", () => {
      if (jobs.length) {
        n = (n + 1) % jobs.length;
        [name.textContent, job.textContent] = jobs[n];
        back.textContent = `(${jobs[n][2]})`;
      }
      play();
    });
    const syncRelay = () => { replay.hidden = reduce.matches; if (reduce.matches) relay.classList.remove("play"); };
    syncRelay();
    reduce.addEventListener("change", syncRelay);
    doc.fonts?.ready.then(place);
    if ("ResizeObserver" in window) new ResizeObserver(place).observe(relay);
    if (hasIO) {
      const once = new IntersectionObserver(([e]) => {
        if (!e.isIntersecting) return;
        once.disconnect();
        if (!reduce.matches) play();
      }, { threshold: 0.6 });
      once.observe(relay);
    }
  }

  // The rule switch. The markup holds both measured results and CSS moves the ghost between
  // them; this counts the figure down or up as it rides, says the result to a screen reader,
  // and, with motion allowed, opens on "rule off" and turns the rule on once the line of
  // dollars is on screen, so the first thing the page does is the saving.
  const hero = $(".hero");
  const on = $("#rule-on"), off = $("#rule-off");
  if (hero && on && off) {
    const num = $(".now .num", hero), bOn = $("b.v-on", num), bOff = $("b.v-off", num);
    const say = $("#rule-status"), rider = $(".head .rider", hero);
    const cost = { on: parseFloat(bOn.textContent), off: parseFloat(bOff.textContent) };
    const text = { on: bOn.textContent, off: bOff.textContent };
    let raf = 0, auto = 0;
    const blink = () => { if (!rider) return; rider.classList.remove("blink"); void rider.offsetWidth; rider.classList.add("blink"); };
    const ride = () => {
      const to = on.checked ? "on" : "off", from = on.checked ? "off" : "on";
      const b = on.checked ? bOn : bOff;
      cancelAnimationFrame(raf);
      bOn.textContent = text.on;
      bOff.textContent = text.off;
      if (say) say.textContent = on.checked ? `Rule on: $${text.on} a run, and all 15 test files pass.` : `Rule off: $${text.off} a run, and all 15 test files pass.`;
      if (reduce.matches) return;
      // The figure follows the ghost: the same 1.5 seconds and the same ease as the --at transition.
      const t0 = performance.now(), ms = 1500;
      const ease = (t) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
      blink();
      const step = (now) => {
        const t = Math.min(1, (now - t0) / ms);
        b.textContent = (cost[from] + (cost[to] - cost[from]) * ease(t)).toFixed(2);
        if (t < 1) { raf = requestAnimationFrame(step); return; }
        b.textContent = text[to];
        num.classList.remove("tick");
        void num.offsetWidth;
        num.classList.add("tick");
        blink();
      };
      raf = requestAnimationFrame(step);
    };
    for (const r of [on, off]) r.addEventListener("change", () => { clearTimeout(auto); ride(); });
    const line = $(".dline .plot-box", hero);
    if (!reduce.matches && hasIO && line) {
      off.checked = true;
      const start = new IntersectionObserver(([e]) => {
        if (!e.isIntersecting) return;
        start.disconnect();
        auto = setTimeout(() => { if (!off.checked) return; on.checked = true; ride(); }, 900);
      }, { threshold: 0.9 });
      start.observe(line);
    }
    root.classList.add("hero-ready");
    // The ride is allowed from the second frame on, once the first state has been painted.
    requestAnimationFrame(() => requestAnimationFrame(() => root.classList.add("hero-go")));
  }
})();
