package ir.crisis.crisis_responder

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import org.json.JSONObject
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL

/**
 * Keeps the app reachable while it is closed: a foreground service long-polls the platform's own push service
 * (no Google or third-party push) and shows each message as a notification. The topic is an unguessable
 * capability issued to the signed-in user; it travels in a header, never in the URL.
 */
class PushService : Service() {
    @Volatile private var worker: Thread? = null
    @Volatile private var running = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val prefs = getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        intent?.getStringExtra("url")?.let { url ->
            prefs.edit()
                .putString("url", url)
                .putString("topic", intent.getStringExtra("topic"))
                .putString("ongoing", intent.getStringExtra("ongoing"))
                .apply()
        }
        if (prefs.getString("topic", null) == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        channels()
        val ongoing = builder(CH_SERVICE)
            .setSmallIcon(R.drawable.ic_stat_notify)
            .setContentTitle("همراه پاد")
            .setContentText(prefs.getString("ongoing", null) ?: "آماده دریافت اعلان‌ها")
            .setOngoing(true)
            .setContentIntent(openApp())
            .build()
        when {
            Build.VERSION.SDK_INT >= 34 -> startForeground(ID_SERVICE, ongoing, ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING)
            Build.VERSION.SDK_INT >= 29 -> startForeground(ID_SERVICE, ongoing, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
            else -> startForeground(ID_SERVICE, ongoing)
        }
        if (worker?.isAlive != true) {
            running = true
            worker = Thread({ loop() }, "push-poll").also { it.start() }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        worker?.interrupt()
        super.onDestroy()
    }

    private fun loop() {
        val prefs = getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        var backoff = 5_000L
        while (running) {
            val url = prefs.getString("url", null)
            val topic = prefs.getString("topic", null)
            if (url == null || topic == null) break
            val since = prefs.getLong("since", -1)
            try {
                val conn = URL(if (since < 0) url else "$url?since=$since").openConnection() as HttpURLConnection
                conn.connectTimeout = 15_000
                conn.readTimeout = 45_000
                conn.setRequestProperty("X-Push-Topic", topic)
                conn.setRequestProperty("Accept", "application/json")
                val code = conn.responseCode
                if (code == 404) {
                    // Topic revoked (signed out elsewhere): stop until the app registers again.
                    prefs.edit().clear().apply()
                    conn.disconnect()
                    stopSelf()
                    break
                }
                if (code != 200) throw RuntimeException("HTTP $code")
                val text = InputStreamReader(conn.inputStream, Charsets.UTF_8).use { it.readText() }
                conn.disconnect()
                val body = JSONObject(text)
                val messages = body.optJSONArray("messages")
                if (messages != null) {
                    for (i in 0 until messages.length()) {
                        val m = messages.getJSONObject(i)
                        show(m.optLong("id").toInt(), m.optString("title"), m.optString("body"), m.optString("kind"))
                    }
                }
                prefs.edit().putLong("since", body.optLong("last_id", maxOf(since, 0))).apply()
                backoff = 5_000L
            } catch (e: InterruptedException) {
                break
            } catch (e: Exception) {
                try { Thread.sleep(backoff) } catch (ie: InterruptedException) { break }
                backoff = minOf(backoff * 2, 60_000L)
            }
        }
    }

    private fun show(id: Int, title: String, text: String, kind: String) {
        val n = builder(if (kind == "assignment") CH_MISSION else CH_ALERT)
            .setSmallIcon(R.drawable.ic_stat_notify)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setAutoCancel(true)
            .setContentIntent(openApp())
            .setCategory(if (kind == "assignment") Notification.CATEGORY_MESSAGE else Notification.CATEGORY_ALARM)
            .apply { if (Build.VERSION.SDK_INT < 26) legacyHeadsUp(this) }
            .build()
        (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).notify(ID_BASE + id, n)
    }

    @Suppress("DEPRECATION")
    private fun legacyHeadsUp(b: Notification.Builder) {
        b.setPriority(Notification.PRIORITY_HIGH).setDefaults(Notification.DEFAULT_ALL)
    }

    private fun openApp(): PendingIntent {
        val i = packageManager.getLaunchIntentForPackage(packageName) ?: Intent(this, MainActivity::class.java)
        i.flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        return PendingIntent.getActivity(this, 0, i, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    private fun channels() {
        if (Build.VERSION.SDK_INT < 26) return
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(NotificationChannel(CH_ALERT, "هشدارهای عمومی", NotificationManager.IMPORTANCE_HIGH))
        nm.createNotificationChannel(NotificationChannel(CH_MISSION, "مأموریت‌ها", NotificationManager.IMPORTANCE_HIGH))
        nm.createNotificationChannel(NotificationChannel(CH_SERVICE, "اتصال اعلان‌ها", NotificationManager.IMPORTANCE_MIN))
    }

    @Suppress("DEPRECATION")
    private fun builder(channel: String): Notification.Builder =
        if (Build.VERSION.SDK_INT >= 26) Notification.Builder(this, channel) else Notification.Builder(this)

    companion object {
        const val PREFS = "crisis_push"
        const val CH_ALERT = "alerts"
        const val CH_MISSION = "missions"
        const val CH_SERVICE = "push_service"
        const val ID_SERVICE = 1
        const val ID_BASE = 1000

        fun start(ctx: Context, url: String, topic: String, ongoing: String) {
            val i = Intent(ctx, PushService::class.java).putExtra("url", url).putExtra("topic", topic).putExtra("ongoing", ongoing)
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        }

        fun stop(ctx: Context) {
            ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().clear().apply()
            ctx.stopService(Intent(ctx, PushService::class.java))
        }
    }
}

/** Restarts the service after a reboot or an app update when the user is still signed in. */
class PushBootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val prefs = ctx.getSharedPreferences(PushService.PREFS, Context.MODE_PRIVATE)
        if (prefs.getString("topic", null) == null) return
        val i = Intent(ctx, PushService::class.java)
        try {
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        } catch (e: Exception) {
            // Some Android versions refuse background starts; the app restarts the service when opened.
        }
    }
}
