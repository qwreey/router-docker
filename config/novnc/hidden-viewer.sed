# Applied to noVNC's core/rfb.js by the Dockerfile (third patch, see its comment there).
# Keeps a remote-resize viewer from resizing the desktop while it can't be seen.
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
s#^\( *\)this\._resizeObserver = new ResizeObserver(this\._eventHandlers\.handleResize);$#&\n\1// PATCHED (router-docker): whether the viewer can be seen - see Dockerfile.\n\1this._viewerHidden = false;\n\1this._viewObserver = new IntersectionObserver((entries) => {\n\1    const hidden = !entries[entries.length - 1].isIntersecting;\n\1    if (hidden === this._viewerHidden) { return; }\n\1    this._viewerHidden = hidden;\n\1    if (!hidden) { this._requestRemoteResize(); }\n\1});\n\1this._eventHandlers.viewerShown = () => { if (document.visibilityState === "visible") { this._requestRemoteResize(); } };#
s#^\( *\)this\._resizeObserver\.observe(this\._screen);$#&\n\1this._viewObserver.observe(document.documentElement);\n\1document.addEventListener("visibilitychange", this._eventHandlers.viewerShown);#
s#^\( *\)this\._resizeObserver\.disconnect();$#&\n\1this._viewObserver.disconnect();\n\1clearTimeout(this._viewerResizeTimeout);\n\1document.removeEventListener("visibilitychange", this._eventHandlers.viewerShown);#
s#^\( *\)if (size\.w < 1 || size\.h < 1) { return; }$#&\n\1// PATCHED (router-docker): nor while the viewer can't be seen - see Dockerfile.\n\1if (this._viewerHidden || document.visibilityState === "hidden") { return; }\n\1if (window.innerWidth === 300 \&\& window.innerHeight === 150) { return; }#
/_handleResize() {/,/^    }$/ s#^\( *\)this\._requestRemoteResize();$#\1// PATCHED (router-docker): a moment later, once the visibility check above has seen this change too - see Dockerfile.\n\1clearTimeout(this._viewerResizeTimeout);\n\1this._viewerResizeTimeout = setTimeout(() => this._requestRemoteResize(), 200);#
