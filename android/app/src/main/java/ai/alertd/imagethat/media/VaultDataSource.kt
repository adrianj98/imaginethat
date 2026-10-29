package ai.alertd.imagethat.media

import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import android.net.Uri
import androidx.media3.common.C
import androidx.media3.common.PlaybackException
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.DataSourceException
import androidx.media3.datasource.DataSpec
import androidx.media3.datasource.TransferListener
import kotlinx.coroutines.runBlocking
import java.io.IOException
import java.io.InputStream

/**
 * Media3 [DataSource] over a [VaultSource] (used for local gocryptfs vaults): the file is read
 * in ranges straight from the engine into player memory, never through a file or a cache.
 * The vault path is the URI's path. Media3 calls [open] and [read] on its loader thread, so
 * blocking on the suspend API here is fine. [SourceError] is surfaced as [IOException].
 */
class VaultDataSource(private val source: VaultSource) : DataSource {
    private var uri: Uri? = null
    private var stream: InputStream? = null
    private var remaining = 0L

    override fun addTransferListener(transferListener: TransferListener) = Unit

    override fun open(dataSpec: DataSpec): Long {
        uri = dataSpec.uri
        val path = dataSpec.uri.path ?: throw IOException("no path in ${dataSpec.uri}")
        try {
            val total = runBlocking { source.size(path) }
            // position == total is a valid zero-length open (Media3: open returns 0, read ends).
            if (dataSpec.position > total) {
                throw DataSourceException(PlaybackException.ERROR_CODE_IO_READ_POSITION_OUT_OF_RANGE)
            }
            val end = if (dataSpec.length == C.LENGTH_UNSET.toLong()) total - 1 else minOf(total - 1, dataSpec.position + dataSpec.length - 1)
            remaining = maxOf(0L, end - dataSpec.position + 1)
            stream = if (remaining == 0L) null else runBlocking { source.open(path, dataSpec.position..end) }
        } catch (e: SourceError) {
            throw IOException(e.message, e)
        }
        return remaining
    }

    override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
        if (length == 0) return 0
        if (remaining == 0L) return C.RESULT_END_OF_INPUT
        val n = try {
            stream?.read(buffer, offset, minOf(length.toLong(), remaining).toInt()) ?: -1
        } catch (e: SourceError) {
            throw IOException(e.message, e)
        }
        if (n < 0) throw IOException("vault file ended ${remaining} bytes early")
        remaining -= n
        return n
    }

    override fun getUri(): Uri? = uri

    override fun close() {
        uri = null
        remaining = 0
        runCatching { stream?.close() }
        stream = null
    }
}
