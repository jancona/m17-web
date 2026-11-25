// m17-web dashboard: live updates. The server sends an "update" event
// whenever the gateway reports something; the live part of the page is
// then fetched again, already rendered. Without this script the page
// reloads itself every 15 seconds instead.
(function () {
	"use strict";
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
