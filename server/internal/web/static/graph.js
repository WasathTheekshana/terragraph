// Draws the dependency graph from /graph/data as an interactive SVG: layered left to right,
// callers on the left and what they call on the right. No libraries.
(function () {
  "use strict";

  var NS = "http://www.w3.org/2000/svg";
  var NODE_W = 248, NODE_H = 50, GAP_X = 190, GAP_Y = 34, GROUP_PAD = 14, GROUP_HEAD = 26;
  var MAX_CHARS = 33;
  // Past this many edges the labels are hidden until a node or line is hovered.
  var LABEL_LIMIT = 14;

  var root = document.getElementById("graph");
  if (!root) return;
  var svg = document.getElementById("graph-canvas");
  var panel = document.getElementById("graph-panel");
  var notice = document.getElementById("graph-notice");
  var search = document.getElementById("graph-search");

  function el(name, attrs, parent) {
    var e = document.createElementNS(NS, name);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  function clip(s) { return s.length > MAX_CHARS ? s.slice(0, MAX_CHARS - 1) + "…" : s; }
  function text(s) { return document.createTextNode(s); }
  function h(tag, cls, content) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (content != null) e.textContent = content;
    return e;
  }

  var LABELS = { current: "up to date", outdated: "outdated", major_behind: "major version behind", unknown: "unknown" };
  var TYPES = { repo: "Repository", project: "Terraform project", module: "Module", module_repo: "Module repository", local: "Local module", resource: "Resource", external: "Outside the scanned projects" };
  var resourceGraph = root.dataset.src.indexOf("/resources/") === 0;

  var view = { x: 0, y: 0, k: 1 };
  var model = null;
  var selected = null;
  var viewport, edgeLayer, nodeLayer;

  fetch(root.dataset.src, { credentials: "same-origin", headers: { Accept: "application/json" } })
    .then(function (r) {
      return r.json().then(function (body) {
        if (!r.ok) throw new Error(body.error || r.statusText);
        return body;
      });
    })
    .then(start)
    .catch(function (e) { showError(e.message); });

  function showError(msg) {
    root.hidden = true;
    notice.hidden = false;
    notice.textContent = "Could not load the graph: " + msg;
  }

  function start(g) {
    if (g.truncated) {
      notice.hidden = false;
      notice.textContent = "This graph is large: " + g.hidden_nodes + " of the least connected nodes are hidden. Focus on a project, repo, or module to see everything around it.";
    }
    if (!g.nodes.length) {
      root.hidden = true;
      document.getElementById("graph-empty").hidden = false;
      return;
    }
    graphData = g;
    model = layout(g);
    applySaved(model);
    svg.classList.toggle("hide-labels", model.edges.length > LABEL_LIMIT);
    draw(model);
    fit();
    bind();
    var n = model.byId[root.dataset.focus];
    if (n) select(n);
  }

  var graphData = null;

  // applySaved puts nodes back where they were left, for the ones that are still in the graph.
  function applySaved(m) {
    var saved = savedPositions();
    if (!saved) return;
    m.nodes.forEach(function (n) {
      var p = saved[n.id];
      if (p && isFinite(p[0]) && isFinite(p[1])) { n.x = p[0]; n.y = p[1]; }
    });
  }

  function resetLayout() {
    try { localStorage.removeItem(layoutKey); } catch (err) { /* nothing to forget */ }
    model = layout(graphData);
    svg.classList.toggle("hide-labels", model.edges.length > LABEL_LIMIT);
    selected = null;
    selectedEdge = null;
    panel.hidden = true;
    draw(model);
    fit();
  }

  // layout gives every node a layer (column) and a row.
  function layout(g) {
    var groups = {};
    g.nodes.forEach(function (n) { groups[n.id] = n; });
    var isGroup = {};
    g.nodes.forEach(function (n) { if (n.group) isGroup[n.group] = true; });
    var nodes = g.nodes.filter(function (n) { return !isGroup[n.id]; });
    var byId = {};
    nodes.forEach(function (n) { byId[n.id] = n; n.out = []; n.in = []; });
    var edges = g.edges.filter(function (e) { return byId[e.from] && byId[e.to]; });
    edges.forEach(function (e) { e.source = byId[e.from]; e.target = byId[e.to]; });

    // Cycles would never settle into columns, so edges that return to a node still being
    // visited are drawn but left out of the layering.
    var adj = {}, indeg = {}, state = {}, dag = [];
    edges.forEach(function (e) {
      (adj[e.from] = adj[e.from] || []).push(e);
      indeg[e.to] = (indeg[e.to] || 0) + 1;
    });
    function visit(id) {
      state[id] = 1;
      (adj[id] || []).forEach(function (e) {
        if (state[e.to] === 1) { e.back = true; return; }
        dag.push(e);
        if (!state[e.to]) visit(e.to);
      });
      state[id] = 2;
    }
    nodes.slice().sort(function (a, b) { return (indeg[a.id] || 0) - (indeg[b.id] || 0); })
      .forEach(function (n) { if (!state[n.id]) visit(n.id); });
    dag.forEach(function (e) { e.source.out.push(e); e.target.in.push(e); });

    // A node sits one column right of its furthest caller.
    var memo = {};
    function layer(n) {
      if (memo[n.id] != null) return memo[n.id];
      memo[n.id] = 0;
      var l = 0;
      n.in.forEach(function (e) { l = Math.max(l, layer(e.source) + 1); });
      memo[n.id] = l;
      return l;
    }
    var count = 0;
    nodes.forEach(function (n) { n.layer = layer(n); count = Math.max(count, n.layer + 1); });
    var layers = [];
    for (var i = 0; i < count; i++) layers.push([]);
    nodes.slice().sort(function (a, b) { return a.label.localeCompare(b.label); })
      .forEach(function (n) { layers[n.layer].push(n); });
    layers.forEach(function (l) { l.forEach(function (n, i) { n.row = i; }); });

    // Order each column by the average row of its neighbours to cut crossings, keeping the
    // members of one group together.
    function order(l, neighbours) {
      var bary = {};
      l.forEach(function (n) {
        var ns = neighbours(n);
        bary[n.id] = ns.length ? ns.reduce(function (s, m) { return s + m.row; }, 0) / ns.length : n.row;
      });
      var units = {};
      l.forEach(function (n) {
        var key = n.group || n.id;
        (units[key] = units[key] || []).push(n);
      });
      var list = Object.keys(units).map(function (k) {
        var members = units[k].sort(function (a, b) { return bary[a.id] - bary[b.id] || a.label.localeCompare(b.label); });
        var avg = members.reduce(function (s, m) { return s + bary[m.id]; }, 0) / members.length;
        return { avg: avg, members: members, key: k };
      }).sort(function (a, b) { return a.avg - b.avg || a.key.localeCompare(b.key); });
      var r = 0;
      list.forEach(function (u) { u.members.forEach(function (n) { n.row = r++; }); });
      l.sort(function (a, b) { return a.row - b.row; });
    }
    for (var pass = 0; pass < 6; pass++) {
      if (pass % 2 === 0) {
        for (var a = 1; a < layers.length; a++) order(layers[a], function (n) { return n.in.map(function (e) { return e.source; }); });
      } else {
        for (var b = layers.length - 2; b >= 0; b--) order(layers[b], function (n) { return n.out.map(function (e) { return e.target; }); });
      }
    }

    // A group's box needs a heading and padding, so the rows after it move down.
    var tallest = 0;
    var heights = [];
    layers.forEach(function (l, li) {
      var y = 0, prev = null;
      l.forEach(function (n) {
        var grp = n.group && groups[n.group] ? n.group : null;
        if (prev !== null && prev !== grp) y += GROUP_PAD;
        if (grp && prev !== grp) y += GROUP_HEAD;
        n.y = y;
        y += NODE_H + GAP_Y;
        prev = grp;
      });
      heights[li] = y - GAP_Y;
      tallest = Math.max(tallest, heights[li]);
    });
    layers.forEach(function (l, li) {
      var shift = (tallest - heights[li]) / 2;
      l.forEach(function (n) { n.x = li * (NODE_W + GAP_X); n.y += shift; });
    });

    var boxes = [];
    layers.forEach(function (l) {
      var by = {};
      l.forEach(function (n) { if (n.group && groups[n.group]) (by[n.group] = by[n.group] || []).push(n); });
      Object.keys(by).forEach(function (k) {
        var ms = by[k];
        boxes.push({ group: groups[k], members: ms });
      });
    });

    nodes.forEach(function (n) { n.edges = []; });
    edges.forEach(function (e) { e.source.edges.push(e); e.target.edges.push(e); });
    boxes.forEach(function (b) { b.members.forEach(function (n) { n.box = b; }); });
    return { nodes: nodes, edges: edges, boxes: boxes, byId: byId };
  }

  function draw(m) {
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    var defs = el("defs", {}, svg);
    var mk = el("marker", { id: "arrow", viewBox: "0 0 10 10", refX: "9", refY: "5", markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse" }, defs);
    el("path", { d: "M0 0 L10 5 L0 10 z" }, mk);

    viewport = el("g", {}, svg);
    var groupLayer = el("g", {}, viewport);
    edgeLayer = el("g", {}, viewport);
    nodeLayer = el("g", {}, viewport);

    m.boxes.forEach(function (b) {
      var g = el("g", { "class": "group" }, groupLayer);
      b.rect = el("rect", { rx: 12 }, g);
      b.title = el("text", {}, g);
      b.title.appendChild(text(clip(b.group.label)));
      fitBox(b);
      dragTarget(b.rect, function () { return b.members; });
    });

    m.edges.forEach(function (e) {
      var cls = "edge s-" + e.status + (e.origins.length === 1 ? " o-" + e.origins[0] : "") + (e.cycle ? " cycle" : "");
      var g = el("g", {}, edgeLayer);
      e.path = el("path", { "class": cls, "marker-end": "url(#arrow)" }, g);
      el("title", {}, e.path).appendChild(text(edgeTitle(e)));
      e.hit = el("path", { "class": "edge-hit" }, g);
      e.hit.addEventListener("click", function (ev) { ev.stopPropagation(); selectEdge(e); });
      e.hit.addEventListener("mouseenter", function () { highlightEdge(e); });
      e.hit.addEventListener("mouseleave", refreshHighlight);
      var label = edgeLabel(e);
      if (label) {
        e.labelEl = el("g", { "class": "edge-label" }, g);
        var w = Math.max(28, label.length * 6 + 12);
        el("rect", { x: -w / 2, y: -8, width: w, height: 16, rx: 8 }, e.labelEl);
        el("text", { y: 3 }, e.labelEl).appendChild(text(label));
      }
      placeEdge(e);
    });

    m.nodes.forEach(function (n) {
      var g = el("g", { "class": "node type-" + n.type + (n.external ? " external" : "") + (n.cycle ? " cycle" : ""), transform: "translate(" + n.x + " " + n.y + ")", tabindex: "0", role: "link", "data-id": n.id }, nodeLayer);
      n.el = g;
      el("title", {}, g).appendChild(text(n.label));
      el("rect", { "class": "box", width: NODE_W, height: NODE_H, rx: 8 }, g);
      el("rect", { "class": "stripe", width: 5, height: NODE_H - 12, x: 0, y: 6, rx: 2 }, g);
      el("text", { x: 16, y: 22 }, g).appendChild(text(clip(n.label)));
      el("text", { x: 16, y: 39, "class": "sub" }, g).appendChild(text(clip(subline(n))));
      var pill = pillOf(n);
      if (pill) {
        var pg = el("g", { transform: "translate(" + (NODE_W - 20) + " 8)" }, g);
        el("rect", { "class": "pill" + (pill.bad ? " bad" : ""), x: -14, width: 30, height: 15, rx: 7.5 }, pg);
        el("text", { "class": "pill-text", x: 1, y: 11 }, pg).appendChild(text(pill.text));
      }
      g.addEventListener("click", function (ev) {
        ev.stopPropagation();
        if (!suppressClick) select(n);
      });
      dragTarget(g, function () { return [n]; });
      g.addEventListener("keydown", function (ev) { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); select(n); } });
      g.addEventListener("mouseenter", function () { highlight(n); });
      g.addEventListener("mouseleave", refreshHighlight);
    });
  }

  // fitBox sizes a group's box around its members, wherever they have been moved to.
  function fitBox(b) {
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    b.members.forEach(function (n) {
      x0 = Math.min(x0, n.x); y0 = Math.min(y0, n.y);
      x1 = Math.max(x1, n.x + NODE_W); y1 = Math.max(y1, n.y + NODE_H);
    });
    b.x = x0 - GROUP_PAD; b.y = y0 - GROUP_HEAD;
    b.rect.setAttribute("x", b.x); b.rect.setAttribute("y", b.y);
    b.rect.setAttribute("width", x1 - x0 + GROUP_PAD * 2); b.rect.setAttribute("height", y1 - y0 + GROUP_HEAD + GROUP_PAD);
    b.title.setAttribute("x", b.x + 12); b.title.setAttribute("y", b.y + 17);
  }

  // placeEdge draws an edge from the side of its source that faces its target, so a block can be
  // moved anywhere and its lines still leave and arrive sensibly.
  function placeEdge(e) {
    var s = e.source, t = e.target, d, x1, y1, x2, y2;
    if (t.x >= s.x + NODE_W) {
      x1 = s.x + NODE_W; x2 = t.x; y1 = s.y + NODE_H / 2; y2 = t.y + NODE_H / 2;
      var mx = (x1 + x2) / 2;
      d = "M" + x1 + " " + y1 + " C" + mx + " " + y1 + " " + mx + " " + y2 + " " + x2 + " " + y2;
    } else if (t.x + NODE_W <= s.x) {
      x1 = s.x; x2 = t.x + NODE_W; y1 = s.y + NODE_H / 2; y2 = t.y + NODE_H / 2;
      var mx2 = (x1 + x2) / 2;
      d = "M" + x1 + " " + y1 + " C" + mx2 + " " + y1 + " " + mx2 + " " + y2 + " " + x2 + " " + y2;
    } else {
      var down = t.y >= s.y;
      x1 = s.x + NODE_W / 2; x2 = t.x + NODE_W / 2;
      y1 = down ? s.y + NODE_H : s.y; y2 = down ? t.y : t.y + NODE_H;
      var my = (y1 + y2) / 2;
      d = "M" + x1 + " " + y1 + " C" + x1 + " " + my + " " + x2 + " " + my + " " + x2 + " " + y2;
    }
    e.path.setAttribute("d", d);
    e.hit.setAttribute("d", d);
    if (e.labelEl) e.labelEl.setAttribute("transform", "translate(" + (x1 + x2) / 2 + " " + (y1 + y2) / 2 + ")");
  }

  var suppressClick = false;
  var layoutKey = "terragraph-layout:" + root.dataset.src;

  function savePositions() {
    try {
      var out = {};
      model.nodes.forEach(function (n) { out[n.id] = [Math.round(n.x), Math.round(n.y)]; });
      localStorage.setItem(layoutKey, JSON.stringify(out));
    } catch (err) { /* storage may be unavailable; the layout just isn't remembered */ }
  }

  function savedPositions() {
    try { return JSON.parse(localStorage.getItem(layoutKey) || "null"); } catch (err) { return null; }
  }

  function moveNodes(nodes, dx, dy) {
    var edges = new Set(), boxes = new Set();
    nodes.forEach(function (n) {
      n.x += dx; n.y += dy;
      n.el.setAttribute("transform", "translate(" + n.x + " " + n.y + ")");
      n.edges.forEach(function (e) { edges.add(e); });
      if (n.box) boxes.add(n.box);
    });
    edges.forEach(placeEdge);
    boxes.forEach(fitBox);
  }

  // dragTarget lets an element be dragged with the mouse, moving the nodes it stands for. A press
  // that doesn't move is left to be a click.
  function dragTarget(elem, nodesOf) {
    elem.addEventListener("pointerdown", function (ev) {
      if (ev.button !== 0) return;
      ev.stopPropagation();
      var start = { x: ev.clientX, y: ev.clientY }, last = start, moved = false;
      elem.setPointerCapture(ev.pointerId);
      function move(m) {
        var dx = m.clientX - last.x, dy = m.clientY - last.y;
        if (!moved && Math.abs(m.clientX - start.x) + Math.abs(m.clientY - start.y) < 4) return;
        if (!moved) { moved = true; svg.classList.add("moving"); }
        last = { x: m.clientX, y: m.clientY };
        moveNodes(nodesOf(), dx / view.k, dy / view.k);
      }
      function up(u) {
        elem.removeEventListener("pointermove", move);
        elem.removeEventListener("pointerup", up);
        elem.removeEventListener("pointercancel", up);
        if (elem.hasPointerCapture(u.pointerId)) elem.releasePointerCapture(u.pointerId);
        svg.classList.remove("moving");
        if (moved) {
          savePositions();
          suppressClick = true;
          setTimeout(function () { suppressClick = false; }, 0);
        }
      }
      elem.addEventListener("pointermove", move);
      elem.addEventListener("pointerup", up);
      elem.addEventListener("pointercancel", up);
    });
  }

  function subline(n) {
    if (n.sub) return n.sub;
    switch (n.type) {
      case "repo": return n.projects + (n.projects === 1 ? " project" : " projects") + ", " + (n.module_calls || 0) + " module calls";
      case "project": return n.path && n.path !== "." ? n.path : "repository root";
      case "module":
        if (n.latest_version) return "latest " + n.latest_version + (n.consumers ? " · " + n.consumers + " using" : "");
        return n.consumers ? n.consumers + " using" : (n.kind || "module");
      case "local": return n.source || "local module";
    }
    return "";
  }

  function pillOf(n) {
    if (n.major_behind_calls) return { text: n.major_behind_calls + "↑", bad: true };
    if (n.outdated_calls) return { text: n.outdated_calls + "↑" };
    if (n.drift) return { text: "drift" };
    return null;
  }

  function edgeLabel(e) {
    if (e.label) return clip(e.label);
    if (!e.versions || !e.versions.length) return "";
    if (e.versions.length === 1) return clip(e.versions[0]);
    return e.versions[0] + " +" + (e.versions.length - 1);
  }

  function edgeTitle(e) {
    var parts = [e.source.label + " → " + e.target.label];
    if (e.versions && e.versions.length) parts.push("versions: " + e.versions.join(", "));
    parts.push(LABELS[e.status] || e.status);
    parts.push(e.origins.join(", "));
    if (e.via && e.via.length) parts.push("through " + e.via.join(", "));
    if (e.details && e.details.length) parts.push(e.details.join("\n"));
    return parts.join("\n");
  }

  // highlight dims everything except a node and what it is directly connected to.
  function highlight(n) {
    model.nodes.forEach(function (m) { m.el.classList.remove("dim"); });
    model.edges.forEach(function (e) {
      e.path.classList.remove("hot");
      e.path.parentNode.classList.remove("dim");
      if (e.labelEl) e.labelEl.classList.remove("show");
    });
    if (!n) return;
    var near = {};
    near[n.id] = true;
    model.edges.forEach(function (e) {
      if (e.from === n.id || e.to === n.id) {
        near[e.from] = near[e.to] = true;
        e.path.classList.add("hot");
        if (e.labelEl) e.labelEl.classList.add("show");
      } else {
        e.path.parentNode.classList.add("dim");
      }
    });
    model.nodes.forEach(function (m) { if (!near[m.id]) m.el.classList.add("dim"); });
  }

  var selectedEdge = null;

  function refreshHighlight() {
    if (selectedEdge) highlightEdge(selectedEdge);
    else highlight(selected);
  }

  // highlightEdge dims everything except one edge and the two nodes it joins.
  function highlightEdge(e) {
    highlight(null);
    model.nodes.forEach(function (m) { if (m !== e.source && m !== e.target) m.el.classList.add("dim"); });
    model.edges.forEach(function (o) { if (o !== e) o.path.parentNode.classList.add("dim"); });
    e.path.classList.add("hot");
    if (e.labelEl) e.labelEl.classList.add("show");
  }

  function selectEdge(e) {
    if (selected) selected.el.classList.remove("selected");
    selected = null;
    selectedEdge = e;
    highlightEdge(e);
    showEdgePanel(e);
  }

  function showEdgePanel(e) {
    panel.replaceChildren();
    panel.hidden = false;
    panel.appendChild(h("p", "text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400", "Dependency"));
    panel.appendChild(h("h2", "mt-1 text-base font-semibold break-all", e.source.label + " \u2192 " + e.target.label));
    var dl = h("dl", "mt-3 text-sm");
    dl.appendChild(row("From", e.source.sub ? e.source.label + " (" + e.source.sub + ")" : e.source.label));
    dl.appendChild(row("To", e.target.sub ? e.target.label + " (" + e.target.sub + ")" : e.target.label));
    dl.appendChild(row("Calls", String(e.calls)));
    if (e.versions && e.versions.length) dl.appendChild(row("Versions", e.versions.join(", ")));
    dl.appendChild(row("Status", LABELS[e.status] || e.status));
    if (e.origins && e.origins.length) dl.appendChild(row("Seen as", e.origins.join(", ")));
    if (e.via && e.via.length) dl.appendChild(row("Through", e.via.join(", ")));
    panel.appendChild(dl);
    if (e.items && e.items.length) {
      panel.appendChild(h("h3", HEADING, "What it is for"));
      var items = h("ul", "mt-1 font-mono text-xs");
      e.items.forEach(function (i) { items.appendChild(h("li", "py-0.5 break-all", i)); });
      panel.appendChild(items);
    }
    if (e.details && e.details.length) {
      panel.appendChild(h("h3", HEADING, "Lookups"));
      var list = h("ul", "mt-1 space-y-2 text-sm");
      e.details.forEach(function (d) { list.appendChild(h("li", "break-all", d)); });
      panel.appendChild(list);
    }
    var actions = h("div", "mt-5 flex flex-col gap-1.5 border-t border-slate-200 pt-4 text-sm dark:border-slate-800");
    [e.source, e.target].forEach(function (n) {
      var b = h("button", "text-left font-medium text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-300", "Select " + n.label);
      b.type = "button";
      b.addEventListener("click", function () { select(n); centerOn(n); });
      actions.appendChild(b);
    });
    panel.appendChild(actions);
  }

  function select(n) {
    selectedEdge = null;
    if (selected) selected.el.classList.remove("selected");
    selected = n;
    if (n) n.el.classList.add("selected");
    highlight(n);
    showPanel(n);
  }

  function link(href, label) {
    var a = h("a", "font-medium text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-300", label);
    a.href = href;
    return a;
  }

  function focusURL(n, direction) {
    var m = /^(project|repo|module):(\d+)/.exec(n.id);
    if (!m) return null;
    var q = new URLSearchParams(window.location.search);
    ["project", "repo", "module", "direction"].forEach(function (k) { q.delete(k); });
    q.set(m[1], m[2]);
    q.set("direction", direction);
    return (resourceGraph ? "/resources/graph?" : "/graph?") + q.toString();
  }

  function row(label, value) {
    var r = h("div", "flex justify-between gap-3 py-1");
    r.appendChild(h("dt", "shrink-0 text-slate-500 dark:text-slate-400", label));
    r.appendChild(h("dd", "text-right break-all", value));
    return r;
  }

  var HEADING = "mt-4 text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400";

  function showPanel(n) {
    panel.replaceChildren();
    if (!n) { panel.hidden = true; return; }
    panel.hidden = false;
    panel.appendChild(h("p", "text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400", TYPES[n.type] || n.type));
    panel.appendChild(h("h2", "mt-1 text-base font-semibold break-all", n.label));

    var dl = h("dl", "mt-3 text-sm");
    var generic = n.details && n.details.length > 0;
    if (generic) n.details.forEach(function (d) { dl.appendChild(row(d.label, d.value)); });
    if (n.key) dl.appendChild(row("Source", n.key + (n.subdir ? "//" + n.subdir : "")));
    if (n.repo_url) dl.appendChild(row("Repository", n.repo_url));
    if (n.type === "project" && n.path) dl.appendChild(row("Path", n.path));
    if (n.branch) dl.appendChild(row("Branch", n.branch));
    if (n.latest_version) dl.appendChild(row("Latest version", n.latest_version));
    if (n.type === "repo" && !generic) dl.appendChild(row("Projects", String(n.projects)));
    if ((n.type === "repo" || n.type === "project") && !generic) {
      dl.appendChild(row("Module calls", String(n.module_calls || 0)));
      if (n.outdated_calls) dl.appendChild(row("Outdated", String(n.outdated_calls)));
      if (n.major_behind_calls) dl.appendChild(row("Major behind", String(n.major_behind_calls)));
    }
    if (n.type === "module") dl.appendChild(row("Used by", n.consumers + (n.consumers === 1 ? " project" : " projects")));
    if (n.source) dl.appendChild(row("Path", n.source));
    if (n.last_scan_at) dl.appendChild(row("Scanned", new Date(n.last_scan_at).toLocaleString()));
    panel.appendChild(dl);

    var flags = [];
    if (n.cycle) flags.push("Part of a dependency cycle.");
    if (n.drift) flags.push("More than one version is in use.");
    if (n.external) flags.push("This module's own repo has not been scanned, so what it depends on is unknown.");
    if (n.unused) flags.push("No scanned project uses it.");
    flags.forEach(function (f) {
      panel.appendChild(h("p", "mt-3 rounded-md bg-amber-50 px-2.5 py-1.5 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-300", f));
    });

    if (n.versions && n.versions.length) {
      panel.appendChild(h("h3", HEADING, "Versions in use"));
      var ul = h("ul", "mt-1 text-sm");
      n.versions.forEach(function (v) {
        var li = h("li", "flex justify-between gap-3 py-0.5");
        li.appendChild(h("span", "font-mono text-xs", v.version));
        li.appendChild(h("span", "text-xs text-slate-500 dark:text-slate-400", v.projects + (v.projects === 1 ? " project, " : " projects, ") + (LABELS[v.status] || v.status)));
        ul.appendChild(li);
      });
      panel.appendChild(ul);
    }

    [["Depends on", model.edges.filter(function (e) { return e.from === n.id; }), "target"],
     ["Used by", model.edges.filter(function (e) { return e.to === n.id; }), "source"]].forEach(function (s) {
      if (!s[1].length) return;
      panel.appendChild(h("h3", HEADING, s[0] + " (" + s[1].length + ")"));
      var list = h("ul", "mt-1 text-sm");
      s[1].forEach(function (e) {
        var other = e[s[2]];
        var li = h("li", "py-0.5");
        var b = h("button", "text-left break-all hover:text-amber-700 dark:hover:text-amber-400", other.label);
        b.type = "button";
        b.addEventListener("click", function () { select(other); centerOn(other); });
        li.appendChild(b);
        list.appendChild(li);
      });
      panel.appendChild(list);
    });

    var actions = h("div", "mt-5 flex flex-col gap-1.5 border-t border-slate-200 pt-4 text-sm dark:border-slate-800");
    if (n.href) actions.appendChild(link(n.href, "Open page"));
    if (n.repo_href) actions.appendChild(link(n.repo_href, "Open the module's repo"));
    if (focusURL(n, "down")) {
      actions.appendChild(link(focusURL(n, "down"), resourceGraph ? "Graph what it looks up" : "Graph what it depends on"));
      actions.appendChild(link(focusURL(n, "up"), resourceGraph ? "Graph who looks up what it creates" : "Graph what depends on it"));
      actions.appendChild(link(focusURL(n, "both"), "Graph both directions"));
    }
    panel.appendChild(actions);
  }

  function applyView() {
    viewport.setAttribute("transform", "translate(" + view.x + " " + view.y + ") scale(" + view.k + ")");
  }

  function fit() {
    var r = svg.getBoundingClientRect();
    var pad = 48, x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    model.nodes.forEach(function (n) {
      x0 = Math.min(x0, n.x); y0 = Math.min(y0, n.y - GROUP_HEAD);
      x1 = Math.max(x1, n.x + NODE_W); y1 = Math.max(y1, n.y + NODE_H + GROUP_PAD);
    });
    var w = x1 - x0, h = y1 - y0;
    var k = Math.min((r.width - pad * 2) / w, (r.height - pad * 2) / h, 1.2);
    view.k = Math.max(k, 0.15);
    view.x = (r.width - w * view.k) / 2 - x0 * view.k;
    view.y = (r.height - h * view.k) / 2 - y0 * view.k;
    applyView();
  }

  function zoomAt(factor, cx, cy) {
    var k = Math.min(3, Math.max(0.1, view.k * factor));
    factor = k / view.k;
    view.x = cx - (cx - view.x) * factor;
    view.y = cy - (cy - view.y) * factor;
    view.k = k;
    applyView();
  }

  function centerOn(n) {
    var r = svg.getBoundingClientRect();
    view.x = r.width / 2 - (n.x + NODE_W / 2) * view.k;
    view.y = r.height / 2 - (n.y + NODE_H / 2) * view.k;
    applyView();
  }

  function bind() {
    var drag = null;
    svg.addEventListener("pointerdown", function (ev) {
      if (ev.target.closest(".node") || ev.target.closest(".edge-hit") || ev.target.closest(".group")) return;
      drag = { x: ev.clientX, y: ev.clientY, vx: view.x, vy: view.y, moved: false };
      svg.setPointerCapture(ev.pointerId);
      svg.classList.add("dragging");
    });
    svg.addEventListener("pointermove", function (ev) {
      if (!drag) return;
      var dx = ev.clientX - drag.x, dy = ev.clientY - drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 3) drag.moved = true;
      view.x = drag.vx + dx;
      view.y = drag.vy + dy;
      applyView();
    });
    function end(ev) {
      if (!drag) return;
      if (!drag.moved) { selectedEdge = null; select(null); }
      drag = null;
      svg.classList.remove("dragging");
      if (svg.hasPointerCapture(ev.pointerId)) svg.releasePointerCapture(ev.pointerId);
    }
    svg.addEventListener("pointerup", end);
    svg.addEventListener("pointercancel", end);
    svg.addEventListener("wheel", function (ev) {
      ev.preventDefault();
      var r = svg.getBoundingClientRect();
      zoomAt(ev.deltaY < 0 ? 1.12 : 1 / 1.12, ev.clientX - r.left, ev.clientY - r.top);
    }, { passive: false });

    function zoomCentre(f) { var r = svg.getBoundingClientRect(); zoomAt(f, r.width / 2, r.height / 2); }
    document.getElementById("graph-zoom-in").addEventListener("click", function () { zoomCentre(1.25); });
    document.getElementById("graph-zoom-out").addEventListener("click", function () { zoomCentre(0.8); });
    document.getElementById("graph-fit").addEventListener("click", fit);
    document.getElementById("graph-reset").addEventListener("click", resetLayout);
    document.getElementById("graph-labels").addEventListener("click", function () { svg.classList.toggle("hide-labels"); });
    window.addEventListener("resize", fit);

    var firstMatch = null;
    search.addEventListener("input", function () {
      var q = search.value.trim().toLowerCase();
      firstMatch = null;
      model.nodes.forEach(function (n) {
        var hit = q !== "" && [n.label, n.key, n.repo_url].some(function (s) { return (s || "").toLowerCase().indexOf(q) >= 0; });
        n.el.classList.toggle("match", hit);
        if (hit && !firstMatch) firstMatch = n;
      });
    });
    search.addEventListener("keydown", function (ev) {
      if (ev.key === "Enter") {
        ev.preventDefault();
        if (firstMatch) { select(firstMatch); centerOn(firstMatch); }
      }
    });
  }
})();
