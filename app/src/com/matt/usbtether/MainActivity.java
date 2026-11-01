package com.matt.usbtether;

import android.app.Activity;
import android.content.Intent;
import android.hardware.usb.UsbAccessory;
import android.hardware.usb.UsbManager;
import android.os.Bundle;

public class MainActivity extends Activity {
    @Override protected void onCreate(Bundle b) {
        super.onCreate(b);
        handle(getIntent());
        finish();
    }

    @Override protected void onNewIntent(Intent i) {
        super.onNewIntent(i);
        handle(i);
    }

    private void handle(Intent i) {
        // ATTACHED or plain launcher start: the service finds the accessory itself.
        startForegroundService(new Intent(this, TunnelService.class));
    }
}
