"use strict";
(() => {
  const button = document.getElementById("close-chat");
  const panel = document.getElementById("chat-panel");
  const canClose = () => location.hash.split("?")[0] === "#chat" ||
    location.hash.startsWith("#executions?") && new URLSearchParams(location.hash.split("?")[1]).has("key");
  function close() {
    if (!canClose()) return false;
    panel.hidden = true;
    document.getElementById("chat-toggle").setAttribute("aria-expanded", "false");
    location.hash = "workbench";
    document.querySelector('[data-view="workbench"]').focus();
    return true;
  }
  function keydown(event) {
    if (event.defaultPrevented || event.repeat || event.isComposing || !event.metaKey ||
        event.ctrlKey || event.altKey || event.shiftKey || event.key.toLowerCase() !== "w" || !canClose()) return;
    event.preventDefault();
    event.stopPropagation();
    close();
  }
  button.addEventListener("click", close);
  const update = () => { button.hidden = !canClose(); };
  window.addEventListener("hashchange", update);
  document.addEventListener("keydown", keydown, true);
  document.querySelectorAll("iframe").forEach(frame => frame.addEventListener("load", () => {
    frame.contentDocument?.addEventListener("keydown", keydown, true);
  }));
  update();
})();
