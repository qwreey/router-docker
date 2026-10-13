# Applied to noVNC's core/rfb.js and app/ui.js by the Dockerfile (fourth patch, see its
# comment there). Keeps a viewer an operator disconnected from reconnecting by itself.
#   1-2. rfb.js: pass the WebSocket close code on in the "disconnect" event.
#   3. ui.js: on router-manager's "disconnected by an operator" code (vncCloseKicked in
#      backend/handlers_vnc.go), turn auto-reconnect off the way noVNC's own disconnect
#      button does. A successful manual connect turns it back on (connectFinished).
s#^\( *\)Log\.Debug("WebSocket on-close event");$#&\n\1// PATCHED (router-docker): keep the close code for the disconnect event - see Dockerfile.\n\1this._closeCode = e.code;#
s#^\( *\){ clean: this\._rfbCleanDisconnect } }));$#\1// PATCHED (router-docker): say why the socket closed - see Dockerfile.\n\1{ clean: this._rfbCleanDisconnect, code: this._closeCode } }));#
/disconnectFinished(e) {/,/^    },$/ s#^\( *\)UI\.rfb = undefined;$#&\n\n\1// PATCHED (router-docker): an operator disconnected this viewer, so don't reconnect by itself - see Dockerfile.\n\1if (e.detail.code === 4001) { UI.inhibitReconnect = true; }#
