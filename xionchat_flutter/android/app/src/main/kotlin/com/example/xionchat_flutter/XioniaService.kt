package com.example.xionchat_flutter

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager

/**
 * Foreground service nativo que mantiene vivo el proceso Go (libxionia.so)
 * en background, incluso en dispositivos con optimizadores de batería
 * agresivos (TCL, MIUI, OnePlus, etc.).
 *
 * Estrategia de wakelock:
 * - acquire(TIMEOUT) con timeout de 12 minutos (Android mata wakelocks
 *   indefinidos en Doze mode agresivo).
 * - Un Handler renueva el wakelock cada 10 minutos mientras el servicio
 *   esté corriendo — así nunca expira en la práctica pero Android puede
 *   intervenirlo si necesita los recursos.
 * - El wakelock de XionApplication cubre el arranque del proceso antes
 *   de que este servicio esté listo.
 *
 * Nota: START_STICKY hace que Android reinicie el servicio si lo mata
 * por memoria. El wakelock se readquiere en onStartCommand.
 */
class XioniaService : Service() {

    private var wakeLock: PowerManager.WakeLock? = null
    private val handler = Handler(Looper.getMainLooper())

    // Wakelock timeout: 12 minutos. El renewRunnable lo renueva cada 10.
    private val WAKELOCK_TIMEOUT_MS = 12 * 60 * 1000L
    private val RENEW_INTERVAL_MS   = 10 * 60 * 1000L

    private val renewRunnable = object : Runnable {
        override fun run() {
            val wl = wakeLock ?: return
            if (!wl.isHeld) {
                wl.acquire(WAKELOCK_TIMEOUT_MS)
            } else {
                // release + re-acquire para resetear el timeout
                wl.release()
                wl.acquire(WAKELOCK_TIMEOUT_MS)
            }
            handler.postDelayed(this, RENEW_INTERVAL_MS)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        acquireWakeLock()
        startForeground(1, createNotification())
        return START_STICKY
    }

    private fun acquireWakeLock() {
        handler.removeCallbacks(renewRunnable)

        val pm = getSystemService(POWER_SERVICE) as PowerManager
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = pm.newWakeLock(
            PowerManager.PARTIAL_WAKE_LOCK,
            "xionia::mesh_wakelock"
        ).apply {
            setReferenceCounted(false)
            acquire(WAKELOCK_TIMEOUT_MS)
        }
        // Programar renovación periódica
        handler.postDelayed(renewRunnable, RENEW_INTERVAL_MS)
    }

    private fun createNotification(): Notification {
        val channelId = "xionia_mesh"

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                channelId,
                "XionIA Mesh",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Nodo mesh soberano activo"
                setShowBadge(false)
            }
            getSystemService(NotificationManager::class.java)
                .createNotificationChannel(channel)
        }

        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, channelId)
                .setContentTitle("XionChat")
                .setContentText("Nodo mesh conectado")
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .setOngoing(true)
                .build()
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
                .setContentTitle("XionChat")
                .setContentText("Nodo mesh conectado")
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .setOngoing(true)
                .build()
        }
    }

    override fun onDestroy() {
        handler.removeCallbacks(renewRunnable)
        wakeLock?.let { if (it.isHeld) it.release() }
        super.onDestroy()
    }
}
