// Adapter: Google's Picker behind this server's own chooser contract.
//
// Served from THIS origin, so `script-src 'self'` covers it; it is this
// file that then loads Google's api.js, which is why script-src names
// apis.google.com and frame-src names docs.google.com. See
// applySecurityHeaders.
//
// The contract is deliberately narrow, and the reason is testability:
// the page calls one function and gets back file ids. Anything
// implementing it is a chooser, so a test can substitute a stand-in and
// exercise the SAME page code that production runs, rather than a
// second path that only tests itself.
//
//   window.htcondorPicker.open({
//     token,          // OAuth access token for the connector
//     apiKey, appId,  // the provider's, not the credential's
//     onPicked(files) // files: [{id, name}]
//     onCancel()
//     onError(message)
//     onOpened()      // the chooser is on screen and waiting
//   })
(function () {
  "use strict";

  var GAPI = "https://apis.google.com/js/api.js";

  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var existing = document.querySelector('script[data-htcondor-picker="' + src + '"]');
      if (existing) {
        if (existing.dataset.loaded === "1") return resolve();
        existing.addEventListener("load", function () { resolve(); });
        existing.addEventListener("error", function () { reject(new Error("could not load " + src)); });
        return;
      }
      var el = document.createElement("script");
      el.src = src;
      el.async = true;
      el.dataset.htcondorPicker = src;
      el.addEventListener("load", function () { el.dataset.loaded = "1"; resolve(); });
      // A blocked load and a network failure look the same here. The
      // message names the Content-Security-Policy because that is the
      // likely cause and the only other evidence is a console entry
      // the person reporting the problem will not think to look at.
      el.addEventListener("error", function () {
        reject(new Error(
          "could not load " + src + ". If this access point's " +
          "Content-Security-Policy does not name that origin, the browser " +
          "refuses it before any request is made."));
      });
      document.head.appendChild(el);
    });
  }

  function loadPickerModule() {
    return new Promise(function (resolve, reject) {
      if (!window.gapi || !window.gapi.load) {
        return reject(new Error("the provider's script loaded but defined no gapi"));
      }
      window.gapi.load("picker", {
        callback: function () { resolve(); },
        onerror: function () { reject(new Error("the provider's picker module failed to load")); },
      });
    });
  }

  // Google positions its dialog with `position: absolute`, from the
  // WINDOW's scroll offset. A page that scrolls an inner container
  // instead -- as this app's layout does, leaving window.scrollY at 0
  // however far you scroll -- has the dialog placed against a scrolled
  // ancestor, and it lands outside the viewport. Measured on the submit
  // page: top = -910px, above the document origin, where no amount of
  // scrolling reveals it. The chooser is open and working and nobody
  // can see it.
  //
  // Pinning the absolutely-positioned ancestor to the viewport is the
  // narrowest fix: it does not touch Google's markup or class names,
  // only where the thing is drawn.
  function pinToViewport(el) {
    while (el && el !== document.body) {
      if (window.getComputedStyle(el).position === "absolute") {
        el.style.position = "fixed";
        el.style.top = "50%";
        el.style.left = "50%";
        el.style.right = "auto";
        el.style.bottom = "auto";
        el.style.margin = "0";
        el.style.transform = "translate(-50%, -50%)";
        return true;
      }
      el = el.parentElement;
    }
    return false;
  }

  // The dialog's backdrop is positioned the same way and covers the
  // page from wherever it was placed, so it gets the same treatment.
  function pinBackdrop() {
    var bg = document.querySelector(".picker-dialog-bg");
    if (!bg) return;
    bg.style.position = "fixed";
    bg.style.top = "0";
    bg.style.left = "0";
    bg.style.width = "100%";
    bg.style.height = "100%";
  }

  function pickerFrame() {
    var frames = document.querySelectorAll("iframe");
    for (var i = frames.length - 1; i >= 0; i--) {
      if ((frames[i].src || "").indexOf("docs.google.com/picker") !== -1) {
        return frames[i];
      }
    }
    return null;
  }

  // How long to wait for the chooser to put itself on screen before
  // calling it a failure. Google's iframe is cross-origin, so we cannot
  // see whether it rendered -- only whether it exists. That is enough
  // for the failure that matters: the one where nothing appears and the
  // caller is left showing "Opening..." forever.
  var OPEN_TIMEOUT_MS = 15000;

  function whenOpened(onOpened, onError) {
    var waited = 0;
    var step = 250;
    var timer = window.setInterval(function () {
      var frame = pickerFrame();
      if (frame) {
        window.clearInterval(timer);
        pinToViewport(frame);
        pinBackdrop();
        onOpened();
        return;
      }
      waited += step;
      if (waited >= OPEN_TIMEOUT_MS) {
        window.clearInterval(timer);
        onError(
          "the file chooser did not open. The provider accepted the " +
          "request but never showed a chooser, which usually means its " +
          "configuration is incomplete -- most often the page's origin " +
          "is not registered with the OAuth client.");
      }
    }, step);
  }

  window.htcondorPicker = {
    open: function (opts) {
      var onError = opts.onError || function () {};
      loadScript(GAPI)
        .then(loadPickerModule)
        .then(function () {
          var view = new window.google.picker.DocsView(window.google.picker.ViewId.DOCS)
            .setIncludeFolders(true)
            .setSelectFolderEnabled(false);

          var builder = new window.google.picker.PickerBuilder()
            .setOAuthToken(opts.token)
            .addView(view)
            .enableFeature(window.google.picker.Feature.MULTISELECT_ENABLED)
            .setCallback(function (data) {
              var action = data[window.google.picker.Response.ACTION];
              if (action === window.google.picker.Action.PICKED) {
                var docs = data[window.google.picker.Response.DOCUMENTS] || [];
                opts.onPicked(docs.map(function (d) {
                  return { id: d.id, name: d.name };
                }));
              } else if (action === window.google.picker.Action.CANCEL) {
                if (opts.onCancel) opts.onCancel();
              }
            });

          // The developer key is the provider's, not the credential's.
          // Without it the chooser renders and lists nothing, which is
          // why the server refuses to call a chooser configured without
          // one usable.
          if (opts.apiKey) builder = builder.setDeveloperKey(opts.apiKey);
          if (opts.appId) builder = builder.setAppId(opts.appId);

          builder.build().setVisible(true);
          whenOpened(opts.onOpened || function () {}, onError);
        })
        .catch(function (err) {
          onError(String(err && err.message ? err.message : err));
        });
    },
  };
})();
