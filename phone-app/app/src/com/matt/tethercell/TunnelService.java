package com.matt.tethercell;

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
    private static final String TAG = "tethercell";
    static final String ACTION_STOP = "com.matt.tethercell.STOP";
    private volatile boolean stopping;
    private volatile Thread worker;

    // Android can launch us mid-switch while the gadget is still flapping, so the
    // first reads may fail with EIO. Keep re-opening the accessory until it sticks.
    private void loop() {
        UsbManager um = getSystemService(UsbManager.class);
        // The desktop pre-launches this service before it sends AOA START; the phone needs up to
        // ~10 s to re-enumerate as the accessory, and the very first launch after an install is
        // slower still (ART verifying). Wait 20 s, not 3 s, so the service is there when it appears.
        int fast = 0;
        while (fast < 60 && !stopping) {
            UsbAccessory acc = ours(um.getAccessoryList());
            if (acc == null) {
                if (++fast > 40) break;
                sleep(500);
                continue;
            }
            ParcelFileDescriptor pfd;
            try {
                pfd = um.openAccessory(acc);
            } catch (SecurityException se) {
                // The accessory is there but the attach intent (which carries the permission
                // grant) has not been delivered yet. Retry inside the wait window instead of
                // giving up: the grant lands a few hundred ms later.
                Log.w(TAG, "openAccessory: no permission yet, retrying");
                fast++;
                sleep(400);
                continue;
            } catch (Exception e) { Log.e(TAG, "open", e); break; }
            if (pfd == null) { fast++; sleep(500); continue; }
            long t0 = System.currentTimeMillis();
            try {
                Log.i(TAG, "tunnel starting");
                com.matt.tethercell.core.Core.run(pfd.detachFd());
            } catch (Exception e) { Log.e(TAG, "run failed", e); }
            long up = System.currentTimeMillis() - t0;
            Log.i(TAG, "tunnel ended after " + up + " ms");
            if (up > 5000) fast = 0; else fast++;
            sleep(700);
        }
        stopSelf();
    }

    static boolean accessoryAttached(android.content.Context c) {
        return ours(c.getSystemService(UsbManager.class).getAccessoryList()) != null;
    }

    // Only ever open OUR accessory identity, never another app's (e.g. stock Tetrd's robskie/tether).
    private static UsbAccessory ours(UsbAccessory[] list) {
        if (list == null) return null;
        for (UsbAccessory a : list) {
            if ("mxlab".equals(a.getManufacturer()) && "tethercell".equals(a.getModel())) return a;
        }
        return null;
    }

        private static void sleep(long ms) { try { Thread.sleep(ms); } catch (InterruptedException ignored) {} }

    @Override public IBinder onBind(Intent i) { return null; }

    @Override public int onStartCommand(Intent i, int flags, int id) {
        if (i != null && ACTION_STOP.equals(i.getAction())) {
            stopping = true;
            com.matt.tethercell.core.Core.stop();
            stopForeground(STOP_FOREGROUND_REMOVE);
            stopSelf();
            return START_NOT_STICKY;
        }
        stopping = false;
        NotificationChannel ch = new NotificationChannel("tun", "TetherCell", NotificationManager.IMPORTANCE_LOW);
        getSystemService(NotificationManager.class).createNotificationChannel(ch);
        Notification n = new Notification.Builder(this, "tun")
            .setContentTitle("USB tether active")
            .setContentText("Tap for status")
            .setSmallIcon(R.drawable.ic_stat_tether)
            .setContentIntent(android.app.PendingIntent.getActivity(this, 0,
                new Intent(this, MainActivity.class).setAction(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER),
                android.app.PendingIntent.FLAG_IMMUTABLE))
            .addAction(new Notification.Action.Builder(null, "Stop",
                android.app.PendingIntent.getService(this, 1,
                    new Intent(this, TunnelService.class).setAction(ACTION_STOP),
                    android.app.PendingIntent.FLAG_IMMUTABLE)).build())
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
        com.matt.tethercell.core.Core.stop();
        super.onDestroy();
    }
}
