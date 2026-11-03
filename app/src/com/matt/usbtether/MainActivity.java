package com.matt.usbtether;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.graphics.Typeface;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

/**
 * Tapped from the launcher: a small status screen (tunnel state, counters, Stop).
 * Started by the desktop (adb am start -n, no MAIN action) or by the USB accessory attach:
 * invisible, it only (re)starts the tunnel service and finishes.
 */
public class MainActivity extends Activity {
    private final Handler ui = new Handler(Looper.getMainLooper());
    private TextView state, stats;
    private boolean visible;

    @Override protected void onCreate(Bundle b) {
        Intent i = getIntent();
        boolean launcher = i != null && Intent.ACTION_MAIN.equals(i.getAction())
                && i.hasCategory(Intent.CATEGORY_LAUNCHER);
        if (launcher) setTheme(android.R.style.Theme_DeviceDefault);
        super.onCreate(b);
        if (!launcher) {
            startTunnel();
            finish();
            return;
        }
        visible = true;
        buildUi();
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        }
        startTunnel(); // if the PC already switched the phone into accessory mode, attach now
    }

    @Override protected void onNewIntent(Intent i) {
        super.onNewIntent(i);
        startTunnel();
    }

    private void startTunnel() {
        startForegroundService(new Intent(this, TunnelService.class));
    }

    private void buildUi() {
        int pad = (int) (20 * getResources().getDisplayMetrics().density);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(pad, pad * 2, pad, pad);
        root.setBackgroundColor(0xff0f172a);

        TextView title = text("USB Tether (mine)", 24, 0xff22d3ee);
        title.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        state = text("", 18, Color.WHITE);
        stats = text("", 14, 0xff94a3b8);
        TextView help = text("Plug into the PC and run  usbtether  there.\n"
                + "The PC switches this phone into accessory mode and starts the tunnel by itself.\n"
                + "Keep the screen unlocked for the first attach.", 14, 0xff94a3b8);

        Button stop = new Button(this);
        stop.setText("Stop tunnel");
        stop.setOnClickListener(v -> {
            startService(new Intent(this, TunnelService.class).setAction(TunnelService.ACTION_STOP));
            ui.postDelayed(this::refresh, 500);
        });
        Button start = new Button(this);
        start.setText("Start / re-attach");
        start.setOnClickListener(v -> { startTunnel(); ui.postDelayed(this::refresh, 1500); });

        for (android.view.View v : new android.view.View[]{title, state, stats, start, stop, help}) {
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
            lp.topMargin = pad / 2;
            root.addView(v, lp);
        }
        setContentView(root);
    }

    private TextView text(String s, int sp, int color) {
        TextView t = new TextView(this);
        t.setText(s);
        t.setTextSize(sp);
        t.setTextColor(color);
        t.setGravity(Gravity.START);
        return t;
    }

    private void refresh() {
        if (!visible) return;
        boolean up = com.matt.usbtether.core.Core.running();
        String acc = TunnelService.accessoryAttached(this) ? "PC attached (accessory mode)" : "PC not attached";
        state.setText((up ? "● Tunnel running" : "○ Tunnel idle") + "\n" + acc);
        state.setTextColor(up ? 0xff84cc16 : 0xfff87171);
        stats.setText(com.matt.usbtether.core.Core.stats());
    }

    private final Runnable tick = new Runnable() {
        @Override public void run() { refresh(); ui.postDelayed(this, 1000); }
    };

    @Override protected void onResume() { super.onResume(); if (visible) ui.post(tick); }
    @Override protected void onPause() { super.onPause(); ui.removeCallbacks(tick); }
}
