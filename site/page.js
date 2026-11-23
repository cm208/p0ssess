// The home page's script: release links, copy buttons, the hairlines that draw when they scroll
// into view, the island, the rescue that plays once, the statement that lights a word at a time
// and the savings example. Everything it moves has a complete still state in the markup; with
// scripts off or with reduced motion the page shows that.
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
  if (docs) $$('[data-link="docs"]').forEach((a) => link(a, docs));
  if (repo) {
    $$('[data-link="repo"]').forEach((a) => link(a, repo));
    $$("[data-repo-url]").forEach((el) => { el.textContent = repo + ".git"; });
  }
  if (version) {
    $$("[data-version]").forEach((el) => { el.textContent = version; });
    $$("[data-version-say]").forEach((el) => { el.hidden = false; });
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

  // Hairlines draw out of their violet start, once, when they scroll into view.
  const lines = $$(".hl, .sec > .wrap");
  if (hasIO) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        e.target.classList.add("in");
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
    lines.forEach((el) => seen.observe(el));
  } else {
    lines.forEach((el) => el.classList.add("in"));
  }

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
      // Where the page itself offers Pro or the install, the dock's own button steps aside.
      island.classList.toggle("end", ["#pro", "#pricing", "#try"].includes(a.getAttribute("href")));
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
      owner.set(t, d);
      spy.observe(t);
    });
    mark(dots[0]);
    doc.fonts?.ready.then(() => { const a = dots.find((d) => d.getAttribute("aria-current") === "true"); if (a) { text.textContent = ""; mark(a); } });
  }

  // The rescue. Each moment plays when it scrolls into view; moments that arrive together follow
  // one another, 1.15 seconds apart. Replay runs all four again.
  const seq = $("#seq"), replay = $("[data-replay]");
  if (seq && hasIO) {
    const steps = $$(".step", seq);
    let next = 0, watch = null;
    const go = (li) => {
      const now = performance.now(), start = Math.max(now, next);
      next = start + 1150;
      li.style.setProperty("--d", Math.round(start - now) + "ms");
      li.classList.add("go");
      clearTimeout(li.rest);
      // Once it has played it is plain markup again, so the Pro and Free views switch at once.
      li.rest = setTimeout(() => li.classList.add("done"), start - now + 1600);
    };
    const reset = () => { next = 0; steps.forEach((li) => { clearTimeout(li.rest); li.classList.remove("go", "done"); }); };
    const arm = () => {
      watch?.disconnect();
      reset();
      seq.classList.toggle("pre", !reduce.matches);
      if (replay) replay.hidden = reduce.matches;
      if (reduce.matches) return;
      watch = new IntersectionObserver((entries) => {
        for (const e of entries.sort((a, b) => steps.indexOf(a.target) - steps.indexOf(b.target))) {
          if (!e.isIntersecting) continue;
          watch.unobserve(e.target);
          go(e.target);
        }
      }, { rootMargin: "0px 0px -12% 0px", threshold: 0.35 });
      steps.forEach((li) => watch.observe(li));
    };
    arm();
    reduce.addEventListener("change", arm);
    replay?.addEventListener("click", () => { watch?.disconnect(); reset(); void seq.offsetWidth; steps.forEach(go); });
  }

  // The statement lights a word at a time as it climbs the screen. Words stay lit.
  const say = $("[data-say]");
  if (say && !reduce.matches) {
    const words = say.textContent.trim().split(/\s+/);
    say.textContent = "";
    words.forEach((w, i) => {
      const s = doc.createElement("span");
      s.className = "w";
      s.textContent = w;
      say.append(s, i < words.length - 1 ? " " : "");
    });
    const spans = $$(".w", say);
    let lit = 0, queued = false;
    const light = () => {
      queued = false;
      const r = say.getBoundingClientRect(), vh = innerHeight;
      const p = reduce.matches ? 1 : Math.min(1, Math.max(0, (vh * 0.9 - r.top) / (vh * 0.42 + r.height)));
      for (const n = Math.round(p * spans.length); lit < n; lit++) spans[lit].classList.add("on");
      if (lit >= spans.length) removeEventListener("scroll", onScroll);
    };
    const onScroll = () => { if (!queued) { queued = true; requestAnimationFrame(light); } };
    say.classList.add("live");
    addEventListener("scroll", onScroll, { passive: true });
    reduce.addEventListener("change", light);
    light();
  }

  // The savings example. One measured call from the docs (a PDF table turned into JSON on
  // deepseek-v4.1-flash: 195 tokens in, 1,456 out, $0.000729) is priced the way p0ssess prices a
  // run: its tokens at Claude's list price, less what OpenRouter charged. The card is that call
  // repeated. The figures print the way the plugin prints them.
  const ledger = $(".ledger"), card = $(".scard");
  const pickClaude = $("[data-claude]"), pickRuns = $("[data-runs]");
  if (ledger && card && pickClaude && pickRuns) {
    const call = { model: "deepseek-v4.1-flash", tin: 195, tout: 1456, cost: 0.000729 };
    // Dollars for a million tokens, in and out.
    const list = { opus: ["Claude Opus 5.5", 4, 20], sonnet: ["Claude Sonnet 5.5", 2, 10], haiku: ["Claude Haiku 5.5", 0.1, 0.5] };
    const money = (v) => {
      const neg = v < 0 ? "-" : "";
      v = Math.abs(v);
      if (v >= 100) return `${neg}$${v.toFixed(0)}`;
      if (v >= 0.1 || v === 0) return `${neg}$${v.toFixed(2)}`;
      if (v >= 0.001) return `${neg}$${v.toFixed(3)}`;
      return `${neg}<$0.001`;
    };
    const tokens = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e4 ? (n / 1e3).toFixed(0) + "k" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n));
    const rate = (v) => "$" + (v % 1 ? v.toFixed(2) : v);
    const clip = (s, n) => (s.length <= n ? s : s.slice(0, n - 1) + "…");
    const side = card.parentElement;
    const L = (k) => $(`[data-l="${k}"]`, ledger), C = (k) => $(`[data-c="${k}"]`, side);
    const paint = () => {
      const [name, pin, pout] = list[$("input:checked", pickClaude).value];
      const n = Number($("input:checked", pickRuns).value);
      const claude = (call.tin * pin + call.tout * pout) / 1e6, saved = claude - call.cost;
      // A receipt names a saving from half a cent up.
      const named = saved >= 0.005;
      L("name").textContent = name;
      L("rate").textContent = `${rate(pin)} in and ${rate(pout)} out for a million tokens`;
      L("claude").textContent = "$" + claude.toFixed(5);
      L("saved").textContent = named ? "~" + money(saved) : "$" + saved.toFixed(5);
      L("receipt").textContent = `(${call.model}, $${call.cost.toFixed(4)}${named ? `, saved ~$${saved.toFixed(2)}` : ""})`;
      L("note").textContent = named ? "A receipt names a saving once it reaches half a cent." : "Under half a cent, so the receipt names no saving.";
      ledger.classList.toggle("none", !named);

      const t = { claude: claude * n, cost: call.cost * n };
      t.saved = t.claude - t.cost;
      const kind = t.saved >= 0.005 ? "saved" : t.saved <= -0.005 ? "worse" : "none";
      const pct = t.saved > 0 ? Math.floor((t.saved / t.claude) * 100 + 0.5) : 0;
      C("big").textContent = kind === "saved" ? "~" + money(t.saved) : "no saving";
      C("sub").textContent = kind === "saved" ? `saved against Claude${pct > 0 ? ` (${pct}%)` : ""}`
        : kind === "worse" ? `Claude would have cost ${money(-t.saved)} less` : "no saving this period";
      C("runs").textContent = n;
      C("claude").textContent = money(t.claude);
      C("cost").textContent = money(t.cost);
      C("kept").textContent = tokens((call.tin + call.tout) * n) + " tokens";
      C("row").textContent = kind === "saved" ? "~" + money(t.saved) : kind === "worse" ? "-" + money(-t.saved) : "none";
      C("basis").textContent = `${n} measured (runs)`;
      C("count").textContent = n === 1 ? "1 run" : n.toLocaleString("en-US") + " runs";
      card.classList.toggle("none", kind !== "saved");
    };
    $(".sc-row span", card).textContent = clip(call.model, 17);
    for (const box of [pickClaude, pickRuns]) { box.hidden = false; box.addEventListener("change", paint); }
    paint();
  }
})();
