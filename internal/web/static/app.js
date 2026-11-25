// m17-web: small improvements on top of plain forms. Every page works
// without this file.
(function () {
	"use strict";
	var root = document.documentElement;
	root.classList.add("js");

	// Busy: some pages take a few seconds (asking the HAT stops the
	// gateway, applying settings restarts it), so show that something is
	// happening from the moment a form is sent or a page link followed,
	// and don't let a second press send it twice.
	function busy() { root.classList.add("busy"); }
	document.addEventListener("submit", function (e) {
		if (e.defaultPrevented) return;
		busy();
		var form = e.target;
		var pressed = e.submitter || form.querySelector("button[type=submit]");
		if (pressed) {
			pressed.classList.add("working");
			pressed.setAttribute("aria-busy", "true");
		}
		// After this event, so the pressed button's name and value are sent
		setTimeout(function () {
			form.querySelectorAll("button").forEach(function (b) { b.disabled = true; });
		}, 0);
	});
	document.addEventListener("click", function (e) {
		var a = e.target.closest && e.target.closest("a[href]");
		if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
		if (a.target || a.origin !== location.origin) return;
		if (a.pathname === location.pathname && a.search === location.search && a.hash) return; // same page
		busy();
	});
	// Back to a page kept by the browser: it's no longer busy
	window.addEventListener("pageshow", function () {
		root.classList.remove("busy");
		document.querySelectorAll("button.working").forEach(function (b) {
			b.classList.remove("working");
			b.removeAttribute("aria-busy");
		});
		document.querySelectorAll("form button[disabled]").forEach(function (b) { b.disabled = false; });
	});

	function onReady(fn) {
		if (document.readyState !== "loading") fn();
		else document.addEventListener("DOMContentLoaded", fn);
	}

	onReady(function () {
		// Network: hide the WiFi fields when Ethernet is chosen
		var wifi = document.querySelector(".wifi-only");
		if (wifi) {
			var conn = document.querySelectorAll("input[name=connection]");
			var update = function () {
				var eth = document.querySelector("input[name=connection][value=ethernet]");
				wifi.classList.toggle("js-hidden", !!(eth && eth.checked));
			};
			conn.forEach(function (r) { r.addEventListener("change", update); });
			update();

			// The name box is for "Other network" only
			var otherBox = document.querySelector(".other-ssid");
			var otherRadio = document.querySelector("input[name=wifi_ssid][value='-other-']");
			if (otherBox && otherRadio) {
				var updateOther = function () {
					otherBox.classList.toggle("js-hidden", !otherRadio.checked);
					if (otherRadio.checked) {
						var input = otherBox.querySelector("input");
						if (input && document.activeElement === otherRadio) input.focus();
					}
				};
				document.querySelectorAll("input[name=wifi_ssid]").forEach(function (r) {
					r.addEventListener("change", updateOther);
				});
				updateOther();
			}
		}

		// Station: suggest the browser's time zone if none is chosen yet
		var tz = document.querySelector("select[data-guess-timezone]");
		if (tz && (tz.value === "UTC" || tz.value === "")) {
			try {
				var guess = Intl.DateTimeFormat().resolvedOptions().timeZone;
				if (guess && tz.querySelector("option[value='" + guess.replace(/'/g, "") + "']")) tz.value = guess;
			} catch (e) { /* older browsers: keep the default */ }
		}

		// Station: show the hostname as it's typed
		document.querySelectorAll("[data-mirror]").forEach(function (span) {
			var input = document.getElementById(span.getAttribute("data-mirror"));
			if (input) input.addEventListener("input", function () { span.textContent = input.value.toLowerCase() || "…"; });
		});

		// Radio: show the transmit frequency only for duplex
		var duplex = document.getElementById("duplex");
		var txBlock = document.querySelector(".duplex-only");
		if (duplex && txBlock) {
			var updateDuplex = function () {
				txBlock.classList.toggle("js-hidden", !duplex.checked);
				root.classList.toggle("duplex", duplex.checked);
			};
			duplex.addEventListener("change", updateDuplex);
			updateDuplex();
		}

		// Passwords typed twice: say whether they match as they're typed.
		// A "don't match" message from the last submit goes away once
		// either field changes, so it isn't left on screen for values that
		// do match.
		document.querySelectorAll("input[data-confirms]").forEach(function (confirm) {
			var first = document.getElementById(confirm.getAttribute("data-confirms"));
			if (!first) return;
			var status = document.createElement("p");
			status.className = "hint";
			status.setAttribute("aria-live", "polite");
			var err = document.getElementById(confirm.id + "-err");
			if (err) err.insertAdjacentElement("afterend", status);
			else confirm.insertAdjacentElement("afterend", status);
			var update = function () {
				if (err) {
					err.hidden = true;
					confirm.removeAttribute("aria-invalid");
				}
				if (confirm.value === "") status.textContent = "";
				else if (confirm.value === first.value) status.textContent = "✓ The passwords match.";
				else if (confirm.value.length >= first.value.length) status.textContent = "The passwords don't match.";
				else status.textContent = "";
			};
			first.addEventListener("input", update);
			confirm.addEventListener("input", update);
		});

		// Reflector: filter the list as you type. The chosen reflector is
		// never hidden, so what gets submitted is always visible.
		document.querySelectorAll("input[data-filter]").forEach(function (input) {
			var list = document.getElementById(input.getAttribute("data-filter"));
			var none = document.getElementById(input.getAttribute("data-filter") + "-none");
			if (!list) return;
			input.addEventListener("input", function () {
				var q = input.value.trim().toUpperCase();
				var shown = 0;
				list.querySelectorAll("li[data-name]").forEach(function (li) {
					var radio = li.querySelector("input");
					var match = q === "" || li.getAttribute("data-name").toUpperCase().indexOf(q) >= 0;
					li.hidden = !match && !(radio && radio.checked);
					if (match) shown++;
				});
				if (none) none.hidden = shown > 0;
			});
		});
	});
})();
