"use strict";

// Appearance belongs to this browser, independently of saved Host configuration.
(() => {
  if (window.top !== window) document.documentElement.classList.add("embedded");
  const key = "agent-host.appearance.v1";
  const system = matchMedia("(prefers-color-scheme: dark)");
  const choices = ["light", "dark", "system"];
  let preference = "system";
  const read = () => {
    try { const value = localStorage.getItem(key); return choices.includes(value) ? value : "system"; }
    catch { return preference; }
  };
  function apply() {
    document.documentElement.dataset.theme = preference === "system" ? (system.matches ? "dark" : "light") : preference;
    document.querySelectorAll("[data-theme-picker]").forEach(input => { input.value = preference; });
    document.querySelectorAll("[data-theme-choice]").forEach(button => {
      button.setAttribute("aria-pressed", String(button.dataset.themeChoice === preference));
    });
  }
  function choose(value) {
    if (!choices.includes(value)) return;
    preference = value;
    let message = "外观已保存，仅影响当前浏览器。";
    try { localStorage.setItem(key, preference); }
    catch { message = "浏览器存储不可用，外观仅在本次页面中生效。"; }
    apply();
    const status = document.querySelector("[data-theme-status]");
    if (status) status.textContent = message;
  }
  preference = read();
  apply();
  system.addEventListener("change", () => { if (preference === "system") apply(); });
  window.addEventListener("storage", event => { if (event.key === key || event.key === null) { preference = read(); apply(); } });
  document.addEventListener("DOMContentLoaded", () => {
    apply();
    document.querySelectorAll("[data-theme-picker]").forEach(input => input.addEventListener("change", () => choose(input.value)));
    document.querySelectorAll("[data-theme-choice]").forEach(button => button.addEventListener("click", () => choose(button.dataset.themeChoice)));
  });
})();
