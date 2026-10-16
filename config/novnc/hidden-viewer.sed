# Applied to noVNC's core/rfb.js by the Dockerfile (third patch, see its comment there).
# While the viewer can't be seen, it neither resizes the desktop nor takes screen updates.
#   1. Track whether the viewer is in view: an IntersectionObserver on the viewer's own
#      <html> (a frame shrunk to nothing or moved off screen). Not trackVisibility: inside
#      code-server's webviews its isVisible read false all the time, which blocked every
#      resize.
#   2-3. Observe and stop observing along with noVNC's own ResizeObserver.
#   4. Skip the request while out of view, while the page itself is hidden, or while the
#      viewer's own viewport is exactly 300x150 - the default size of an iframe, which is
#      what a viewer in a code-server editor tab measured once another tab was active.
#      Nobody runs a desktop that size on purpose, and the request it made left the
#      desktop there until someone looked again.
#   5. Send a resize-driven request 200 ms late: the ResizeObserver reports a frame
#      shrinking before the IntersectionObserver reports it out of view.
#   6. What the embedding page says: router's VNC tab posts {source:
#      "code-docker-router-vnc", type: "viewer-shown", shown} to each viewer frame (only
#      it knows a viewer is an inactive tab, or that its own page sits in a hidden VS Code
#      view - both hide with visibility:hidden, which nothing in here can observe). Kept
#      at module level, so a viewer that reconnects while hidden starts paused.
#   7-8. Pause on any of the three signals: out of view, page hidden, embedder says hidden.
#      In request mode the next FramebufferUpdateRequest is held back; with continuous
#      updates (a server that offers them) they are turned off, and not turned on for the
#      first time while paused. noVNC otherwise asks again after every update, so a
#      hidden viewer of a desktop with any animation on it kept downloading it.
#   9. Resuming asks for the whole screen once, since what changed while paused was
#      never sent.
s#^export default class RFB extends EventTargetMixin {$#// PATCHED (router-docker): whether the page embedding this viewer shows it - see Dockerfile.\nlet viewerShownByEmbedder = true;\nconst viewerShownListeners = new Set();\nwindow.addEventListener("message", (e) => {\n    if (window.parent === window || e.source !== window.parent || e.origin !== window.location.origin) { return; }\n    const d = e.data;\n    if (!d || d.source !== "code-docker-router-vnc" || d.type !== "viewer-shown" || typeof d.shown !== "boolean") { return; }\n    viewerShownByEmbedder = d.shown;\n    viewerShownListeners.forEach((f) => f());\n});\n\n&#
s#^\( *\)this\._resizeObserver = new ResizeObserver(this\._eventHandlers\.handleResize);$#&\n\1// PATCHED (router-docker): whether the viewer can be seen - see Dockerfile.\n\1this._viewerHidden = false;\n\1this._viewObserver = new IntersectionObserver((entries) => {\n\1    const hidden = !entries[entries.length - 1].isIntersecting;\n\1    if (hidden === this._viewerHidden) { return; }\n\1    this._viewerHidden = hidden;\n\1    this._syncUpdates();\n\1    if (!hidden) { this._requestRemoteResize(); }\n\1});\n\1this._eventHandlers.viewerShown = () => { if (document.visibilityState === "visible") { this._requestRemoteResize(); } };\n\1this._updatesPaused = false;\n\1this._skippedUpdate = false;\n\1this._pausedContinuous = false;\n\1this._eventHandlers.syncUpdates = () => this._syncUpdates();#
s#^\( *\)this\._resizeObserver\.observe(this\._screen);$#&\n\1this._viewObserver.observe(document.documentElement);\n\1document.addEventListener("visibilitychange", this._eventHandlers.viewerShown);\n\1document.addEventListener("visibilitychange", this._eventHandlers.syncUpdates);\n\1viewerShownListeners.add(this._eventHandlers.syncUpdates);\n\1this._syncUpdates();#
s#^\( *\)this\._resizeObserver\.disconnect();$#&\n\1this._viewObserver.disconnect();\n\1clearTimeout(this._viewerResizeTimeout);\n\1document.removeEventListener("visibilitychange", this._eventHandlers.viewerShown);\n\1document.removeEventListener("visibilitychange", this._eventHandlers.syncUpdates);\n\1viewerShownListeners.delete(this._eventHandlers.syncUpdates);#
s#^\( *\)if (size\.w < 1 || size\.h < 1) { return; }$#&\n\1// PATCHED (router-docker): nor while the viewer can't be seen - see Dockerfile.\n\1if (this._viewerHidden || document.visibilityState === "hidden") { return; }\n\1if (window.innerWidth === 300 \&\& window.innerHeight === 150) { return; }#
/_handleResize() {/,/^    }$/ s#^\( *\)this\._requestRemoteResize();$#\1// PATCHED (router-docker): a moment later, once the visibility check above has seen this change too - see Dockerfile.\n\1clearTimeout(this._viewerResizeTimeout);\n\1this._viewerResizeTimeout = setTimeout(() => this._requestRemoteResize(), 200);#
s#^\( *\)if (ret \&\& !this\._enabledContinuousUpdates) {$#\1// PATCHED (router-docker): don't ask for the next update while paused - see Dockerfile.\n\1if (ret \&\& !this._enabledContinuousUpdates \&\& this._updatesPaused) {\n\1    this._skippedUpdate = true;\n\1} else if (ret \&\& !this._enabledContinuousUpdates) {#
s#^\( *\)if (!this\._enabledContinuousUpdates) { return; }$#&\n\1// PATCHED (router-docker): nor turn continuous updates on while paused; _syncUpdates does it on resume - see Dockerfile.\n\1if (this._updatesPaused) {\n\1    this._pausedContinuous = true;\n\1    this._enabledContinuousUpdates = false;\n\1    return;\n\1}#
s#^\(    \)_updateContinuousUpdates() {$#\1// PATCHED (router-docker): pause screen updates while the viewer can't be seen, and catch up on resume - see Dockerfile.\n\1_syncUpdates() {\n\1    const paused = this._viewerHidden || document.visibilityState === "hidden" || !viewerShownByEmbedder;\n\1    if (paused === this._updatesPaused) { return; }\n\1    this._updatesPaused = paused;\n\1    if (this._rfbConnectionState !== "connected") { return; }\n\1    if (paused) {\n\1        if (this._enabledContinuousUpdates) {\n\1            this._pausedContinuous = true;\n\1            RFB.messages.enableContinuousUpdates(this._sock, false, 0, 0, this._fbWidth, this._fbHeight);\n\1        }\n\1        return;\n\1    }\n\1    const resumeContinuous = this._pausedContinuous;\n\1    if (resumeContinuous) {\n\1        this._pausedContinuous = false;\n\1        this._enabledContinuousUpdates = true;\n\1        this._updateContinuousUpdates();\n\1    }\n\1    if (this._skippedUpdate || resumeContinuous) {\n\1        this._skippedUpdate = false;\n\1        RFB.messages.fbUpdateRequest(this._sock, false, 0, 0, this._fbWidth, this._fbHeight);\n\1    }\n\1}\n\n&#
