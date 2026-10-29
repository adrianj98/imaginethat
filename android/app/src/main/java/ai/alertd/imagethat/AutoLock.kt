package ai.alertd.imagethat

import ai.alertd.imagethat.ui.LockAlarm
import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.SystemClock

/**
 * The background auto-lock alarm. The cached-app freezer suspends a backgrounded process, so the
 * in-process tick loop cannot be relied on; an alarm broadcast thaws the process and locks.
 * Inexact (setAndAllowWhileIdle): no exact-alarm permission, and it is never delivered early.
 */
class AndroidLockAlarm(private val context: Context) : LockAlarm {
    private val alarms get() = context.getSystemService(AlarmManager::class.java)

    private fun intent() = PendingIntent.getBroadcast(
        context, 0, Intent(context, AutoLockReceiver::class.java),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    override fun schedule(delayMs: Long) {
        alarms.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME_WAKEUP, SystemClock.elapsedRealtime() + delayMs, intent())
    }

    override fun cancel() = alarms.cancel(intent())
}

/** Not exported: only the app's own PendingIntent (via AlarmManager) can reach it. */
class AutoLockReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        (context.applicationContext as? ImageThatApp)?.container?.session?.onAlarm()
    }
}
