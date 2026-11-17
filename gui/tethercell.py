#!/usr/bin/env python3
"""TetherCell — desktop console for tethercell.

Wraps the installed `tethercell` supervisor: every host flag is exposed as a control,
live link state/traffic/latency in one window, and the exact command line is shown
before it runs. Nothing here needs root; tethercell carries cap_net_admin itself.

Config: ~/.config/tethercell/config.json
"""

import glob
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
import traceback
from datetime import timedelta

from PyQt5.QtCore import QPointF, QProcess, Qt, QTimer
from PyQt5.QtGui import (QColor, QFont, QIcon, QPainter, QPen, QPixmap,
                         QPolygonF)
from PyQt5.QtWidgets import (QAction, QActionGroup, QApplication, QCheckBox, QComboBox,
                             QFormLayout, QFrame, QGroupBox, QHBoxLayout,
                             QLabel, QLineEdit, QMainWindow, QMessageBox,
                             QPlainTextEdit, QPushButton, QScrollArea,
                             QSplitter, QSpinBox, QVBoxLayout, QWidget)

APP = "TetherCell"
TAGLINE = "USB tether console · max-speed profile"
INSTALLED = "/usr/local/bin/tethercell"
REPO_DIST = os.path.expanduser("~/work/tethercell/dist/tethercell")
CONFIG_DIR = os.path.expanduser("~/.config/tethercell")
CONFIG_PATH = os.path.join(CONFIG_DIR, "config.json")

DEFAULTS = {
    "iface": "tcell0",
    "serial": "",
    "hostroute": "",
    "mtu": 16000,
    "urbs": 8,
    "wait": 20,
    "dead_after": 10,
    "wedge_after": 6,
    "probe_after": 3,
    "probe_every": 2,
    "ping_interval": 5,
    "route": True,
    "v6": True,
    "drop": True,
    "clear_halt": True,
    "reuse": False,
    "preset": "Max speed",
    "theme": "Midnight",
}

PRESETS = {
    "Max speed": dict(mtu=16000, urbs=8),
    "Max speed + deep async": dict(mtu=16000, urbs=16),
    "Old packet sizes (pre-v3)": dict(mtu=1400, urbs=8),
    "Debug (sync reads, slow probes)": dict(mtu=1400, urbs=0, probe_after=8, probe_every=5),
}

# -- helpers -----------------------------------------------------------------

def run(cmd, timeout=4):
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return p.returncode, (p.stdout or "") + (p.stderr or "")
    except Exception as e:  # noqa: BLE001 - GUI must never die on a probe
        return 1, str(e)


def human_bytes(n):
    for unit in ("B", "kB", "MB", "GB", "TB"):
        if abs(n) < 1000 or unit == "TB":
            return f"{n:.0f} {unit}" if unit == "B" else f"{n:.1f} {unit}"
        n /= 1000.0


def human_rate(bps):
    return human_bytes(bps) + "/s"


def access_usb_mode():
    """('accessory'|'normal'|'none', detail) from sysfs, no subprocess."""
    normal = None
    for v in glob.glob("/sys/bus/usb/devices/*/idVendor"):
        try:
            vid = open(v).read().strip()
            pid = open(os.path.join(os.path.dirname(v), "idProduct")).read().strip()
        except OSError:
            continue
        if vid == "18d1" and pid.startswith("2d0"):
            return "accessory", f"18d1:{pid}"
        if vid == "04e8" and normal is None:
            normal = f"04e8:{pid}"
    if normal:
        return "normal", normal
    return "none", "not attached"


def iface_addrs(iface):
    rc, out = run(["ip", "-j", "addr", "show", "dev", iface], timeout=2)
    if rc == 0:
        try:
            data = json.loads(out)
            parts = []
            for a in data[0].get("addr_info", []) if data else []:
                if a.get("family") in ("inet", "inet6") and a.get("local"):
                    parts.append(f"{a['local']}/{a.get('prefixlen', '?')}")
            return " ".join(parts)
        except Exception:
            pass
    return ""


def default_route_iface():
    rc, out = run(["ip", "route", "show", "default"], timeout=2)
    if rc != 0:
        return ""
    m = re.search(r"default .*?dev (\S+)", out)
    return m.group(1) if m else ""


def make_icon():
    pm = QPixmap(256, 256)
    pm.fill(Qt.transparent)
    p = QPainter(pm)
    p.setRenderHint(QPainter.Antialiasing)
    p.setBrush(QColor("#101820"))
    p.setPen(Qt.NoPen)
    p.drawRoundedRect(8, 8, 240, 240, 54, 54)
    pen = QPen(QColor("#2ee6a8"), 16, Qt.SolidLine, Qt.RoundCap, Qt.RoundJoin)
    p.setPen(pen)
    for r, span in ((76, 90), (112, 90), (148, 90)):
        p.drawArc(128 - r, 128 - r + 22, r * 2, r * 2, 45 * 16, span * 16)
    bolt = QPolygonF([QPointF(140, 70), QPointF(102, 138), QPointF(126, 138),
                      QPointF(112, 190), QPointF(154, 116), QPointF(128, 116)])
    p.setPen(Qt.NoPen)
    p.setBrush(QColor("#f2f7f5"))
    p.drawPolygon(bolt)
    p.end()
    return QIcon(pm)


# -- widgets -----------------------------------------------------------------

class Card(QFrame):
    def __init__(self, title, parent=None):
        super().__init__(parent)
        self.setObjectName("card")
        outer = QVBoxLayout(self)
        outer.setContentsMargins(16, 13, 16, 15)
        outer.setSpacing(9)
        t = QLabel(title)
        t.setObjectName("cardTitle")
        outer.addWidget(t)
        self.body = QVBoxLayout()
        self.body.setSpacing(5)
        outer.addLayout(self.body)

    def row(self, key):
        r = QHBoxLayout()
        r.setSpacing(10)
        k = QLabel(key)
        k.setObjectName("dim")
        k.setMinimumWidth(92)
        v = QLabel("—")
        v.setObjectName("value")
        v.setWordWrap(True)
        v.setTextInteractionFlags(Qt.TextSelectableByMouse)
        r.addWidget(k)
        r.addWidget(v, 1)
        self.body.addLayout(r)
        return v


class MainWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self.setWindowTitle(f"{APP} — {TAGLINE}")
        self.setWindowIcon(make_icon())
        self.resize(1120, 760)
        self.setMinimumSize(940, 620)

        self.cfg = dict(DEFAULTS)
        self.load_config()

        self.proc = None            # QProcess of `tethercell up` we started
        self.ping_proc = None
        self.started_at = 0.0
        self.ping_ms = None
        self.last_stat = (0.0, 0, 0)   # t, rx, tx
        self.rx_rate = 0.0
        self.tx_rate = 0.0
        self.phone_info = "—"
        self.probe_supported = self.detect_probe_flags()

        self.build_ui()
        self.apply_cfg()
        self.apply_theme(self.cfg.get("theme", "Midnight"))
        self.refresh_binary_banner()

        self.tick = QTimer(self)
        self.tick.timeout.connect(self.update_state)
        self.tick.start(1000)

        self.phone_timer = QTimer(self)
        self.phone_timer.timeout.connect(self.refresh_phone_once)
        self.phone_timer.start(30000)
        QTimer.singleShot(300, self.refresh_phone_once)

        self.ping_timer = QTimer(self)
        self.ping_timer.timeout.connect(self.ping_tick)
        self.restart_ping_timer()

        self.update_state()

    # -- config --------------------------------------------------------------

    def load_config(self):
        try:
            with open(CONFIG_PATH) as f:
                self.cfg.update(json.load(f))
        except Exception:
            pass
        if self.cfg.get("iface") in (None, "", "mxtun0"):
            self.cfg["iface"] = "tcell0"

    def save_config(self):
        try:
            os.makedirs(CONFIG_DIR, exist_ok=True)
            with open(CONFIG_PATH, "w") as f:
                json.dump(self.cfg, f, indent=2)
        except Exception as e:
            self.statusBar().showMessage(f"config not saved: {e}", 5000)

    # -- ui ------------------------------------------------------------------

    def build_ui(self):
        root = QWidget()
        self.setCentralWidget(root)
        v = QVBoxLayout(root)
        v.setContentsMargins(18, 16, 18, 10)
        v.setSpacing(12)

        # header
        head = QHBoxLayout()
        head.setSpacing(12)
        logo = QLabel()
        logo.setPixmap(make_icon().pixmap(44, 44))
        head.addWidget(logo)
        titles = QVBoxLayout()
        titles.setSpacing(0)
        t1 = QLabel(APP)
        t1.setObjectName("h1")
        t2 = QLabel(TAGLINE)
        t2.setObjectName("sub")
        titles.addWidget(t1)
        titles.addWidget(t2)
        head.addLayout(titles)
        head.addStretch(1)
        self.pill = QLabel("OFFLINE")
        self.pill.setObjectName("pill")
        head.addWidget(self.pill, 0, Qt.AlignVCenter)
        v.addLayout(head)

        # banner (missing/old binary)
        self.banner = QFrame()
        self.banner.setObjectName("banner")
        b = QHBoxLayout(self.banner)
        b.setContentsMargins(14, 9, 10, 9)
        self.banner_text = QLabel("")
        self.banner_text.setWordWrap(True)
        b.addWidget(self.banner_text, 1)
        self.banner_btn = QPushButton("Install / update")
        self.banner_btn.setObjectName("ghost")
        self.banner_btn.clicked.connect(self.install_update)
        b.addWidget(self.banner_btn)
        self.banner.hide()
        v.addWidget(self.banner)

        split = QSplitter(Qt.Horizontal)
        split.setChildrenCollapsible(False)
        v.addWidget(split, 1)

        # left: link state + traffic + log
        left = QWidget()
        lv = QVBoxLayout(left)
        lv.setContentsMargins(0, 0, 0, 0)
        lv.setSpacing(12)

        link = Card("Link")
        self.st_state = link.row("state")
        self.st_iface = link.row("interface")
        self.st_usb = link.row("phone USB")
        self.st_app = link.row("phone app")
        self.st_route = link.row("default route")
        self.st_traffic = link.row("traffic")
        self.st_ping = link.row("latency")
        self.st_session = link.row("session")
        lv.addWidget(link)

        actions = QHBoxLayout()
        actions.setSpacing(10)
        self.btn_conn = QPushButton("Connect")
        self.btn_conn.setObjectName("accent")
        self.btn_conn.clicked.connect(self.connect_clicked)
        self.btn_disc = QPushButton("Disconnect")
        self.btn_disc.setObjectName("ghost")
        self.btn_disc.clicked.connect(self.disconnect_clicked)
        self.btn_disc.setEnabled(False)
        actions.addWidget(self.btn_conn)
        actions.addWidget(self.btn_disc)
        actions.addStretch(1)
        lv.addLayout(actions)

        logcard = Card("Host log")
        self.log = QPlainTextEdit()
        self.log.setReadOnly(True)
        self.log.setObjectName("log")
        self.log.setMaximumBlockCount(4000)
        self.log.setMinimumHeight(150)
        f = QFont("DejaVu Sans Mono", 9)
        f.setStyleHint(QFont.Monospace)
        self.log.setFont(f)
        logcard.body.addWidget(self.log)
        self.hoststats = QLabel("")
        self.hoststats.setObjectName("dimmono")
        logcard.body.addWidget(self.hoststats)
        lv.addWidget(logcard, 1)

        # right: settings
        settings = QWidget()
        sv = QVBoxLayout(settings)
        sv.setContentsMargins(0, 0, 0, 0)
        sv.setSpacing(12)

        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QFrame.NoFrame)
        scroll.setHorizontalScrollBarPolicy(Qt.ScrollBarAlwaysOff)
        inner = QWidget()
        inner.setMinimumWidth(320)
        iv = QVBoxLayout(inner)
        iv.setContentsMargins(0, 0, 8, 0)
        iv.setSpacing(12)

        # preset row
        prow = QHBoxLayout()
        prow.addWidget(QLabel("Preset"))
        self.preset = QComboBox()
        self.preset.addItems(list(PRESETS))
        self.preset.activated.connect(self.apply_preset)
        prow.addWidget(self.preset, 1)
        iv.addLayout(prow)

        # performance
        g = QGroupBox("Performance")
        gf = QFormLayout(g)
        gf.setLabelAlignment(Qt.AlignRight)
        gf.setRowWrapPolicy(QFormLayout.WrapLongRows)
        self.w_mtu = QSpinBox()
        self.w_mtu.setRange(576, 16000)
        self.w_mtu.setSingleStep(100)
        self.w_mtu.setSuffix(" B")
        self.w_mtu.setToolTip("TUN MTU. 16000 = max speed on the USB link; 1400 = pre-v3 packet sizes. "
                              "The phone follows the host's MSS, so large values are safe here.")
        gf.addRow("MTU", self.w_mtu)
        self.w_urbs = QSpinBox()
        self.w_urbs.setRange(0, 64)
        self.w_urbs.setSuffix(" URBs")
        self.w_urbs.setToolTip("Async bulk-IN reads kept in flight. More = better throughput under load; "
                               "0 = old one-at-a-time sync read.")
        gf.addRow("Async reads", self.w_urbs)
        iv.addWidget(g)

        # watchdog
        g = QGroupBox("Watchdog & probes")
        gf = QFormLayout(g)
        gf.setLabelAlignment(Qt.AlignRight)
        gf.setRowWrapPolicy(QFormLayout.WrapLongRows)
        self.w_dead = QSpinBox()
        self.w_dead.setRange(1, 300)
        self.w_dead.setSuffix(" s")
        self.w_dead.setToolTip("Declare the tunnel dead when data was sent and no valid packet came back for this long.")
        gf.addRow("Dead after", self.w_dead)
        self.w_wedge = QSpinBox()
        self.w_wedge.setRange(1, 300)
        self.w_wedge.setSuffix(" s")
        self.w_wedge.setToolTip("Declare the tunnel dead when every USB write has timed out for this long.")
        gf.addRow("Wedge after", self.w_wedge)
        self.w_pa = QSpinBox()
        self.w_pa.setRange(0, 120)
        self.w_pa.setSuffix(" s")
        self.w_pa.setToolTip("Silence before the host asks the phone with an ICMP echo (-probe-after). "
                             "Needs the 2026-11-16 build installed.")
        gf.addRow("Probe after", self.w_pa)
        self.w_pe = QSpinBox()
        self.w_pe.setRange(0, 120)
        self.w_pe.setSuffix(" s")
        self.w_pe.setToolTip("Minimum gap between probes, 0 = never probe (-probe-every).")
        gf.addRow("Probe every", self.w_pe)
        self.w_ping = QSpinBox()
        self.w_ping.setRange(0, 120)
        self.w_ping.setSuffix(" s")
        self.w_ping.setToolTip("TetherCell's own latency check through the tunnel (0 = off). "
                               "Runs `ping -I <iface> 1.1.1.1`.")
        self.w_ping.valueChanged.connect(self.restart_ping_timer)
        gf.addRow("GUI ping", self.w_ping)
        iv.addWidget(g)

        # routing
        g = QGroupBox("Routing")
        gf = QFormLayout(g)
        gf.setLabelAlignment(Qt.AlignRight)
        gf.setRowWrapPolicy(QFormLayout.WrapLongRows)
        self.w_route = QCheckBox("Default route via tunnel (metric 50)")
        self.w_route.setToolTip("Wins over wlan0; turn off to keep Wi-Fi as the default and only use -hostroute.")
        gf.addRow("", self.w_route)
        self.w_v6 = QCheckBox("ULA IPv6 + v6 default route")
        gf.addRow("", self.w_v6)
        self.w_hostroute = QLineEdit()
        self.w_hostroute.setPlaceholderText("optional: 1.1.1.1/32")
        self.w_hostroute.setToolTip("Install exactly one route via the TUN instead of (or as well as) the default.")
        gf.addRow("One route", self.w_hostroute)
        self.w_drop = QCheckBox("Drop root after setup (recommended)")
        self.w_drop.setToolTip("Drop to SUDO_UID once the TUN and USB fds are open and routes installed.")
        gf.addRow("", self.w_drop)
        iv.addWidget(g)

        # device
        g = QGroupBox("Device")
        gf = QFormLayout(g)
        gf.setLabelAlignment(Qt.AlignRight)
        gf.setRowWrapPolicy(QFormLayout.WrapLongRows)
        self.w_iface = QLineEdit()
        gf.addRow("Interface", self.w_iface)
        self.w_serial = QLineEdit()
        self.w_serial.setPlaceholderText("optional USB serial")
        gf.addRow("Serial", self.w_serial)
        self.w_reuse = QCheckBox("Attach to a phone already in accessory mode")
        gf.addRow("", self.w_reuse)
        self.w_wait = QSpinBox()
        self.w_wait.setRange(2, 600)
        self.w_wait.setSuffix(" s")
        gf.addRow("Wait for accessory", self.w_wait)
        self.w_halt = QCheckBox("CLEAR_HALT bulk endpoints on attach")
        gf.addRow("", self.w_halt)
        iv.addWidget(g)

        # command preview
        g = QGroupBox("Command")
        gv = QVBoxLayout(g)
        self.preview = QPlainTextEdit()
        self.preview.setReadOnly(True)
        self.preview.setMaximumHeight(74)
        f2 = QFont("DejaVu Sans Mono", 9)
        f2.setStyleHint(QFont.Monospace)
        self.preview.setFont(f2)
        self.preview.setObjectName("preview")
        gv.addWidget(self.preview)
        hb = QHBoxLayout()
        b = QPushButton("Copy")
        b.setObjectName("ghost")
        b.clicked.connect(self.copy_cmd)
        hb.addStretch(1)
        hb.addWidget(b)
        gv.addLayout(hb)
        iv.addWidget(g)
        iv.addStretch(1)
        scroll.setWidget(inner)
        sv.addWidget(scroll, 1)
        split.addWidget(left)
        split.addWidget(settings)
        split.setSizes([480, 540])

        for w in (self.w_mtu, self.w_urbs, self.w_dead, self.w_wedge, self.w_pa,
                  self.w_pe, self.w_wait):
            w.valueChanged.connect(self.cfg_changed)
        for w in (self.w_route, self.w_v6, self.w_drop, self.w_reuse, self.w_halt):
            w.toggled.connect(self.cfg_changed)
        for w in (self.w_iface, self.w_serial, self.w_hostroute):
            w.textChanged.connect(self.cfg_changed)
        self.w_ping.valueChanged.connect(self.cfg_changed)

        self.menuBar().addMenu("&Session")
        sess = self.menuBar().actions()[0].menu()
        a = QAction("Connect", self)
        a.triggered.connect(self.connect_clicked)
        sess.addAction(a)
        a = QAction("Disconnect", self)
        a.triggered.connect(self.disconnect_clicked)
        sess.addAction(a)
        sess.addSeparator()
        a = QAction("Save settings", self)
        a.triggered.connect(self.save_from_widgets)
        sess.addAction(a)
        a = QAction("Quit", self)
        a.setShortcut("Ctrl+Q")
        a.triggered.connect(self.close)
        sess.addAction(a)

        tools = self.menuBar().addMenu("&Tools")
        a = QAction("Refresh phone app info", self)
        a.triggered.connect(self.refresh_phone_once)
        tools.addAction(a)
        a = QAction("Show installed tethercell version", self)
        a.triggered.connect(self.show_versions)
        tools.addAction(a)
        a = QAction("Stop external tunnel…", self)
        a.triggered.connect(self.stop_external)
        tools.addAction(a)
        a = QAction("Install / update desktop binary…", self)
        a.triggered.connect(self.install_update)
        tools.addAction(a)
        tools.addSeparator()
        a = QAction("Open log folder", self)
        a.triggered.connect(lambda: subprocess.Popen(["xdg-open", os.path.dirname(self.log_path())]))
        tools.addAction(a)

        view = self.menuBar().addMenu("&View")
        self.theme_group = QActionGroup(self)
        self.theme_group.setExclusive(True)
        self.theme_actions = {}
        for name in THEMES:
            a = QAction(name, self, checkable=True)
            a.triggered.connect(lambda _c=False, n=name: self.apply_theme(n))
            self.theme_group.addAction(a)
            self.theme_actions[name] = a
            view.addAction(a)

        helpm = self.menuBar().addMenu("&Help")
        a = QAction("About TetherCell", self)
        a.triggered.connect(self.about)
        helpm.addAction(a)

        self.statusBar().showMessage("ready")
    # -- theme ---------------------------------------------------------------

    def apply_theme(self, name):
        if name not in THEMES:
            name = "Midnight"
        app = QApplication.instance()
        if app:
            app.setStyleSheet(build_style(name))
        self.cfg["theme"] = name
        act = getattr(self, "theme_actions", {}).get(name)
        if act:
            act.setChecked(True)
        if hasattr(self, "preset"):
            self.save_config()

    # -- widget <-> cfg ------------------------------------------------------

    def apply_cfg(self):
        c = self.cfg
        self.w_iface.setText(c["iface"])
        self.w_serial.setText(c["serial"])
        self.w_hostroute.setText(c["hostroute"])
        self.w_mtu.setValue(int(c["mtu"]))
        self.w_urbs.setValue(int(c["urbs"]))
        self.w_wait.setValue(int(c["wait"]))
        self.w_dead.setValue(int(c["dead_after"]))
        self.w_wedge.setValue(int(c["wedge_after"]))
        self.w_pa.setValue(int(c["probe_after"]))
        self.w_pe.setValue(int(c["probe_every"]))
        self.w_ping.setValue(int(c["ping_interval"]))
        self.w_route.setChecked(bool(c["route"]))
        self.w_v6.setChecked(bool(c["v6"]))
        self.w_drop.setChecked(bool(c["drop"]))
        self.w_halt.setChecked(bool(c["clear_halt"]))
        self.w_reuse.setChecked(bool(c["reuse"]))
        idx = self.preset.findText(c.get("preset", "Max speed"))
        if idx >= 0:
            self.preset.setCurrentIndex(idx)
        self.update_preview()

    def collect(self):
        return dict(
            iface=self.w_iface.text().strip() or "tcell0",
            serial=self.w_serial.text().strip(),
            hostroute=self.w_hostroute.text().strip(),
            mtu=self.w_mtu.value(),
            urbs=self.w_urbs.value(),
            wait=self.w_wait.value(),
            dead_after=self.w_dead.value(),
            wedge_after=self.w_wedge.value(),
            probe_after=self.w_pa.value(),
            probe_every=self.w_pe.value(),
            ping_interval=self.w_ping.value(),
            route=self.w_route.isChecked(),
            v6=self.w_v6.isChecked(),
            drop=self.w_drop.isChecked(),
            clear_halt=self.w_halt.isChecked(),
            reuse=self.w_reuse.isChecked(),
            preset=self.preset.currentText(),
            theme=self.cfg.get("theme", "Midnight"),
        )

    def save_from_widgets(self):
        self.cfg = self.collect()
        self.save_config()
        self.statusBar().showMessage("settings saved", 4000)

    def apply_preset(self, idx):
        name = self.preset.itemText(idx)
        for k, val in PRESETS.get(name, {}).items():
            if k == "mtu":
                self.w_mtu.setValue(val)
            elif k == "urbs":
                self.w_urbs.setValue(val)
            elif k == "probe_after":
                self.w_pa.setValue(val)
            elif k == "probe_every":
                self.w_pe.setValue(val)
        self.update_preview()

    def cfg_changed(self, *_):
        self.cfg = self.collect()
        self.update_preview()

    # -- command -------------------------------------------------------------

    def bin_path(self):
        for p in (INSTALLED, shutil.which("tethercell"), REPO_DIST):
            if p and os.path.isfile(p) and os.access(p, os.X_OK):
                return p
        return ""

    def detect_probe_flags(self):
        b = INSTALLED if os.path.exists(INSTALLED) else self.bin_path()
        if not b:
            return False
        rc, out = run([b, "run", "-h"], timeout=6)
        return "probe-after" in out

    def build_args(self):
        c = self.collect()
        b = self.bin_path() or "tethercell"
        a = [b, "up",
             "-iface", c["iface"],
             "-mtu", str(c["mtu"]),
             "-urbs", str(c["urbs"]),
             "-wait", f'{c["wait"]}s',
             "-dead-after", f'{c["dead_after"]}s',
             "-wedge-after", f'{c["wedge_after"]}s',
             f'-route={str(c["route"]).lower()}',
             f'-v6={str(c["v6"]).lower()}',
             f'-drop={str(c["drop"]).lower()}',
             f'-clear-halt={str(c["clear_halt"]).lower()}']
        if c["serial"]:
            a += ["-serial", c["serial"]]
        if c["hostroute"]:
            a += ["-hostroute", c["hostroute"]]
        if c["reuse"]:
            a += ["-reuse"]
        if self.probe_supported:
            a += ["-probe-after", f'{c["probe_after"]}s',
                  "-probe-every", f'{c["probe_every"]}s']
        return a

    def update_preview(self):
        if not hasattr(self, "preview"):
            return
        self.preview.setPlainText(" ".join(shlex.quote(x) for x in self.build_args()))

    def copy_cmd(self):
        QApplication.clipboard().setText(" ".join(shlex.quote(x) for x in self.build_args()))
        self.statusBar().showMessage("command copied", 3000)

    # -- connect / stop ------------------------------------------------------

    def iface_exists(self, iface=None):
        iface = iface or self.collect()["iface"]
        return os.path.exists(f"/sys/class/net/{iface}")

    def owned_running(self):
        return self.proc is not None

    def connect_clicked(self):
        b = self.bin_path()
        if not b:
            QMessageBox.critical(self, APP, "tethercell is not installed.\n\nRun `tethercell setup` first.")
            return
        if self.owned_running():
            self.statusBar().showMessage("already running", 3000)
            return
        if self.iface_exists(self.collect()["iface"]):
            QMessageBox.warning(self, APP,
                                f"A tunnel is already up on {self.collect()['iface']}.\n\n"
                                "Stop it first (Ctrl-C in its terminal, or Tools → Stop external tunnel).")
            return
        self.save_from_widgets()
        args = self.build_args()
        self.log.clear()
        self.append_log("$ " + " ".join(shlex.quote(x) for x in args))
        self.proc = QProcess(self)
        self.proc.setProcessChannelMode(QProcess.MergedChannels)
        self.proc.readyReadStandardOutput.connect(self.on_out)
        self.proc.finished.connect(self.on_finished)
        self.proc.errorOccurred.connect(self.on_proc_error)
        self.started_at = time.time()
        self.ping_ms = None
        self.proc.start(args[0], args[1:])
        self.set_pill("STARTING", "warn")
        self.btn_conn.setEnabled(False)
        self.btn_disc.setEnabled(True)

    def disconnect_clicked(self):
        if not self.owned_running():
            self.statusBar().showMessage("nothing started from TetherCell", 4000)
            return
        self.set_pill("STOPPING", "warn")
        self.proc.terminate()  # SIGTERM: tethercell stops cleanly and gives routes back

    def on_out(self):
        if not self.proc:
            return
        chunk = bytes(self.proc.readAllStandardOutput()).decode("utf-8", "replace")
        for line in chunk.splitlines():
            self.append_log(line)
            m = re.search(r"stats 10s: (.*)$", line)
            if m:
                self.hoststats.setText("host · " + m.group(1))

    def append_log(self, line):
        self.log.appendPlainText(line)

    def on_finished(self, code, _status):
        self.append_log(f"— tethercell exited with code {code} —")
        if code == 0:
            self.append_log("stopped by you")
        elif code == 2:
            self.append_log("bad host flags (see tethercell run -h)")
        elif code == 3:
            self.append_log("tunnel died; the supervisor reconnects by itself")
        elif code == 4:
            self.append_log("tunnel never came up: replug the phone cable and connect again")
        self.proc = None
        self.btn_conn.setEnabled(True)
        self.btn_disc.setEnabled(False)
        self.update_state()

    def on_proc_error(self, err):
        self.append_log(f"process error: {err}")
        if err == QProcess.FailedToStart:
            self.proc = None
            self.btn_conn.setEnabled(True)
            self.btn_disc.setEnabled(False)
            self.set_pill("OFFLINE", "off")

    def stop_external(self):
        rc, out = run(["pgrep", "-af", r"tethercell (up|run)"], timeout=3)
        if rc != 0 or not out.strip():
            QMessageBox.information(self, APP, "No tethercell process found.")
            return
        if QMessageBox.question(self, APP,
                                "Stop these processes?\n\n" + out.strip()) != QMessageBox.Yes:
            return
        run(["pkill", "-TERM", "-f", r"tethercell (up|run)"], timeout=5)
        self.statusBar().showMessage("SIGTERM sent", 4000)

    # -- status --------------------------------------------------------------

    def set_pill(self, text, kind):
        self.pill.setText(text)
        self.pill.setProperty("kind", kind)
        self.pill.style().unpolish(self.pill)
        self.pill.style().polish(self.pill)

    def update_state(self):
        iface = self.collect()["iface"]
        up = os.path.exists(f"/sys/class/net/{iface}")
        running = self.owned_running()

        if up:
            self.set_pill("LIVE" if running else "EXTERNAL", "ok")
            self.st_state.setText("connected" if running else "connected (started outside TetherCell)")
            self.st_iface.setText(f"{iface}  {iface_addrs(iface)}".strip())
            r = default_route_iface()
            self.st_route.setText(f"dev {r} metric 50" if r == iface else (f"dev {r}" if r else "none"))
            self.update_traffic(iface)
            self.st_session.setText(self.session_text(running))
        else:
            if running:
                self.set_pill("STARTING", "warn")
                self.st_state.setText("starting…")
            else:
                self.set_pill("OFFLINE", "off")
                self.st_state.setText("not connected")
            self.st_iface.setText("—")
            self.st_route.setText(default_route_iface() or "none")
            self.st_traffic.setText("—")
            self.st_ping.setText(f"{self.ping_ms:.0f} ms (stale)" if self.ping_ms else "—")
            self.st_session.setText("—")
            self.rx_rate = self.tx_rate = 0.0

        mode, detail = access_usb_mode()
        self.st_usb.setText({"accessory": f"accessory · {detail}",
                             "normal": f"normal · {detail} (replug flips it)",
                             "none": "not attached"}[mode])
        self.st_app.setText(self.phone_info)

    def update_traffic(self, iface):
        try:
            rx = int(open(f"/sys/class/net/{iface}/statistics/rx_bytes").read())
            tx = int(open(f"/sys/class/net/{iface}/statistics/tx_bytes").read())
        except OSError:
            return
        now = time.time()
        t0, rx0, tx0 = self.last_stat
        if t0 and now > t0:
            self.rx_rate = max(0.0, (rx - rx0) / (now - t0))
            self.tx_rate = max(0.0, (tx - tx0) / (now - t0))
        self.last_stat = (now, rx, tx)
        self.st_traffic.setText(f"↓ {human_rate(self.rx_rate)} · {human_bytes(rx)}   "
                                f"↑ {human_rate(self.tx_rate)} · {human_bytes(tx)}")

    def session_text(self, running):
        if not running or not self.started_at:
            return "external process"
        return str(timedelta(seconds=int(time.time() - self.started_at)))

    # -- ping ----------------------------------------------------------------

    def restart_ping_timer(self):
        if not hasattr(self, "ping_timer"):
            return
        iv = self.w_ping.value() if hasattr(self, "w_ping") else 5
        if iv <= 0:
            self.ping_timer.stop()
            if hasattr(self, "st_ping"):
                self.st_ping.setText("off")
        else:
            self.ping_timer.setInterval(iv * 1000)
            self.ping_timer.start()

    def ping_tick(self):
        iface = self.collect()["iface"]
        if not os.path.exists(f"/sys/class/net/{iface}") or self.ping_proc:
            return
        self.ping_proc = QProcess(self)
        self.ping_proc.setProcessChannelMode(QProcess.MergedChannels)
        self.ping_proc.finished.connect(self.ping_done)
        self.ping_proc.errorOccurred.connect(self.ping_error)
        self.ping_proc.start("ping", ["-n", "-c", "1", "-W", "1", "-I", iface, "1.1.1.1"])

    def ping_error(self, err):
        if self.ping_proc and self.ping_proc.state() == QProcess.NotRunning:
            self.ping_proc.deleteLater()
            self.ping_proc = None
            self.st_ping.setText("ping failed")

    def ping_done(self, _code, _status):
        p = self.ping_proc
        if not p:
            return
        self.ping_proc = None
        out = bytes(p.readAllStandardOutput()).decode("utf-8", "replace")
        m = re.search(r"time[=<]([\d.]+)\s*ms", out)
        if m:
            self.ping_ms = float(m.group(1))
            self.st_ping.setText(f"{self.ping_ms:.0f} ms  (1.1.1.1 via tunnel)")
        else:
            self.ping_ms = None
            self.st_ping.setText("no reply")
        p.deleteLater()

    # -- phone app -----------------------------------------------------------

    def refresh_phone_once(self):
        if self.proc:
            return
        rc, out = run(["adb", "shell", "dumpsys", "package", "com.matt.tethercell"], timeout=8)
        if rc != 0 or "Package [" not in out:
            self.phone_info = "unknown (adb not authorized / not installed)"
            self.st_app.setText(self.phone_info)
            return
        m = re.search(r"versionCode=(\d+)", out)
        v = m.group(1) if m else "?"
        self.phone_info = f"com.matt.tethercell v{v}"
        self.st_app.setText(self.phone_info)

    # -- misc ----------------------------------------------------------------

    def refresh_binary_banner(self):
        b = self.bin_path()
        if not b:
            self.banner_text.setText("tethercell is not installed on this desktop. "
                                    "Install it once: `tethercell setup`.")
            self.banner_btn.setText("Install / update")
            self.banner.show()
            self.w_pa.setEnabled(False)
            self.w_pe.setEnabled(False)
            return
        cap_ok = True
        if os.path.exists(INSTALLED):
            rc, out = run(["getcap", INSTALLED], timeout=3)
            cap_ok = "cap_net_admin" in out
        else:
            cap_ok = False
        if not os.path.exists(INSTALLED):
            self.banner_text.setText(f"{INSTALLED} is missing; TetherCell is using {b}. "
                                    "Install it once with the button.")
            self.banner_btn.setText("Install / update")
            self.banner.show()
        elif not self.probe_supported:
            self.banner_text.setText("Installed build is older than this GUI (no -probe-after/-probe-every). "
                                     "Install the 2026-11-16 build to enable the probe controls.")
            self.banner_btn.setText("Install update")
            self.banner.show()
        elif not cap_ok:
            self.banner_text.setText(f"{INSTALLED} is missing cap_net_admin: run `tethercell setup` once.")
            self.banner_btn.setText("Run setup")
            self.banner.show()
        else:
            self.banner.hide()
        self.w_pa.setEnabled(self.probe_supported)
        self.w_pe.setEnabled(self.probe_supported)

    def install_update(self):
        b = REPO_DIST if os.path.exists(REPO_DIST) else self.bin_path()
        if not b:
            return
        if QMessageBox.question(self, APP,
                                f"Run `{b} setup` now?\n\nIt pops one password dialog and installs "
                                f"{INSTALLED} + the udev rule + NM profile.") != QMessageBox.Yes:
            return
        self.log.clear()
        self.append_log(f"$ {b} setup")
        p = QProcess(self)
        p.setProcessChannelMode(QProcess.MergedChannels)
        out = []
        p.readyReadStandardOutput.connect(
            lambda: out.append(bytes(p.readAllStandardOutput()).decode("utf-8", "replace")))

        def done(*_):
            self.append_log("".join(out).strip())
            self.probe_supported = self.detect_probe_flags()
            self.refresh_binary_banner()

        p.finished.connect(done)
        p.start(b, ["setup"])

    def show_versions(self):
        rows = []
        for name, p in (("installed", INSTALLED), ("repo dist", REPO_DIST)):
            if os.path.exists(p):
                rc, out = run([p, "version"], timeout=5)
                rows.append(f"{name}: {out.strip() or '?'}")
        rows.append(f"probe flags: {'yes' if self.probe_supported else 'no'}")
        QMessageBox.information(self, APP, "\n".join(rows))

    def about(self):
        QMessageBox.about(self, f"About {APP}",
                          f"<b>{APP}</b> — {TAGLINE}<br><br>"
                          "GUI front-end for <code>tethercell</code> (AOA accessory reverse tether).<br>"
                          f"Installed binary: <code>{INSTALLED}</code><br>"
                          f"Config: <code>{CONFIG_PATH}</code><br><br>"
                          "Everything is passed to <code>tethercell up</code> as host flags; no root here.")

    def log_path(self):
        return os.path.expanduser("~/.local/state/tethercell/host.log")

    def closeEvent(self, ev):
        self.save_from_widgets()
        if self.proc:
            r = QMessageBox.question(self, APP, "A tunnel started here is still running. Stop it and quit?")
            if r != QMessageBox.Yes:
                ev.ignore()
                return
            self.proc.terminate()
            self.proc.waitForFinished(5000)
        ev.accept()


THEMES = {
    "Midnight": dict(
        bg="#0d1218", panel="#111a22", border="#1e2b36", text="#dce6e3", dim="#7f93a0",
        sub="#7f93a0", accent="#2ee6a8", ink="#06231a", log="#0a1015", logtext="#cfe4dd",
        field="#0b1116", fieldborder="#22303c", btn="#1b2733", btnborder="#2a3b4a",
        hover="#4df0b8", ok="#2ee6a8", okbg="#123a2e", warn="#f0c96a", warnbg="#3a2f12",
        warnborder="#59451c", warntext="#f0d9a8", offbg="#1b2733", offtext="#7f93a0",
        scroll="#24343f", tip="#16212b", title="#9fb4bf"),
    "Nord": dict(
        bg="#2e3440", panel="#353c4a", border="#4c566a", text="#eceff4", dim="#9aa7bd",
        sub="#9aa7bd", accent="#88c0d0", ink="#1b2b33", log="#232833", logtext="#d8dee9",
        field="#2a303c", fieldborder="#4c566a", btn="#3b4252", btnborder="#4c566a",
        hover="#8fbcbb", ok="#a3be8c", okbg="#2f3d33", warn="#ebcb8b", warnbg="#3d341f",
        warnborder="#5e5232", warntext="#f0d9a8", offbg="#3b4252", offtext="#9aa7bd",
        scroll="#4c566a", tip="#3b4252", title="#aebacd"),
    "Solarized": dict(
        bg="#002b36", panel="#073642", border="#12505f", text="#eee8d5", dim="#93a1a1",
        sub="#839496", accent="#2aa198", ink="#002b36", log="#00212b", logtext="#d5e3dd",
        field="#00252e", fieldborder="#12505f", btn="#0a4150", btnborder="#12505f",
        hover="#35bdb3", ok="#859900", okbg="#2b3a1c", warn="#b58900", warnbg="#3a2f12",
        warnborder="#6b551c", warntext="#f0d9a8", offbg="#0a4150", offtext="#93a1a1",
        scroll="#12505f", tip="#073642", title="#a9b8b6"),
    "Matrix": dict(
        bg="#050a05", panel="#0a140a", border="#1c361c", text="#c8facc", dim="#5f8a63",
        sub="#5f8a63", accent="#39ff14", ink="#032003", log="#030f05", logtext="#b7f7bd",
        field="#071207", fieldborder="#1c361c", btn="#102210", btnborder="#275227",
        hover="#6bff4d", ok="#39ff14", okbg="#10330f", warn="#c6ff3d", warnbg="#2a3310",
        warnborder="#4d5e1c", warntext="#e6ffb0", offbg="#102210", offtext="#5f8a63",
        scroll="#275227", tip="#102210", title="#7fae83"),
    "Amber CRT": dict(
        bg="#120c04", panel="#1a1206", border="#3a2a0e", text="#ffd9a0", dim="#a8814a",
        sub="#a8814a", accent="#ffb000", ink="#1d1200", log="#0d0803", logtext="#ffd9a0",
        field="#160f05", fieldborder="#3a2a0e", btn="#241807", btnborder="#4a3410",
        hover="#ffc933", ok="#ffb000", okbg="#3a2a0e", warn="#ff8c42", warnbg="#33150a",
        warnborder="#5e2a12", warntext="#ffd0b0", offbg="#241807", offtext="#a8814a",
        scroll="#4a3410", tip="#241807", title="#c9a46a"),
    "Rosé": dict(
        bg="#191724", panel="#1f1d2e", border="#403d52", text="#e0def4", dim="#908caa",
        sub="#908caa", accent="#ebbcba", ink="#191724", log="#14121f", logtext="#e0def4",
        field="#16141f", fieldborder="#403d52", btn="#262337", btnborder="#403d52",
        hover="#f2c6c4", ok="#9ccfd8", okbg="#22343a", warn="#f6c177", warnbg="#3a2f1c",
        warnborder="#5e4a2a", warntext="#ffe0b3", offbg="#262337", offtext="#908caa",
        scroll="#403d52", tip="#262337", title="#b5b1c9"),
    "Cyberpunk": dict(
        bg="#0b0614", panel="#140b22", border="#34194f", text="#e8d9ff", dim="#9b86b8",
        sub="#9b86b8", accent="#ff2bd6", ink="#1a0016", log="#080410", logtext="#e8d9ff",
        field="#0e0818", fieldborder="#34194f", btn="#1d0f31", btnborder="#46216a",
        hover="#ff63e0", ok="#00e5ff", okbg="#0c3a44", warn="#ffd166", warnbg="#3a2f12",
        warnborder="#5e4a1c", warntext="#ffe6a8", offbg="#1d0f31", offtext="#9b86b8",
        scroll="#46216a", tip="#1d0f31", title="#b9a3d6"),
    "Daylight": dict(
        bg="#f2f6f4", panel="#ffffff", border="#d3e0db", text="#17242b", dim="#5f7480",
        sub="#5f7480", accent="#0f9d75", ink="#ffffff", log="#eaf2ef", logtext="#16332a",
        field="#f7faf9", fieldborder="#cfdcd7", btn="#eef4f1", btnborder="#cfdcd7",
        hover="#0c8a66", ok="#0f9d75", okbg="#d9f2e8", warn="#a8690b", warnbg="#fdf0d5",
        warnborder="#e8c37a", warntext="#6b4405", offbg="#e8eeeb", offtext="#6b7f88",
        scroll="#c3d4cd", tip="#ffffff", title="#57707c"),
}

CSS_TEMPLATE = """
QMainWindow, QDialog { background: $bg; color: $text; }
QLabel { color: $text; background: transparent; }
QMenuBar { background: $bg; color: $text; }
QMenuBar::item:selected, QMenu::item:selected { background: $btn; }
QMenu { background: $panel; border: 1px solid $border; color: $text; }
QLabel#h1 { font-size: 22px; font-weight: 700; color: $text; letter-spacing: 0.5px; }
QLabel#sub { color: $sub; font-size: 11px; }
QLabel#dim { color: $dim; }
QLabel#dimmono { color: $dim; font-family: "DejaVu Sans Mono"; font-size: 9px; }
QLabel#value { color: $text; }
QLabel#cardTitle { color: $title; font-size: 10px; font-weight: 700; letter-spacing: 1.2px; }
QFrame#card { background: $panel; border: 1px solid $border; border-radius: 10px; }
QFrame#banner { background: $warnbg; border: 1px solid $warnborder; border-radius: 8px; color: $warntext; }
QGroupBox { background: $panel; color: $text; border: 1px solid $border; border-radius: 10px;
            margin-top: 18px; padding: 14px 10px 10px 10px; font-weight: 600; }
QGroupBox::title { subcontrol-origin: margin; left: 12px; padding: 0 6px;
                   color: $title; background: $bg; border-radius: 5px; }
QLabel#pill { padding: 5px 14px; border-radius: 12px; font-weight: 700; letter-spacing: 1px;
              font-size: 10px; background: $offbg; color: $offtext; }
QLabel#pill[kind="ok"] { background: $okbg; color: $ok; }
QLabel#pill[kind="warn"] { background: $warnbg; color: $warn; }
QLabel#pill[kind="off"] { background: $offbg; color: $offtext; }
QPushButton { background: $btn; border: 1px solid $btnborder; border-radius: 7px;
              padding: 7px 16px; color: $text; }
QPushButton:hover { border-color: $accent; }
QPushButton:disabled { color: $dim; border-color: $border; }
QPushButton#accent { background: $accent; color: $ink; border: none; font-weight: 700; }
QPushButton#accent:hover { background: $hover; }
QPushButton#ghost { background: transparent; color: $text; }
QFrame#banner QPushButton { color: $warntext; border-color: $warnborder; background: transparent; }
QSpinBox::up-button, QSpinBox::down-button { width: 0; border: none; }
QLineEdit, QSpinBox, QComboBox { background: $field; border: 1px solid $fieldborder;
                                 border-radius: 6px; padding: 5px 7px; color: $text;
                                 selection-background-color: $accent; selection-color: $ink; }
QPlainTextEdit#log, QPlainTextEdit#preview { background: $log; border: 1px solid $border;
                                             border-radius: 7px; color: $logtext; }
QCheckBox { spacing: 8px; color: $text; }
QCheckBox::indicator { width: 14px; height: 14px; border-radius: 3px;
                       border: 1px solid $fieldborder; background: $field; }
QCheckBox::indicator:checked { background: $accent; border-color: $accent; }
QCheckBox::indicator:disabled { border-color: $border; background: $btn; }
QScrollArea, QScrollArea > QWidget, QScrollArea > QWidget > QWidget { background: transparent; }
QScrollBar:vertical { background: $bg; width: 10px; }
QScrollBar::handle:vertical { background: $scroll; border-radius: 5px; min-height: 30px; }
QStatusBar { color: $dim; }
QSplitter::handle { background: transparent; }
QToolTip { background: $tip; color: $text; border: 1px solid $border; }
"""


def build_style(name):
    from string import Template
    return Template(CSS_TEMPLATE).substitute(THEMES.get(name, THEMES["Midnight"]))



def _excepthook(tp, val, tb):
    traceback.print_exception(tp, val, tb)
    try:
        QMessageBox.critical(None, APP, f"{tp.__name__}: {val}")
    except Exception:
        pass


def main():
    app = QApplication(sys.argv)
    sys.excepthook = _excepthook
    app.setApplicationName(APP)
    app.setDesktopFileName("tethercell")
    w = MainWindow()
    w.show()
    sys.exit(app.exec_())


if __name__ == "__main__":
    main()
