"use strict";
(() => {
  const button = document.getElementById("close-chat");
  const panel = document.getElementById("chat-panel");
  const detailOpen = () => location.hash.startsWith("#executions?") && new URLSearchParams(location.hash.split("?")[1]).has("key");
  const canClose = () => !panel.hidden || detailOpen();
  function close() {
    if (!canClose()) return false;
    if (!panel.hidden) {
      window.showChat(false);
      document.getElementById("chat-toggle").focus();
      return true;
    }
    location.hash = "executions";
    document.querySelector('[data-view="executions"]').focus();
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
  const update = () => {
    button.hidden = !canClose();
    const label = !panel.hidden ? "关闭右侧 Chat" : "关闭当前对话";
    button.setAttribute("aria-label", label);
    button.title = `${label} (⌘W)`;
  };
  window.addEventListener("hashchange", update);
  document.addEventListener("agh:chat-visibility", update);
  document.addEventListener("keydown", keydown, true);
  document.querySelectorAll("iframe").forEach(frame => frame.addEventListener("load", () => {
    frame.contentDocument?.addEventListener("keydown", keydown, true);
  }));
  update();
})();
