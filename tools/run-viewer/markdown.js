"use strict";
(() => {
  const {rescanMarkdown, parseInlineMarkdown} = globalThis.AGHMarkdownParser;
  const caches = new WeakMap();
  function element(tag, text) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function inline(target, text, depth = 0) {
    for (const piece of parseInlineMarkdown(text)) {
      const tag = {code:"code",strong:"strong",em:"em",link:"a"}[piece.kind] || "span";
      // Local file links need a workspace file API. Until then show their label
      // as text, rather than navigating to an unrelated viewer HTTP endpoint.
      const safeLink = piece.kind === "link" && /^https?:\/\/[^\s\\]+$/i.test(piece.href);
      const child = element(tag === "a" && !safeLink ? "span" : tag);
      if ((piece.kind === "strong" || piece.kind === "em") && depth < 8) inline(child,piece.text,depth + 1);
      else child.textContent = piece.text;
      if (safeLink) { child.href = piece.href; child.target = "_blank"; child.rel = "noopener noreferrer"; }
      target.append(child);
    }
  }
  function blockNode(block) {
    if (block.kind === "hr") return element("hr");
    if (block.kind === "code") {
      const pre = element("pre"); pre.append(element("code", block.text)); return pre;
    }
    if (block.kind === "list") {
      const list = element(block.ordered ? "ol" : "ul");
      if (block.start) list.start = block.start;
      for (const line of block.lines) { const li = element("li"); inline(li,line); list.append(li); }
      return list;
    }
    if (block.kind === "table") {
      const wrap = element("div"); wrap.className = "chat-table";
      const table = element("table"); wrap.append(table);
      block.rows.forEach((row,index) => {
        const tr = element("tr");
        for (const value of row) { const cell = element(index ? "td" : "th"); inline(cell,value); tr.append(cell); }
        table.append(tr);
      });
      return wrap;
    }
    const target = element(block.kind === "quote" ? "blockquote" : block.kind === "heading" ? `h${block.level}` : "p");
    inline(target,block.text); return target;
  }
  globalThis.AGHMarkdown = (target,text) => {
    const previous = caches.get(target);
    const scan = rescanMarkdown(previous?.scan || null,text);
    if (scan === previous?.scan) return;
    const nodes = scan.blocks.map(entry => {
      const cached = previous?.nodes.get(entry.block);
      return [entry.block,cached || blockNode(entry.block)];
    });
    target.replaceChildren(...nodes.map(([,node]) => node));
    caches.set(target,{scan,nodes:new Map(nodes)});
  };
})();
