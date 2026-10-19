# Applied to noVNC's app/ui.js by the Dockerfile (fifth patch, see its comment there).
# Shares the remote desktop's clipboard with the browser's, so copy and paste work
# without noVNC's clipboard panel. Needs the Async Clipboard API: a secure context
# (https, or http://localhost), and in a frame, allow="clipboard-read; clipboard-write"
# on every frame above it. Without it, both halves do nothing and noVNC behaves as before.
#   1. Desktop to browser: whatever the desktop copies is written to the browser's
#      clipboard as well, once the user has clicked or typed into this connection. The
#      server sends what it already holds as soon as one connects, and merely opening a
#      viewer shouldn't replace what the user copied. The browser also refuses the write
#      while the page has no focus; such a copy reaches the panel only.
#   2. Browser to desktop: Ctrl+V (or Cmd+V) on the viewer first reads the browser's
#      clipboard and hands it to the desktop, then sends the V. Only when the browser's
#      clipboard changed since this page last read or wrote it: otherwise a copy made on
#      the desktop since then (one the browser refused to take, see 1) would be
#      overwritten by an older one. Only on that key, never on focus, so nothing leaves
#      the browser's clipboard without a paste asking for it. The first read asks the
#      user for permission.
#      Keys pressed while the clipboard is being read are held and replayed after the V,
#      so a Ctrl released meanwhile doesn't reach the desktop before it.
#   3. Listen ahead of noVNC's own keyboard handler (capture phase on the document),
#      which would otherwise send the V straight away.
s#^\( *\)document\.getElementById('noVNC_clipboard_text')\.value = e\.detail\.text;$#&\n\1// PATCHED (router-docker): and to the browser's clipboard - see Dockerfile.\n\1UI.clipboardToBrowser(e.detail.text);#
/^    clipboardSend() {$/i\
    // PATCHED (router-docker): share the clipboard with the browser's - see Dockerfile.\
    // The browser clipboard's text as this page last read or wrote it.\
    clipboardBrowserText: null,\
    // Key events that arrived while a paste was reading the clipboard, or null.\
    clipboardHeldKeys: null,\
    // The connection the user has clicked or typed into.\
    clipboardUserRfb: null,\
\
    clipboardToBrowser(text) {\
        // Not before the user has used this connection: the server sends what it already\
        // has on connect, and opening the viewer shouldn't replace the browser's clipboard.\
        if (UI.clipboardUserRfb !== UI.rfb) { return; }\
        if (!navigator.clipboard || !navigator.clipboard.writeText) { return; }\
        navigator.clipboard.writeText(text).then(() => {\
            UI.clipboardBrowserText = text;\
        }, (err) => {\
            Log.Debug("Browser clipboard not written: " + err);\
        });\
    },\
\
    clipboardUserInput(e) {\
        if (document.getElementById('noVNC_container').contains(e.target)) { UI.clipboardUserRfb = UI.rfb; }\
    },\
\
    clipboardPasteKey(e) {\
        if (UI.clipboardHeldKeys && document.getElementById('noVNC_container').contains(e.target)) {\
            // A paste is still reading the clipboard: these keys go after its V.\
            e.preventDefault();\
            e.stopImmediatePropagation();\
            UI.clipboardHeldKeys.push(e);\
            return;\
        }\
        if (e.type !== 'keydown' || e.repeat || e.altKey || !(e.ctrlKey || e.metaKey)) { return; }\
        // e.key is what the layout types; code, for an input method that maps the key.\
        if (e.key !== 'v' && e.key !== 'V' && !(e.code === 'KeyV' && !/^[a-z]$/i.test(e.key))) { return; }\
        if (!UI.connected || !UI.rfb || UI.rfb.viewOnly) { return; }\
        if (!navigator.clipboard || !navigator.clipboard.readText) { return; }\
        if (!document.getElementById('noVNC_container').contains(e.target)) { return; }\
\
        e.preventDefault();\
        e.stopImmediatePropagation();\
        const rfb = UI.rfb;\
        const keysym = e.shiftKey ? KeyTable.XK_V : KeyTable.XK_v;\
        // noVNC releases every key when the page loses focus (a permission prompt can\
        // take it), so the V would arrive without its modifiers. Press them again then.\
        const mods = [];\
        if (e.ctrlKey) { mods.push([KeyTable.XK_Control_L, 'ControlLeft']); }\
        if (e.metaKey) { mods.push([KeyTable.XK_Super_L, 'MetaLeft']); }\
        if (e.shiftKey) { mods.push([KeyTable.XK_Shift_L, 'ShiftLeft']); }\
        let blurred = false;\
        const onBlur = () => { blurred = true; };\
        window.addEventListener('blur', onBlur);\
        UI.clipboardHeldKeys = [];\
        navigator.clipboard.readText().then((text) => {\
            if (text === UI.clipboardBrowserText) { return; }\
            UI.clipboardBrowserText = text;\
            if (text === '') { return; }\
            document.getElementById('noVNC_clipboard_text').value = text;\
            rfb.clipboardPasteFrom(text);\
            // Give the desktop a moment to take it: a V right behind it reached the app\
            // first and pasted the old text (20 ms was enough against wayvnc, for both a\
            // Wayland app and Wine under Xwayland; the rest is for the network bunching\
            // the two messages together).\
            return new Promise((resolve) => { setTimeout(resolve, 300); });\
        }, (err) => {\
            Log.Debug("Browser clipboard not read: " + err);\
        }).finally(() => {\
            window.removeEventListener('blur', onBlur);\
            if (blurred) { mods.forEach(([sym, code]) => rfb.sendKey(sym, code, true)); }\
            rfb.sendKey(keysym, 'KeyV');\
            if (blurred) { mods.reverse().forEach(([sym, code]) => rfb.sendKey(sym, code, false)); }\
            const held = UI.clipboardHeldKeys;\
            UI.clipboardHeldKeys = null;\
            held.forEach((h) => h.target.dispatchEvent(new KeyboardEvent(h.type, h)));\
        });\
    },\

s#^\( *\)\.addEventListener('change', UI\.clipboardSend);$#&\n\1// PATCHED (router-docker): ahead of noVNC's own keyboard handler - see Dockerfile.\n\1document.addEventListener('keydown', UI.clipboardUserInput, true);\n\1document.addEventListener('pointerdown', UI.clipboardUserInput, true);\n\1document.addEventListener('keydown', UI.clipboardPasteKey, true);\n\1document.addEventListener('keyup', UI.clipboardPasteKey, true);#
