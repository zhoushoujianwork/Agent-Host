"use strict";
(function (root) {
  const ALL = "__all_scopes__";
  const MISSING = "__missing_scope__";
  const value = scope => typeof scope === "string" && scope ? scope : MISSING;
  const label = scope => {
    if (!scope || scope === MISSING) return "未记录 scope";
    const parts = scope.split("/").filter(Boolean);
    return parts.slice(-2).join("/") || scope;
  };
  const matches = (selected, scope) => selected === ALL || selected === value(scope);
  const options = items => {
    const counts = new Map();
    for (const item of items || []) {
      const key = value(item?.memory?.scope);
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return [{value: ALL, label: "全部 Scope", count: items?.length || 0},
      ...[...counts].sort(([left], [right]) => left.localeCompare(right))
        .map(([key, count]) => ({value: key, label: label(key), count}))];
  };
  const api = {ALL, MISSING, value, label, matches, options};
  if (typeof module !== "undefined") module.exports = api;
  if (root) root.AGHMemoryScope = api;
})(typeof window !== "undefined" ? window : undefined);
