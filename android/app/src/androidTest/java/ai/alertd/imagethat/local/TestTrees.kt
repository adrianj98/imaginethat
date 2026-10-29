package ai.alertd.imagethat.local

import android.content.Intent
import android.net.Uri
import android.provider.DocumentsContract
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONArray

const val RW = Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION

fun testTree(authority: String = TestDocsProvider.AUTHORITY): Uri =
    DocumentsContract.buildTreeDocumentUri(authority, "r")

/** Gives the app under test a persistable read/write grant on [tree], and empties it. */
fun grantAndResetTree(tree: Uri) {
    val ctx = InstrumentationRegistry.getInstrumentation().targetContext
    ctx.grantUriPermission(ctx.packageName, tree, RW or Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or Intent.FLAG_GRANT_PREFIX_URI_PERMISSION)
    ctx.contentResolver.takePersistableUriPermission(tree, RW)
    val s = SafStorage(ctx.contentResolver, tree)
    val a = JSONArray(s.listDir(""))
    for (i in 0 until a.length()) s.removeAll(a.getJSONObject(i).getString("name"))
}

fun releaseTree(tree: Uri) {
    val ctx = InstrumentationRegistry.getInstrumentation().targetContext
    runCatching { ctx.contentResolver.releasePersistableUriPermission(tree, RW) }
}
