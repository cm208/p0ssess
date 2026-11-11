// Copy buttons on code blocks, and the current section in "On this page". Both are extras:
// without this script the code is still selectable and the headings still link.
(() => {
  const doc = document;
  const status = doc.getElementById("copy-status");

  const copy = async (text) => {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      const ta = doc.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.className = "sr-only";
      doc.body.append(ta);
      ta.select();
      let ok = false;
      try { ok = doc.execCommand("copy"); } catch { ok = false; }
      ta.remove();
      return ok;
    }
  };

  doc.querySelectorAll(".code").forEach((block) => {
    const pre = block.querySelector("pre");
    if (!pre) return;
    let bar = block.querySelector(".code-bar");
    // One-line blocks keep the button at the end of the line; longer ones get a bar for it.
    if (!bar && pre.textContent.trim().includes("\n")) {
      bar = doc.createElement("div");
      bar.className = "code-bar";
      bar.append(doc.createElement("span"));
      block.prepend(bar);
    }
    const btn = doc.createElement("button");
    btn.type = "button";
    btn.className = "copy-btn";
    btn.textContent = "Copy";
    const lang = bar && bar.querySelector(".code-lang");
    btn.setAttribute("aria-label", lang && lang.textContent ? `Copy ${lang.textContent} code` : "Copy code");
    let timer;
    btn.addEventListener("click", async () => {
      const ok = await copy(pre.innerText.replace(/\n$/, ""));
      btn.textContent = ok ? "Copied" : "Select";
      btn.classList.toggle("done", ok);
      if (status) status.textContent = ok ? "Copied to the clipboard." : "Could not copy; select the text instead.";
      clearTimeout(timer);
      timer = setTimeout(() => {
        btn.textContent = "Copy";
        btn.classList.remove("done");
        if (status) status.textContent = "";
      }, 1800);
    });
    if (bar) bar.append(btn);
    else {
      block.classList.add("bare");
      block.append(btn);
    }
    // A block wider than its box scrolls; make it reachable from the keyboard.
    if (pre.scrollWidth > pre.clientWidth) pre.tabIndex = 0;
  });

  const toc = doc.querySelector(".toc-d");
  if (!toc || !("IntersectionObserver" in window)) return;
  const links = new Map();
  toc.querySelectorAll('a[href^="#"]').forEach((a) => {
    const h = doc.getElementById(decodeURIComponent(a.hash.slice(1)));
    if (h) links.set(h, a);
  });
  const heads = [...links.keys()];
  let current;
  const mark = () => {
    let here = heads[0];
    for (const h of heads) {
      if (h.getBoundingClientRect().top < window.innerHeight * 0.3) here = h;
      else break;
    }
    if (here === current) return;
    if (current) links.get(current).classList.remove("here");
    links.get(here).classList.add("here");
    current = here;
  };
  const io = new IntersectionObserver(mark, { rootMargin: "0px 0px -70% 0px" });
  heads.forEach((h) => io.observe(h));
  mark();
})();
