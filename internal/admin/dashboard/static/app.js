// Hoot-Lb dashboard frontend. Vanilla JS, no build step, no external
// dependencies — everything this page needs ships embedded in the
// binary and runs fully offline.
(function () {
  "use strict";

  var TOKEN_KEY = "hoot_lb_admin_token";
  // How long an optimistic drain/undrain override is trusted before
  // the next WebSocket push is allowed to override it back, in case
  // the REST call failed silently for a reason the click handler
  // didn't catch (e.g. the request reached the server but the
  // backend address had already been removed).
  var OPTIMISTIC_GRACE_MS = 6000;
  var RECONNECT_DELAY_MS = 2000;

  var statusEl = document.getElementById("status");
  var gateEl = document.getElementById("token-gate");
  var appEl = document.getElementById("app");
  var formEl = document.getElementById("token-form");
  var inputEl = document.getElementById("token-input");
  var errorEl = document.getElementById("token-error");
  var bodyEl = document.getElementById("pools-body");
  var emptyEl = document.getElementById("empty");
  var updatedEl = document.getElementById("updated");

  // pending maps "pool\x00address" -> {draining, expiresAt}, tracking
  // optimistic drain/undrain clicks until the live feed confirms (or
  // the grace period expires and the server's view wins instead).
  var pending = {};
  var ws = null;
  var reconnectTimer = null;

  function getToken() {
    return sessionStorage.getItem(TOKEN_KEY) || "";
  }

  function setToken(t) {
    sessionStorage.setItem(TOKEN_KEY, t);
  }

  function setStatus(text, cls) {
    statusEl.textContent = text;
    statusEl.className = "status status-" + cls;
  }

  function showGate(showError) {
    gateEl.classList.remove("hidden");
    appEl.classList.add("hidden");
    errorEl.classList.toggle("hidden", !showError);
  }

  function showApp() {
    gateEl.classList.add("hidden");
    appEl.classList.remove("hidden");
  }

  function wsURL(token) {
    var proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    return proto + "//" + window.location.host + "/admin/ws?token=" + encodeURIComponent(token);
  }

  function connect() {
    var token = getToken();
    if (!token) {
      showGate(false);
      return;
    }
    showApp();
    setStatus("connecting", "connecting");

    ws = new WebSocket(wsURL(token));

    ws.onopen = function () {
      setStatus("connected", "connected");
    };

    ws.onmessage = function (evt) {
      try {
        render(JSON.parse(evt.data));
      } catch (e) {
        // Malformed snapshot — ignore this tick, keep the connection.
      }
    };

    ws.onclose = function (evt) {
      setStatus("disconnected", "disconnected");
      // A close arriving immediately after connecting, with no
      // message ever received, most likely means the token was
      // rejected before the upgrade completed. Prompt for a new one
      // instead of looping reconnect attempts forever.
      if (!evt.wasClean || evt.code === 1006) {
        scheduleReconnect();
      }
    };

    ws.onerror = function () {
      // onclose fires right after; let it decide whether to retry.
    };
  }

  function scheduleReconnect() {
    if (reconnectTimer) return;
    reconnectTimer = window.setTimeout(function () {
      reconnectTimer = null;
      if (getToken()) connect();
    }, RECONNECT_DELAY_MS);
  }

  function pendingKey(pool, address) {
    return pool + "\u0000" + address;
  }

  function effectiveDraining(pool, backend) {
    var key = pendingKey(pool, backend.address);
    var p = pending[key];
    if (!p) return backend.draining;
    if (Date.now() > p.expiresAt) {
      delete pending[key];
      return backend.draining;
    }
    if (p.draining === backend.draining) {
      // The server has caught up with the optimistic change.
      delete pending[key];
    }
    return p.draining === backend.draining ? backend.draining : p.draining;
  }

  function render(snapshot) {
    var pools = snapshot.pools || [];
    bodyEl.innerHTML = "";
    emptyEl.classList.toggle("hidden", pools.length > 0);

    pools.forEach(function (pool) {
      (pool.backends || []).forEach(function (backend) {
        bodyEl.appendChild(renderRow(pool, backend));
      });
    });

    if (snapshot.generated_at) {
      var d = new Date(snapshot.generated_at);
      updatedEl.textContent = "last updated " + d.toLocaleTimeString();
    }
  }

  function renderRow(pool, backend) {
    var draining = effectiveDraining(pool.name, backend);
    var tr = document.createElement("tr");

    tr.appendChild(cell(pool.name));
    tr.appendChild(cell(pool.algorithm));
    tr.appendChild(cell(backend.address));
    tr.appendChild(cell(String(backend.weight)));

    var stateCell = document.createElement("td");
    stateCell.appendChild(stateBadge(backend.healthy, draining));
    tr.appendChild(stateCell);

    tr.appendChild(cell(String(backend.active_connections || 0)));

    var actionCell = document.createElement("td");
    actionCell.appendChild(drainButton(pool.name, backend.address, draining));
    tr.appendChild(actionCell);

    return tr;
  }

  function cell(text) {
    var td = document.createElement("td");
    td.textContent = text;
    return td;
  }

  function stateBadge(healthy, draining) {
    var span = document.createElement("span");
    if (draining) {
      span.className = "badge badge-draining";
      span.textContent = "draining";
    } else if (healthy) {
      span.className = "badge badge-healthy";
      span.textContent = "healthy";
    } else {
      span.className = "badge badge-unhealthy";
      span.textContent = "unhealthy";
    }
    return span;
  }

  function drainButton(pool, address, draining) {
    var btn = document.createElement("button");
    btn.className = draining ? "undrain-btn" : "drain-btn";
    btn.textContent = draining ? "Undrain" : "Drain";
    btn.addEventListener("click", function () {
      setDraining(pool, address, !draining, btn);
    });
    return btn;
  }

  function setDraining(pool, address, draining, btn) {
    btn.disabled = true;

    // Optimistically reflect the new state immediately, without
    // waiting for the next WebSocket push.
    pending[pendingKey(pool, address)] = {
      draining: draining,
      expiresAt: Date.now() + OPTIMISTIC_GRACE_MS,
    };
    btn.className = draining ? "undrain-btn" : "drain-btn";
    btn.textContent = draining ? "Undrain" : "Drain";

    var path = "/admin/pools/" + encodeURIComponent(pool) +
      "/backends/" + encodeURIComponent(address) +
      "/" + (draining ? "drain" : "undrain");

    fetch(path, {
      method: "POST",
      headers: { Authorization: "Bearer " + getToken() },
    })
      .then(function (resp) {
        if (!resp.ok) {
          // The call didn't take — drop the optimistic override so
          // the next push's server-reported state wins right away.
          delete pending[pendingKey(pool, address)];
        }
      })
      .catch(function () {
        delete pending[pendingKey(pool, address)];
      })
      .finally(function () {
        btn.disabled = false;
      });
  }

  formEl.addEventListener("submit", function (evt) {
    evt.preventDefault();
    var token = inputEl.value.trim();
    if (!token) return;
    setToken(token);
    inputEl.value = "";
    connect();
  });

  connect();
})();
