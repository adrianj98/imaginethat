package ai.alertd.imagethat.media

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.ui.TestFixture
import android.net.Uri
import androidx.media3.common.C
import androidx.media3.datasource.DataSpec
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.IOException

/** Reads ranges of a real gocryptfs file (offline vault) through the Media3 DataSource. */
class VaultDataSourceTest {
    private lateinit var fx: TestFixture
    private lateinit var source: LocalSource
    private val plain = ByteArray(10_000) { (it * 31 + it / 251).toByte() }
    private val path = "/clip.bin"

    @Before fun setUp() {
        fx = TestFixture()
        fx.local.createOffline(TestFixture.PASSWORD.toCharArray())
        source = LocalSource(MobileVaultApi(fx.local.openOffline(TestFixture.PASSWORD.toCharArray())))
        runBlocking { source.upload("/", "clip.bin", ByteArrayInputStream(plain), plain.size.toLong()) }
    }

    @After fun tearDown() = fx.cleanup()

    private fun uri() = Uri.parse("vault://offline").buildUpon().path(path).build()

    private fun read(position: Long, length: Long): Pair<Long, ByteArray> {
        val ds = VaultDataSource(source)
        val opened = ds.open(DataSpec.Builder().setUri(uri()).setPosition(position).setLength(length).build())
        val out = java.io.ByteArrayOutputStream()
        val buf = ByteArray(777)
        while (true) {
            val n = ds.read(buf, 0, buf.size)
            if (n == C.RESULT_END_OF_INPUT) break
            out.write(buf, 0, n)
        }
        ds.close()
        return opened to out.toByteArray()
    }

    @Test fun readsFirstHundredBytes() {
        val (opened, bytes) = read(0, 100)
        assertEquals(100L, opened)
        assertArrayEquals(plain.copyOfRange(0, 100), bytes)
    }

    @Test fun readsRangeCrossingAChunkBoundary() {
        val (opened, bytes) = read(4090, 110)
        assertEquals(110L, opened)
        assertArrayEquals(plain.copyOfRange(4090, 4200), bytes)
    }

    @Test fun readsToEndOfFileWhenLengthUnset() {
        val (opened, bytes) = read(9_000, C.LENGTH_UNSET.toLong())
        assertEquals(1_000L, opened)
        assertArrayEquals(plain.copyOfRange(9_000, 10_000), bytes)
        val (_, all) = read(0, C.LENGTH_UNSET.toLong())
        assertArrayEquals(plain, all)
    }

    @Test fun positionPastTheEndFailsAsIOException() {
        val ds = VaultDataSource(source)
        try {
            ds.open(DataSpec.Builder().setUri(uri()).setPosition(20_000).build())
            throw AssertionError("expected failure")
        } catch (e: IOException) {
            assertTrue(true)
        }
    }

    @Test fun missingFileFailsAsIOExceptionNotSourceError() {
        val ds = VaultDataSource(source)
        try {
            ds.open(DataSpec(Uri.parse("vault://offline").buildUpon().path("/nope.bin").build()))
            throw AssertionError("expected failure")
        } catch (e: IOException) {
            assertTrue(true)
        }
    }
}
