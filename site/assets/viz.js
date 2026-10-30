/*
 * Load before the page's runtime script. Bind inside its existing IIFE.
 * All prose and node labels are supplied by that script.
 */
(function () {
  "use strict";

  var NS = "http://www.w3.org/2000/svg";
  var serial = 0;

  window.RakitsuViz = {
    bind: function (page) {
      var el = page.el;
      var T = page.T;
      var L = page.L;
      var codeCell = page.codeCell;
      var anims = page.anims;
      var keep = page.keep || {};
      var motion = window.matchMedia("(prefers-reduced-motion: reduce)");

      function S(tag, attrs, parent, text) {
        var e = document.createElementNS(NS, tag);
        Object.keys(attrs || {}).forEach(function (k) {
          e.setAttribute(k, attrs[k]);
        });
        if (text != null) e.textContent = text;
        if (parent) parent.appendChild(e);
        return e;
      }

      function svgIn(host, vb, label, interactive) {
        var box = el("div", "figbox");
        host.appendChild(box);
        var svg = S("svg", {
          viewBox: vb,
          preserveAspectRatio: "xMidYMid meet",
          "class": "rk-viz",
          role: interactive ? "group" : "img",
          "aria-label": label
        }, box);
        var defs = S("defs", {}, svg);
        var id = "rk-viz-" + (++serial);
        ["muted", "active"].forEach(function (kind) {
          var m = S("marker", {
            id: id + "-" + kind,
            viewBox: "0 0 10 10",
            refX: 9,
            refY: 5,
            markerWidth: 10,
            markerHeight: 10,
            markerUnits: "userSpaceOnUse",
            orient: "auto"
          }, defs);
          S("path", {
            d: "M1 1 L9 5 L1 9 Z",
            "class": "rk-arrow-" + kind
          }, m);
        });
        svg._markers = {
          muted: "url(#" + id + "-muted)",
          active: "url(#" + id + "-active)"
        };
        svg._edges = S("g", {}, svg);
        svg._nodes = S("g", {}, svg);
        return svg;
      }

      function pd(points) {
        return points.map(function (p, i) {
          return (i ? "L" : "M") + p[0] + "," + p[1];
        }).join(" ");
      }

      function edge(svg, points, label) {
        var g = S("g", {"class": "rk-edge"}, svg._edges);
        var d = typeof points === "string" ? points : pd(points);
        S("path", {
          d: d,
          "class": "rk-track",
          "aria-hidden": "true"
        }, g);
        var p = S("path", {
          d: d,
          "class": "rk-dashes",
          "marker-end": svg._markers.muted,
          "aria-hidden": "true"
        }, g);
        if (label) {
          S("text", {
            x: label.x,
            y: label.y,
            "text-anchor": label.anchor || "middle",
            "class": "rk-edge-label"
          }, g, label.text);
        }
        return {
          g: g,
          path: p,
          live: function (on) {
            g.classList.toggle("on", !!on);
            p.setAttribute("marker-end",
              on ? svg._markers.active : svg._markers.muted);
          }
        };
      }

      function node(svg, n) {
        var g = S("g", {"class": "nd"}, svg._nodes);
        S("rect", {
          x: n.x - 4,
          y: n.y - 4,
          width: n.w + 8,
          height: n.h + 8,
          rx: 10,
          "class": "rk-ring"
        }, g);
        S("rect", {
          x: n.x,
          y: n.y,
          width: n.w,
          height: n.h,
          rx: 6,
          "class": "nb"
        }, g);
        var title = S("text", {
          x: n.x + n.w / 2,
          y: n.y + (n.sub ? n.h / 2 - 3 : n.h / 2 + 4),
          "text-anchor": "middle"
        }, g, n.label);
        fit(title, n.label, n.w - 12, 7.2);
        if (n.sub) {
          var sub = S("text", {
            x: n.x + n.w / 2,
            y: n.y + n.h / 2 + 12,
            "text-anchor": "middle",
            "class": "sub"
          }, g, n.sub);
          fit(sub, n.sub, n.w - 12, 6.3);
        }
        return g;
      }

      function fit(text, content, width, unit) {
        var estimated = Array.from(String(content)).reduce(function (sum, c) {
          return sum + (c.charCodeAt(0) > 255 ? unit * 1.7 : unit);
        }, 0);
        if (estimated > width) {
          text.setAttribute("textLength", width);
          text.setAttribute("lengthAdjust", "spacingAndGlyphs");
        }
      }

      function activeNodes(groups, id, bad) {
        Object.keys(groups).forEach(function (k) {
          groups[k].classList.toggle("on", k === id && !bad);
          groups[k].classList.toggle("bad", k === id && !!bad);
          if (groups[k].getAttribute("role") === "button") {
            groups[k].setAttribute("aria-pressed", String(k === id));
          }
        });
      }

      function nodeSet(svg, nodes) {
        var groups = {};
        Object.keys(nodes).forEach(function (k) {
          groups[k] = node(svg, nodes[k]);
        });
        return groups;
      }

      /*
       * Uses the page's existing frame()/anims lifecycle.
       * Step still advances once and pauses; Play resumes the current phase.
       * Reduced motion suppresses marching even after an explicit Play.
       */
      function Anim(host, o) {
        var a = {
          i: motion.matches && o.rest ? o.rest.i : (o.start || 0),
          t: motion.matches && o.rest ? o.rest.t : 0,
          playing: !motion.matches,
          last: 0,
          dead: false,
          stepped: false,
          hold: 0,
          visible: true
        };
        var controls = el("div", "ctrls");
        var play = el("button");
        var step = el("button");
        play.type = step.type = "button";
        controls.appendChild(play);
        controls.appendChild(step);
        host.appendChild(controls);

        function sync() {
          play.textContent = a.playing ? T("Pause", "一時停止") : T("Play", "再生");
          play.setAttribute("aria-pressed", String(a.playing));
          step.textContent = T("Step", "1 ステップ");
          host.classList.toggle("rk-running", a.playing && !motion.matches);
        }

        play.onclick = function () {
          a.playing = !a.playing;
          a.last = 0;
          sync();
        };
        step.onclick = function () {
          a.playing = false;
          a.last = 0;
          a.stepped = true;
          a.hold = 0;
          a.i = (a.i + 1) % o.n;
          a.t = 1;
          sync();
          o.draw(a.i, a.t);
        };
        a.step = function (now) {
          if (!a.playing || a.dead) return;
          if (!a.visible) {
            a.last = 0;
            return;
          }
          if (!host.isConnected || document.hidden) {
            a.last = 0;
            return;
          }
          if (!a.last) {
            a.last = now;
            return;
          }
          var dt = Math.min(100, now - a.last);
          a.last = now;
          var dur = typeof o.dur === "function" ? o.dur(a.i) : o.dur;
          if (a.stepped) {
            /* Step then Play: keep the stepped frame on screen for a while. */
            a.hold += dt;
            if (a.hold < dur * 0.6) return;
            a.stepped = false;
            a.hold = 0;
            a.t = 1;
          }
          a.t += dt / dur;
          if (a.t >= 1) {
            a.t = 0;
            a.i = (a.i + 1) % o.n;
          }
          o.draw(a.i, a.t);
        };
        a.goto = function (i) {
          a.playing = false;
          a.last = 0;
          a.i = Math.max(0, Math.min(o.n - 1, i));
          a.t = 1;
          sync();
          o.draw(a.i, a.t);
        };
        a.pause = function () {
          a.playing = false;
          a.last = 0;
          sync();
        };
        a.restore = function (s) {
          a.i = s.i % o.n;
          a.t = s.t;
          a.playing = s.playing;
          a.last = 0;
          a.stepped = false;
          a.hold = 0;
          sync();
          o.draw(a.i, a.t);
        };
        if (window.IntersectionObserver) {
          a.io = new IntersectionObserver(function (es) {
            es.forEach(function (e) { a.visible = e.isIntersecting; });
          });
          a.io.observe(host);
        }
        a.kill = function () {
          if (a.io) a.io.disconnect();
          a.dead = true;
          host.classList.remove("rk-running");
          var i = anims.indexOf(a);
          if (i !== -1) anims.splice(i, 1);
        };
        a.motionChanged = function () {
          if (motion.matches) {
            a.playing = false;
            a.last = 0;
          }
          sync();
        };
        anims.push(a);
        sync();
        o.draw(a.i, a.t);
        return a;
      }

      function motionChanged() {
        anims.slice().forEach(function (a) {
          if (a.motionChanged) a.motionChanged();
        });
      }
      if (motion.addEventListener) {
        motion.addEventListener("change", motionChanged);
      } else {
        motion.addListener(motionChanged);
      }

      function hero(fig, original, caps, label) {
        var svg = svgIn(fig, "0 0 640 290", label);
        var N = {};
        Object.keys(original).forEach(function (k) {
          N[k] = Object.assign({}, original[k]);
        });
        N.answer.y = 14;
        N.think.y = N.act.y = N.obs.y = 102;
        N.refl.y = 218;

        var E = [
          edge(svg, [[140,130],[260,130]], {
            x: 200, y: 115, text: T("tool_call", "ツール呼出")
          }),
          edge(svg, [[380,130],[500,130]], {
            x: 440, y: 115, text: T("result", "結果")
          }),
          edge(svg, [[560,158],[560,240],[380,240]]),
          edge(svg, [[260,240],[80,240],[80,158]]),
          edge(svg, [[80,102],[80,54]]),
          edge(svg, [[600,158],[600,276],[40,276],[40,158]], {
            x: 510, y: 266, text: T("reflection off", "reflection なし")
          })
        ];
        var G = nodeSet(svg, N);
        var cap = el("p", "cap");
        fig.appendChild(cap);
        /* Order: think, act, observe, (reflect), think again, answer. */
        var steps = ["think","act","obs","refl","think","answer"];
        var lit = [[0], [1], [5, 2], [3], [4], []];
        return Anim(fig, {
          n: caps.length,
          dur: 1500,
          rest: {i: caps.length - 1, t: 1},
          draw: function (i) {
            activeNodes(G, steps[i]);
            E.forEach(function (e, k) {
              e.live(lit[i].indexOf(k) !== -1);
            });
            cap.textContent = caps[i];
          }
        });
      }

      /*
       * Clips an edge to rectangle boundaries. Opposite directions get
       * different ports, rather than sharing a line under the nodes.
       */
      function route(a, b, reversePair) {
        var ax = a.x + a.w / 2;
        var ay = a.y + a.h / 2;
        var bx = b.x + b.w / 2;
        var by = b.y + b.h / 2;
        var dx = bx - ax;
        var dy = by - ay;
        var len = Math.hypot(dx, dy) || 1;
        var offset = reversePair ? 9 : 0;
        var ox = -dy / len * offset;
        var oy = dx / len * offset;

        function port(n, cx, cy, vx, vy) {
          var tx = vx ? (n.w / 2 - Math.abs(ox)) / Math.abs(vx) : Infinity;
          var ty = vy ? (n.h / 2 - Math.abs(oy)) / Math.abs(vy) : Infinity;
          var t = Math.min(tx, ty);
          return [cx + ox + vx * t, cy + oy + vy * t];
        }
        return [
          port(a, ax, ay, dx, dy),
          port(b, bx, by, -dx, -dy)
        ];
      }

      function orchestration(holder, D, label) {
        var svg = svgIn(holder, D.vb, label);
        var edges = {};
        D.hops.forEach(function (h) {
          if (h.a === h.b) return;
          var key = h.a + ":" + h.b;
          if (edges[key]) return;
          var paired = D.hops.some(function (q) {
            return q.a === h.b && q.b === h.a;
          });
          var points;
          if (h.a === "jd" && h.b === "wk") {
            points = [[380,134],[380,205],[125,205],[125,134]];
          } else {
            points = route(D.nodes[h.a], D.nodes[h.b], paired);
          }
          edges[key] = edge(svg, points);
        });

        /* The tool record feeds the mechanical gate, not the model's prose. */
        if (D.nodes.log && D.nodes.gate) {
          edges.record = edge(svg, [[330,187],[365,187],[365,104]], {
            x: 399, y: 160, text: T("record", "記録")
          });
        }
        var G = nodeSet(svg, D.nodes);
        var cap = el("p", "cap");
        holder.appendChild(cap);
        holder.appendChild(el("div", D.warn ? "warnbox" : "hint", D.note));
        return Anim(holder, {
          n: D.hops.length,
          dur: 1900,
          rest: {i: D.hops.length - 1, t: 1},
          draw: function (i) {
            var h = D.hops[i];
            activeNodes(G, h.a, h.k === "fail");
            if (h.a !== h.b && h.k !== "fail") G[h.b].classList.add("on");
            Object.keys(edges).forEach(function (k) {
              edges[k].live(k === h.a + ":" + h.b ||
                (k === "record" &&
                  (h.a === "gate" || (h.a === "dev" && h.b === "gate"))));
            });
            cap.textContent = T("Step ", "手順 ") +
              (i + 1) + "/" + D.hops.length + ". " + h.t;
          }
        });
      }

      function FlowMap(host, cfg) {
        var fig = el("div", "fig");
        host.appendChild(fig);
        var svg = svgIn(fig, cfg.vb, cfg.label, true);
        var N = cfg.nodes;
        var labels = N.ag ? {
          "ag:tools": {x: 361, y: 101, anchor: "end",
            text: T("tool call", "ツール呼出")},
          "tools:ag": {x: 442, y: 101, anchor: "start",
            text: T("result", "結果")},
          "ag:bus": {x: 512, y: 91, anchor: "start",
            text: T("event", "イベント")},
          "bus:hub": {x: 515, y: 222, anchor: "start",
            text: T("event", "イベント")},
          "hub:ui": {x: 320, y: 239, text: T("SSE", "SSE")},
          "ui:hub": {x: 320, y: 305, text: T("debug", "デバッグ")}
        } : {
          "acp:rk": {x: 182, y: 64, text: T("ACP", "ACP")},
          "rk:acp": {x: 181, y: 108, text: T("stream", "ストリーム")},
          "mcpc:rk": {x: 190, y: 211, text: T("MCP", "MCP")},
          "rk:mcpc": {x: 232, y: 232, text: T("result", "結果")},
          "rk:mcps": {x: 460, y: 65, text: T("MCP", "MCP")},
          "mcps:rk": {x: 459, y: 108, text: T("result", "結果")},
          "rk:peer": {x: 450, y: 176, text: T("A2A", "A2A")},
          "peer:rk": {x: 448, y: 245, text: T("poll/result", "ポーリング/結果")}
        };
        var E = cfg.edges.map(function (e) {
          return {
            data: e,
            view: edge(svg, e.pts, labels[e.from + ":" + e.to])
          };
        });
        var G = nodeSet(svg, N);
        var selected = keep[cfg.key] && N[keep[cfg.key]] ? keep[cfg.key] : cfg.first;
        var litIdx = -1;
        var anim = null;
        var info = el("div", "info");
        var cap = el("p", "cap");

        function firstFrom(id) {
          for (var k = 0; k < E.length; k++) {
            if (E[k].data.from === id) return k;
          }
          return -1;
        }

        /*
         * Play and Step walk the edges one by one; the node the edge leaves
         * is selected and its info panel shown, so the caption, the lit
         * edge and the panel always describe the same hop.
         */
        function paint() {
          activeNodes(G, selected);
          E.forEach(function (e, k) { e.view.live(k === litIdx); });
          var e = E[litIdx];
          cap.textContent = e ? T("Flow ", "流れ ") + (litIdx + 1) + "/" +
            E.length + ". " + N[e.data.from].label + " \u2192 " +
            N[e.data.to].label : "";
        }

        function select(id, fromTour) {
          selected = id;
          keep[cfg.key] = id;
          if (!fromTour) {
            litIdx = firstFrom(id);
            if (anim && litIdx >= 0) {
              anim.i = litIdx;
              anim.t = 0;
              anim.last = 0;
              anim.stepped = false;
              previous = litIdx;
            }
          }
          paint();
          info.textContent = "";
          var d = cfg.info[id];
          info.appendChild(el("h4", null,
            N[id].label + (N[id].sub ? "  /  " + N[id].sub : "")));
          info.appendChild(el("p", null, L(d.what)));
          if (d.keys && d.keys.length) {
            var keys = el("p", null, T("Config and flags: ", "設定とフラグ: "));
            d.keys.forEach(function (key) {
              keys.appendChild(codeCell(key));
              keys.appendChild(document.createTextNode(" "));
            });
            info.appendChild(keys);
          }
          if (d.limit) {
            info.appendChild(el("p", null, T("Limit: ", "制限: ") + L(d.limit)));
          }
          var source = el("p", null, T("Source: ", "ソース: "));
          source.appendChild(codeCell(d.src));
          info.appendChild(source);
        }

        /* Original EN/JA hint, including its timing disclaimer. */
        if (cfg.hint) fig.appendChild(el("div", "hint", L(cfg.hint)));
        fig.appendChild(cap);
        fig.appendChild(info);

        Object.keys(G).forEach(function (id) {
          var g = G[id];
          g.setAttribute("role", "button");
          g.setAttribute("tabindex", "0");
          g.setAttribute("aria-label",
            N[id].label + (N[id].sub ? " - " + N[id].sub : ""));
          g.addEventListener("click", function () { select(id); if (cfg.onUser) cfg.onUser(id); });
          g.addEventListener("keydown", function (e) {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              select(id);
              if (cfg.onUser) cfg.onUser(id);
            }
          });
        });
        var previous = -1;
        select(selected);
        previous = litIdx;

        anim = Anim(fig, {
          n: Math.max(1, cfg.edges.length),
          dur: 1800,
          start: Math.max(0, litIdx),
          rest: {i: Math.max(0, litIdx), t: 1},
          draw: function (i) {
            if (i !== previous) {
              previous = i;
              litIdx = i;
              if (E[i] && E[i].data.from !== selected) select(E[i].data.from, true);
              else paint();
            }
          }
        });
        fig.insertBefore(fig.querySelector(".ctrls"), fig.children[1]);
        anim.select = select;
        return anim;
      }

      function wake(fig, OUT, LB, DS, label) {
        /*
         * Four columns by three rows: the phone does not shrink twelve
         * adjacent tick labels into unreadable, overlapping text.
         */
        var svg = svgIn(fig, "0 0 640 390", label);
        S("text", {x: 22, y: 24, "class": "sub"}, svg,
          T("model (L2 agent turns)", "モデル (L2 エージェントターン)"));
        S("text", {x: 22, y: 376, "class": "sub"}, svg,
          T("L0 free ticks (checks, no model)",
            "L0 無料ティック (チェックのみ、モデルなし)"));

        function pos(k) {
          var row = Math.floor(k / 4);
          var col = k % 4;
          if (row % 2) col = 3 - col;
          return [80 + col * 160, 105 + row * 112];
        }

        var ticks = [];
        var segments = [];
        var escalations = {};
        var models = {};
        OUT.forEach(function (o, k) {
          var p = pos(k);
          if (k < OUT.length - 1) {
            var q = pos(k + 1);
            segments.push(edge(svg,
              p[1] === q[1]
                ? [[p[0] + (q[0] > p[0] ? 9 : -9), p[1]],
                   [q[0] + (q[0] > p[0] ? -9 : 9), q[1]]]
                : [[p[0], p[1] + 9], [q[0], q[1] - 9]]));
          }
          var g = S("g", {}, svg._nodes);
          S("circle", {
            cx: p[0], cy: p[1], r: 14, "class": "rk-ring"
          }, g);
          var tick = S("circle", {
            cx: p[0], cy: p[1], r: 7, "class": "rk-tick"
          }, g);
          S("text", {
            x: p[0], y: p[1] + 26,
            "text-anchor": "middle", "class": "sub"
          }, g, LB[o]);
          ticks.push({g: g, tick: tick});
          if (o === "chg" || o === "alarm") {
            models[k] = node(svg, {
              x: p[0] - 25, y: p[1] - 65,
              w: 50, h: 30, label: "LLM"
            });
            escalations[k] = edge(svg,
              [[p[0],p[1] - 9],[p[0],p[1] - 35]], {
                x: p[0] + 19, y: p[1] - 19,
                anchor: "start", text: T("event", "イベント")
              });
          }
        });

        var counters = el("div", "cnt");
        var c1 = el("span");
        var c2 = el("span");
        var c3 = el("span");
        counters.appendChild(c1);
        counters.appendChild(c2);
        counters.appendChild(c3);
        fig.appendChild(counters);
        var cap = el("p", "cap");
        fig.appendChild(cap);

        function count(host, prefix, value, suffix) {
          host.textContent = prefix;
          host.appendChild(el("b", null, String(value)));
          if (suffix) host.appendChild(document.createTextNode(suffix));
        }

        return Anim(fig, {
          n: OUT.length,
          dur: 900,
          rest: {i: OUT.length - 1, t: 1},
          draw: function (i, t) {
            var completed = t >= .8;
            var calls = 0;
            var total = 0;
            ticks.forEach(function (item, k) {
              var done = k < i || (k === i && completed);
              var escalation = OUT[k] === "chg" || OUT[k] === "alarm";
              item.g.classList.toggle("on", k === i);
              item.tick.setAttribute("class", "rk-tick" +
                (done ? OUT[k] === "sup" ? " suppressed" :
                  escalation ? " escalated" : " done" : ""));
              if (done) {
                total++;
                if (escalation) calls++;
              }
            });
            segments.forEach(function (e, k) {
              e.live(k === i && OUT[i] !== "chg" && OUT[i] !== "alarm");
            });
            Object.keys(escalations).forEach(function (k) {
              var live = +k === i;
              escalations[k].live(live);
              models[k].classList.toggle("on", live);
              models[k].classList.toggle("rk-hidden",
                +k > i || (+k === i && !completed && !live));
            });
            count(c1, T("ticks ", "ティック "), total);
            count(c2, T("model calls ", "モデル呼び出し "), calls);
            count(c3, T("quiet ticks cost ", "静かなティックのコスト "),
              0, T(" calls", " 回"));
            cap.textContent = DS[OUT[i]];
          }
        });
      }

      return {
        Anim: Anim,
        FlowMap: FlowMap,
        hero: hero,
        orchestration: orchestration,
        wake: wake
      };
    }
  };
})();
