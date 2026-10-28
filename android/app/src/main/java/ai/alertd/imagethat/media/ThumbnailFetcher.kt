package ai.alertd.imagethat.media

import ai.alertd.imagethat.ui.Session
import coil3.ImageLoader
import coil3.Uri
import coil3.decode.DataSource
import coil3.decode.ImageSource
import coil3.fetch.FetchResult
import coil3.fetch.Fetcher
import coil3.fetch.SourceFetchResult
import coil3.request.Options
import kotlinx.coroutines.CancellationException
import okio.Buffer
import okio.FileSystem
import java.io.IOException
import java.net.URLDecoder
import java.net.URLEncoder

/** `vault://<sourceId>/<path>?thumb=<px>`: the key a thumbnail is requested, memory-cached and fetched by. */
object VaultUri {
    class Parsed(val sourceId: String, val path: String, val px: Int)

    private fun enc(s: String) = URLEncoder.encode(s, "UTF-8").replace("+", "%20")
    private fun dec(s: String) = URLDecoder.decode(s.replace("+", "%2B"), "UTF-8")

    fun build(sourceId: String, path: String, px: Int): String =
        "vault://${enc(sourceId)}/" + path.trim('/').split('/').filter { it.isNotEmpty() }.joinToString("/") { enc(it) } + "?thumb=$px"

    fun parse(uri: String): Parsed? {
        val rest = uri.removePrefix("vault://").takeIf { it.length != uri.length } ?: return null
        val q = rest.indexOf('?')
        val head = if (q < 0) rest else rest.substring(0, q)
        val px = if (q < 0) null else rest.substring(q + 1).removePrefix("thumb=").toIntOrNull()
        val slash = head.indexOf('/')
        if (slash < 0 || px == null) return null
        val path = "/" + head.substring(slash + 1).split('/').filter { it.isNotEmpty() }.joinToString("/") { dec(it) }
        return Parsed(dec(head.substring(0, slash)), path, px)
    }
}

/**
 * Coil fetcher that gets thumbnail bytes from the session's [ai.alertd.imagethat.data.VaultSource].
 * The bytes go to Coil as an in-memory okio Buffer: nothing here touches disk, and the app's
 * ImageLoader has no disk cache to put them in.
 */
class ThumbnailFetcher(
    private val session: Session,
    private val target: VaultUri.Parsed,
    private val options: Options,
) : Fetcher {
    override suspend fun fetch(): FetchResult {
        val source = session.source(target.sourceId) ?: throw IOException("vault is locked")
        val bytes = try {
            source.thumbnail(target.path, target.px)
        } catch (e: CancellationException) {
            throw e
        } catch (e: ai.alertd.imagethat.data.SourceError.Locked) {
            session.reportLocked(target.sourceId)
            throw IOException("vault is locked", e)
        } catch (e: ai.alertd.imagethat.data.SourceError) {
            throw IOException(e.message, e)
        }
        if (bytes.isEmpty()) throw IOException("no preview")
        return SourceFetchResult(
            source = ImageSource(Buffer().apply { write(bytes) }, FileSystem.SYSTEM),
            mimeType = "image/jpeg",
            dataSource = DataSource.NETWORK,
        )
    }

    class Factory(private val session: () -> Session) : Fetcher.Factory<Uri> {
        override fun create(data: Uri, options: Options, imageLoader: ImageLoader): Fetcher? {
            val parsed = VaultUri.parse(data.toString()) ?: return null
            return ThumbnailFetcher(session(), parsed, options)
        }
    }
}
