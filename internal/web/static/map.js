// m17-web dashboard: the map of stations' last positions. Leaflet draws
// it; the tiles come from OpenStreetMap. The positions are refetched each
// time the live part of the page is (see dashboard.js).
(function () {
	"use strict";
	var el = document.getElementById("map");
	if (!el || !window.L || !el.dataset.tiles) return;
	el.hidden = false;

	// The status page's small map doesn't take the page's scrolling
	var map = L.map(el, { worldCopyJump: true, scrollWheelZoom: !el.dataset.mini }).setView([20, 0], 2);
	L.tileLayer(el.dataset.tiles, {
		maxZoom: 18,
		// The page sends no Referer; OpenStreetMap's tile servers want one
		referrerPolicy: "strict-origin",
		attribution: '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors'
	}).addTo(map);

	var markers = {}, fitted = false;
	var types = { 0: "fixed", 1: "mobile", 2: "handheld" };

	// The popup, built from text: callsigns and all come off the air
	function popup(p) {
		var div = document.createElement("div");
		var b = document.createElement("strong");
		b.textContent = p.callsign;
		div.appendChild(b);
		function line(text) {
			div.appendChild(document.createElement("br"));
			div.appendChild(document.createTextNode(text));
		}
		if (types[p.station_type]) line(types[p.station_type]);
		line(new Date(p.time).toLocaleString());
		if (p.altitude != null) line(Math.round(p.altitude) + " m (" + Math.round(p.altitude * 3.28084) + " ft)");
		if (p.speed != null) {
			line(p.speed === 0 ? "stopped" :
				Math.round(p.speed) + " km/h (" + Math.round(p.speed * 0.621371) + " mph)" +
				(p.bearing != null ? " heading " + p.bearing + "°" : ""));
		}
		line(p.source === "rf" ? "heard on the radio" : "from the reflector");
		return div;
	}

	function update() {
		fetch(el.dataset.positions, { cache: "no-store" })
			.then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
			.then(function (list) {
				var seen = {};
				list.forEach(function (p) {
					if (!isFinite(p.latitude) || !isFinite(p.longitude)) return;
					var key = p.callsign.toUpperCase();
					seen[key] = true;
					var ll = [p.latitude, p.longitude];
					if (markers[key]) {
						markers[key].setLatLng(ll).setPopupContent(popup(p));
					} else {
						markers[key] = L.marker(ll, { title: p.callsign }).bindPopup(popup(p)).addTo(map);
					}
				});
				Object.keys(markers).forEach(function (key) {
					if (!seen[key]) {
						map.removeLayer(markers[key]);
						delete markers[key];
					}
				});
				// Show them all, the first time there are any
				var keys = Object.keys(markers);
				if (!fitted && keys.length) {
					fitted = true;
					map.invalidateSize({ pan: false });
					map.fitBounds(L.latLngBounds(keys.map(function (k) { return markers[k].getLatLng(); })),
						{ maxZoom: 10, padding: [30, 30], animate: false });
				}
			})
			.catch(function () { /* the next update tries again */ });
	}

	// The box can change size after the page lays out (and with the window)
	if (window.ResizeObserver) new ResizeObserver(function () { map.invalidateSize({ pan: false }); }).observe(el);
	// Fit once the page has its final layout
	window.addEventListener("load", update);
	document.addEventListener("dashboard:update", update);
})();
