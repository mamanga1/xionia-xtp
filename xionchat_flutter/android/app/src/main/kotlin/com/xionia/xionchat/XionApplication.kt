package com.xionia.xionchat

import android.app.Application
import android.content.Context
import android.os.PowerManager

/**
 * Sostiene un PARTIAL_WAKE_LOCK durante toda la vida del proceso.
 *
 * Motivo: al pasar de un Service nativo propio (que tenía wakelock) a
 * flutter_background_service, se perdió esa protección. El plugin deja
 * la notificación fija (el proceso "parece" vivo) pero sin wakelock,
 * Doze puede seguir frenando el Timer de Dart y las goroutines de Go
 * apenas se apaga la pantalla — el síntoma es "recibe en foreground,
 * se queda sordo al minimizar".
 *
 * Costo: esto consume batería de forma sostenida mientras la app esté
 * en memoria. Es la contrapartida directa de no depender de push
 * notifications (no hay FCM/APNs en una red P2P soberana) — si en algún
 * momento se agrega un mecanismo de wake-on-push real, este wakelock
 * permanente debería reconsiderarse.
 */
class XionApplication : Application() {
    private var wakeLock: PowerManager.WakeLock? = null

    override fun onCreate() {
        super.onCreate()
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(
            PowerManager.PARTIAL_WAKE_LOCK,
            "xionia::process_wakelock"
        ).apply {
            setReferenceCounted(false)
            acquire()
        }
    }
}
