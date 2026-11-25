// m17-web dashboard: live updates. The server sends an "update" event
// whenever the gateway reports something; the live part of the page is
// then fetched again, already rendered. Without this script the page
// reloads itself every 15 seconds instead.
(function () {
	"use strict";

	// The list's switches apply as soon as they change
	document.addEventListener("change", function (ev) {
		var form = ev.target.form;
		if (form && form.classList.contains("toggles")) form.submit();
	});

	// "33 s ago" and the length of a transmission in progress, kept current
	function ago(t, now) {
		var s = Math.floor((now - t) / 1000);
		if (s < 5) return "now";
		if (s < 60) return s + " s ago";
		if (s < 3600) return Math.floor(s / 60) + " min ago";
		if (s < 86400) return Math.floor(s / 3600) + " h ago";
		return new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" });
	}
	function tick() {
		var now = Date.now();
		document.querySelectorAll("[data-ago]").forEach(function (el) {
			var t = Date.parse(el.getAttribute("datetime"));
			if (!isNaN(t)) el.textContent = ago(t, now);
		});
		document.querySelectorAll("[data-since]").forEach(function (el) {
			var t = Date.parse(el.dataset.since);
			if (isNaN(t)) return;
			var s = Math.max(0, Math.floor((now - t) / 1000));
			el.textContent = Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
		});
	}
	setInterval(tick, 1000);

	var live = document.getElementById("live");
	if (!live || !window.EventSource || !live.dataset.events) return;

	var fetching = false, again = false;
	function refresh() {
		if (fetching) { again = true; return; }
		fetching = true;
		fetch(live.dataset.live, { cache: "no-store" })
			.then(function (r) { return r.ok ? r.text() : Promise.reject(r.status); })
			.then(function (html) {
				live.innerHTML = html;
				tick();
				// For the map, which fetches its positions again
				document.dispatchEvent(new Event("dashboard:update"));
			})
			.catch(function () { /* the next event tries again */ })
			.then(function () {
				fetching = false;
				if (again) { again = false; refresh(); }
			});
	}

	var es = new EventSource(live.dataset.events);
	es.addEventListener("update", refresh);
	// After the stream was down (a restart, the network), catch up
	es.addEventListener("open", refresh);
})();
