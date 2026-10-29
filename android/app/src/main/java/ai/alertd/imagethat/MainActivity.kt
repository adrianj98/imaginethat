package ai.alertd.imagethat

import ai.alertd.imagethat.ui.AppNav
import ai.alertd.imagethat.ui.theme.ImageThatTheme
import android.graphics.Color
import android.os.Bundle
import android.view.MotionEvent
import android.view.WindowManager
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.fragment.app.FragmentActivity

class MainActivity : FragmentActivity() {
    private val session get() = (application as ImageThatApp).container.session

    override fun onCreate(savedInstanceState: Bundle?) {
        // The design is dark only: light status and navigation bar icons on a transparent bar.
        enableEdgeToEdge(SystemBarStyle.dark(Color.TRANSPARENT), SystemBarStyle.dark(Color.TRANSPARENT))
        super.onCreate(savedInstanceState)
        // Keep decrypted content out of screenshots, recents and screen capture.
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        setContent {
            ImageThatTheme { AppNav(session, this) }
        }
    }

    // Any touch counts as activity for the idle auto-lock.
    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        session.touch()
        return super.dispatchTouchEvent(ev)
    }
}
