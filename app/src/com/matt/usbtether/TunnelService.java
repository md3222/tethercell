package com.matt.usbtether;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.hardware.usb.UsbAccessory;
import android.hardware.usb.UsbManager;
import android.os.IBinder;
import android.os.ParcelFileDescriptor;
import android.util.Log;

public class TunnelService extends Service {
    private static final String TAG = "mxusbtether";
    private volatile Thread worker;

    // Android can launch us mid-switch while the gadget is still flapping, so the
    // first reads may fail with EIO. Keep re-opening the accessory until it sticks.
    private void loop() {
        UsbManager um = getSystemService(UsbManager.class);
        int fast = 0;
        while (fast < 20) {
            UsbAccessory acc = ours(um.getAccessoryList());
            if (acc == null) {
                if (++fast > 6) break;
                sleep(500);
                continue;
            }
            ParcelFileDescriptor pfd;
            try { pfd = um.openAccessory(acc); } catch (Exception e) { Log.e(TAG, "open", e); break; }
            if (pfd == null) { fast++; sleep(500); continue; }
            long t0 = System.currentTimeMillis();
            try {
                Log.i(TAG, "tunnel starting");
                com.matt.usbtether.core.Core.run(pfd.detachFd());
            } catch (Exception e) { Log.e(TAG, "run failed", e); }
            long up = System.currentTimeMillis() - t0;
            Log.i(TAG, "tunnel ended after " + up + " ms");
            if (up > 5000) fast = 0; else fast++;
            sleep(700);
        }
        stopSelf();
    }

    // Only ever open OUR accessory identity, never another app's (e.g. stock Tetrd's robskie/tether).
    private static UsbAccessory ours(UsbAccessory[] list) {
        if (list == null) return null;
        for (UsbAccessory a : list) {
            if ("mxlab".equals(a.getManufacturer()) && "usbtether".equals(a.getModel())) return a;
        }
        return null;
    }

        private static void sleep(long ms) { try { Thread.sleep(ms); } catch (InterruptedException ignored) {} }

    @Override public IBinder onBind(Intent i) { return null; }

    @Override public int onStartCommand(Intent i, int flags, int id) {
        NotificationChannel ch = new NotificationChannel("tun", "USB tether", NotificationManager.IMPORTANCE_LOW);
        getSystemService(NotificationManager.class).createNotificationChannel(ch);
        Notification n = new Notification.Builder(this, "tun")
            .setContentTitle("USB tether active")
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .build();
        try {
            startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
        } catch (RuntimeException e) {
            // Never crash here: a crash makes Android back off restarts for 30-90 min.
            Log.e(TAG, "startForeground refused", e);
            stopSelf();
            return START_NOT_STICKY;
        }

        if (worker == null || !worker.isAlive()) {
            worker = new Thread(this::loop, "tunnel");
            worker.start();
        }
        return START_NOT_STICKY;
    }

    @Override public void onDestroy() {
        com.matt.usbtether.core.Core.stop();
        super.onDestroy();
    }
}
