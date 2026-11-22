// SessionLights focus helper.
//
// GNOME on Wayland lets no ordinary app raise another app's window, so the
// SessionLights bar cannot bring a session's terminal forward when its name is
// clicked. The shell can. This extension owns one D-Bus name and answers one
// call: raise the window that belongs to a process, or to its nearest
// ancestor that has a window (a claude process sits several levels below its
// terminal).
//
//   gdbus call --session --dest io.github.carltk.SessionLights.Shell \
//     --object-path /io/github/carltk/SessionLights/Shell \
//     --method io.github.carltk.SessionLights.Shell.ActivateProcess <pid>

import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Meta from 'gi://Meta';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';

const BUS_NAME = 'io.github.carltk.SessionLights.Shell';
const OBJECT_PATH = '/io/github/carltk/SessionLights/Shell';
const IFACE_XML = `
<node>
  <interface name="io.github.carltk.SessionLights.Shell">
    <method name="ActivateProcess">
      <arg type="u" direction="in" name="pid"/>
      <arg type="b" direction="out" name="found"/>
    </method>
  </interface>
</node>`;

// How far up the process tree to look for a window owner.
const MAX_DEPTH = 32;

function parentOf(pid) {
    try {
        const [ok, bytes] = GLib.file_get_contents(`/proc/${pid}/stat`);
        if (!ok)
            return 0;
        const raw = new TextDecoder().decode(bytes);
        // The command name is in parentheses and may contain spaces or ')'.
        const rest = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
        return parseInt(rest[1], 10) || 0;
    } catch {
        return 0;
    }
}

class FocusService {
    ActivateProcess(pid) {
        // Most recently used first, so a terminal with several windows gives
        // the one used last unless another asks for attention.
        const windows = global.display.get_tab_list(Meta.TabList.NORMAL_ALL, null);
        for (let p = pid, depth = 0; p > 1 && depth < MAX_DEPTH; p = parentOf(p), depth++) {
            const owned = windows.filter(w => w.get_pid() === p);
            if (owned.length === 0)
                continue;
            // A terminal told to focus one of its windows marks it as wanting
            // attention when Wayland will not let it raise itself: prefer it.
            const win = owned.find(w => w.demands_attention || w.urgent) ?? owned[0];
            // A D-Bus call is not an input event, so get_current_time() is 0
            // here and mutter would refuse the focus change; ask the X/Wayland
            // server for a real timestamp instead.
            Main.activateWindow(win, global.display.get_current_time_roundtrip());
            return true;
        }
        return false;
    }
}

export default class SessionLightsFocus extends Extension {
    enable() {
        this._service = new FocusService();
        this._dbus = Gio.DBusExportedObject.wrapJSObject(IFACE_XML, this._service);
        this._dbus.export(Gio.DBus.session, OBJECT_PATH);
        this._nameId = Gio.bus_own_name(Gio.BusType.SESSION, BUS_NAME,
            Gio.BusNameOwnerFlags.NONE, null, null, null);
    }

    disable() {
        if (this._nameId) {
            Gio.bus_unown_name(this._nameId);
            this._nameId = 0;
        }
        this._dbus?.unexport();
        this._dbus = null;
        this._service = null;
    }
}
