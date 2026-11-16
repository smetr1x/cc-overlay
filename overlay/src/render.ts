// Перенос модели отображения на страницу. Логики здесь нет: она в view.ts.

import type { Row, View } from "./view";

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className: string, text?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function row(r: Row): HTMLElement {
  const node = el("div", `row tone-${r.tone}`);
  node.dataset.key = r.key;

  const line = el("div", "row-line");
  line.append(el("span", "row-label", r.label), el("span", "row-text", r.text));
  node.append(line);

  if (r.bar !== undefined) {
    const bar = el("div", "bar");
    const fill = el("div", "bar-fill");
    fill.style.width = `${r.bar}%`;
    bar.append(fill);
    node.append(bar);
  }
  if (r.hint) node.append(el("div", "row-hint", r.hint));
  return node;
}

export function render(root: HTMLElement, view: View): void {
  const nodes: HTMLElement[] = [];
  if (view.title) nodes.push(el("header", "title", view.title));
  if (view.banner) nodes.push(el("div", `banner banner-${view.banner.tone}`, view.banner.text));
  for (const r of view.rows) nodes.push(row(r));
  root.replaceChildren(...nodes);
}
