/**
 * Turns a nav of in-page links into an ARIA tablist. Without JavaScript the links
 * stay plain anchors to sections that are all visible.
 */
export function bindTabs(nav: HTMLElement): void {
  const tabs = [...nav.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')];
  const panels = tabs.map((tab) => document.getElementById(tab.hash.slice(1))!);
  nav.setAttribute("role", "tablist");

  const select = (index: number, focus = false) => {
    tabs.forEach((tab, i) => {
      tab.setAttribute("aria-selected", String(i === index));
      tab.tabIndex = i === index ? 0 : -1;
      panels[i].hidden = i !== index;
    });
    if (focus) tabs[index].focus();
  };

  tabs.forEach((tab, i) => {
    tab.id = `tab-${panels[i].id}`;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", panels[i].id);
    panels[i].setAttribute("role", "tabpanel");
    panels[i].setAttribute("aria-labelledby", tab.id);
    panels[i].tabIndex = 0;
    tab.addEventListener("click", (event) => {
      event.preventDefault();
      select(i);
      // keeps the tab across reloads and pager links
      history.replaceState(null, "", tab.hash);
    });
    tab.addEventListener("keydown", (event) => {
      const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[event.key];
      if (next === undefined) return;
      event.preventDefault();
      select((next + tabs.length) % tabs.length, true);
    });
  });

  const fromHash = panels.findIndex((panel) => `#${panel.id}` === location.hash);
  select(Math.max(0, fromHash));
}
