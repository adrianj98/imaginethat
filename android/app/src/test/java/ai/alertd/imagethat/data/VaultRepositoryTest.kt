package ai.alertd.imagethat.data

import ai.alertd.imagethat.remote.ApiClient
import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.IOException
import java.io.InputStream

/** In-memory stand-in for the gocryptfs vault: a flat map of path to bytes plus a directory set. */
class FakeLocalVault : LocalVaultApi {
    val files = HashMap<String, ByteArray>()
    val dirs = HashSet<String>()
    var peakUsed = 0L
    var retain = true
    var created = 0L
    var aborted = 0

    override fun list(path: String): String = "null"
    override fun size(path: String): Long = files[path]?.size?.toLong() ?: throw IllegalStateException("not_found: $path")
    override fun readRange(path: String, off: Long, n: Long): ByteArray {
        val b = files[path] ?: throw IllegalStateException("not_found: $path")
        if (off >= b.size) return ByteArray(0)
        return b.copyOfRange(off.toInt(), minOf(b.size.toLong(), off + n).toInt())
    }
    override fun create(path: String): LocalWriter {
        if (files.containsKey(path)) throw IllegalStateException("exists: $path")
        return object : LocalWriter {
            val buf = java.io.ByteArrayOutputStream()
            override fun write(data: ByteArray) {
                created += data.size
                if (retain) buf.write(data) else {
                    val rt = Runtime.getRuntime()
                    peakUsed = maxOf(peakUsed, rt.totalMemory() - rt.freeMemory())
                }
            }
            override fun close() { files[path] = buf.toByteArray() }
            override fun abort() { aborted++ }
        }
    }
    override fun mkdir(path: String) {
        if (!dirs.add(path)) throw IllegalStateException("exists: $path")
    }
    override fun move(from: String, to: String) { files[to] = files.remove(from)!! }
    override fun remove(path: String) { files.remove(path) }
}

class FakeSource(private val tree: Map<String, List<Item>>, private val data: Map<String, ByteArray>) : VaultSource {
    var failPaths = emptySet<String>()
    var uploads = ArrayList<Triple<String, String, Long>>()
    override suspend fun list(path: String) = tree[path] ?: emptyList()
    override suspend fun open(path: String, range: LongRange?): InputStream {
        val b = data[path] ?: throw SourceError.NotFound
        if (path in failPaths) return object : InputStream() {
            var n = 0
            override fun read(): Int = throw IOException("boom")
            override fun read(buf: ByteArray, off: Int, len: Int): Int {
                if (n >= 1000) throw IOException("boom")
                val k = minOf(len, 1000 - n)
                n += k
                return k
            }
        }
        return ByteArrayInputStream(b)
    }
    override suspend fun size(path: String) = data[path]?.size?.toLong() ?: throw SourceError.NotFound
    override suspend fun thumbnail(path: String, px: Int) = ByteArray(0)
    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        uploads += Triple(dir, name, length)
    }
    override suspend fun mkdir(path: String) {}
    override suspend fun move(from: String, to: String) {}
    override suspend fun delete(path: String) {}
}

class VaultRepositoryTest {
    private fun item(path: String, isDir: Boolean = false, size: Long = 0) =
        Item(path, path.substringAfterLast('/'), isDir, size, mediaKindOf(path.substringAfterLast('/'), isDir, ""))

    @Test fun mediaKindFromExtensionCaseInsensitive() {
        assertEquals(MediaKind.IMAGE, mediaKindOf("A.JPG", false, ""))
        assertEquals(MediaKind.IMAGE, mediaKindOf("a.heic", false, ""))
        assertEquals(MediaKind.VIDEO, mediaKindOf("a.MkV", false, ""))
        assertEquals(MediaKind.VIDEO, mediaKindOf("a.3gp", false, ""))
        assertEquals(MediaKind.OTHER, mediaKindOf("notes.txt", false, ""))
        assertEquals(MediaKind.OTHER, mediaKindOf("photos.jpg", true, ""))
        assertEquals(MediaKind.VIDEO, mediaKindOf("noext", false, "video"))
    }

    @Test fun localListMapsEntriesAndMediaKinds() = runTest {
        val v = object : LocalVaultApi by FakeLocalVault() {
            override fun list(path: String) =
                """[{"name":"a.PNG","isDir":false,"size":5,"mediaType":"image"},{"name":"sub","isDir":true,"size":0,"mediaType":""},{"name":"x.bin","isDir":false,"size":1,"mediaType":""}]"""
        }
        val items = LocalSource(v).list("/d")
        assertEquals(listOf("/d/a.PNG", "/d/sub", "/d/x.bin"), items.map { it.path })
        assertEquals(listOf(MediaKind.IMAGE, MediaKind.OTHER, MediaKind.OTHER), items.map { it.media })
        assertTrue(items[1].isDir)
        assertEquals(5L, items[0].size)
    }

    @Test fun localListNullIsEmpty() = runTest {
        assertEquals(emptyList<Item>(), LocalSource(FakeLocalVault()).list("/"))
    }

    @Test fun localOpenRangeAndSize() = runTest {
        val vault = FakeLocalVault()
        val bytes = ByteArray(600_000) { (it % 251).toByte() }
        vault.files["/f"] = bytes
        val src = LocalSource(vault)
        assertEquals(600_000L, src.size("/f"))
        assertArrayEquals(bytes, src.open("/f").readBytes())
        assertArrayEquals(bytes.copyOfRange(10, 300_011), src.open("/f", 10L..300_010L).readBytes())
        assertArrayEquals(bytes.copyOfRange(599_990, 600_000), src.open("/f", 599_990L..700_000L).readBytes())
    }

    @Test fun localErrorsMapToSourceError() = runTest {
        val src = LocalSource(FakeLocalVault())
        try { src.size("/missing"); fail() } catch (e: SourceError) { assertEquals(SourceError.NotFound, e) }
    }

    @Test fun saveOfflineReproducesTreeUnderPrefix() = runTest {
        val a = ByteArray(300_000) { 1 }
        val b = ByteArray(10) { 2 }
        val c = ByteArray(0)
        val tree = mapOf(
            "/Trip" to listOf(item("/Trip/a.jpg", size = a.size.toLong()), item("/Trip/inner", true)),
            "/Trip/inner" to listOf(item("/Trip/inner/b.txt", size = 10), item("/Trip/inner/empty", true)),
        )
        val data = mapOf("/Trip/a.jpg" to a, "/Trip/inner/b.txt" to b, "/c.mp4" to c)
        val vault = FakeLocalVault()
        val report = saveOffline(
            FakeSource(tree, data),
            listOf(item("/Trip", true), item("/c.mp4")),
            LocalSource(vault),
            "srv/vault",
        )
        assertTrue(report.failed.toString(), report.failed.isEmpty())
        assertArrayEquals(a, vault.files["/srv/vault/Trip/a.jpg"])
        assertArrayEquals(b, vault.files["/srv/vault/Trip/inner/b.txt"])
        assertArrayEquals(c, vault.files["/srv/vault/c.mp4"])
        assertTrue(vault.dirs.containsAll(listOf("/srv", "/srv/vault", "/srv/vault/Trip", "/srv/vault/Trip/inner", "/srv/vault/Trip/inner/empty")))
        assertEquals(3, report.saved.size)
        assertTrue("srv/vault/Trip/a.jpg" in report.saved)
    }

    @Test fun saveOfflineMidStreamFailureAbortsOnlyThatFile() = runTest {
        val good = ByteArray(2000) { 7 }
        val bad = ByteArray(5000) { 9 }
        val data = mapOf("/good" to good, "/bad" to bad, "/good2" to good)
        val src = FakeSource(emptyMap(), data).also { it.failPaths = setOf("/bad") }
        val vault = FakeLocalVault()
        val report = saveOffline(
            src,
            listOf(item("/good", size = 2000), item("/bad", size = 5000), item("/good2", size = 2000)),
            LocalSource(vault),
            "s/v",
        )
        assertEquals(listOf("s/v/good", "s/v/good2"), report.saved)
        assertEquals(setOf("s/v/bad"), report.failed.keys)
        assertTrue(report.failed.getValue("s/v/bad").contains("boom"))
        assertFalse(vault.files.containsKey("/s/v/bad"))
        assertEquals(1, vault.aborted)
        assertArrayEquals(good, vault.files["/s/v/good2"])
    }

    @Test fun saveOffline64MiBStreamsWithoutBuffering() = runTest {
        val size = 64L * 1024 * 1024
        val src = object : VaultSource by FakeSource(emptyMap(), emptyMap()) {
            override suspend fun open(path: String, range: LongRange?): InputStream = object : InputStream() {
                var left = size
                override fun read(): Int = if (left-- > 0) 0 else -1
                override fun read(b: ByteArray, off: Int, len: Int): Int {
                    if (left <= 0) return -1
                    val k = minOf(len.toLong(), left).toInt()
                    left -= k
                    return k
                }
            }
        }
        val vault = FakeLocalVault().also { it.retain = false }
        System.gc()
        val rt = Runtime.getRuntime()
        val base = rt.totalMemory() - rt.freeMemory()
        val report = saveOffline(src, listOf(item("/big.mp4", size = size)), LocalSource(vault), "s/v")
        assertTrue(report.failed.toString(), report.failed.isEmpty())
        assertEquals(size, vault.created)
        val growth = vault.peakUsed - base
        assertTrue("heap grew $growth", growth < 16L * 1024 * 1024)
    }

    @Test fun offlinePrefixSanitizesSegments() {
        assertEquals("My Server/Holiday", offlinePrefix("My Server", "Holiday"))
        assertEquals("a_b/c_d", offlinePrefix("a/b", "c\\d"))
        assertEquals("_/_", offlinePrefix("..", "  "))
        assertEquals("_/x", offlinePrefix("", "x"))
    }

    @Test fun remoteLockedSurfacesAsSourceErrorLocked() = runTest {
        val server = MockWebServer().also { it.start() }
        val client = ApiClient(server.url("/"), null)
        try {
            server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"vault is locked"}"""))
            try { RemoteSource(client, "ab12").list("/"); fail() } catch (e: SourceError) { assertEquals(SourceError.Locked, e) }
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"nope"}"""))
            try { RemoteSource(client, "ab12").size("/x"); fail() } catch (e: SourceError) { assertEquals(SourceError.NotFound, e) }
            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"bad"}"""))
            try { RemoteSource(client, "ab12").mkdir("/x"); fail() } catch (e: SourceError) { assertTrue(e is SourceError.Other) }
        } finally {
            client.close(); server.shutdown()
        }
    }

    @Test fun remoteMalformedBodyIsOtherNotCrash() = runTest {
        val server = MockWebServer().also { it.start() }
        val client = ApiClient(server.url("/"), null)
        try {
            server.enqueue(MockResponse().setResponseCode(200).setBody("not json"))
            try { RemoteSource(client, "ab12").list("/"); fail() } catch (e: SourceError) { assertTrue(e is SourceError.Other) }
        } finally {
            client.close(); server.shutdown()
        }
    }

    @Test fun remoteListMapsEntriesAndPaths() = runTest {
        val server = MockWebServer().also { it.start() }
        val client = ApiClient(server.url("/"), null)
        try {
            server.enqueue(MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json")
                .setBody("""{"path":"/d","parent":"/","entries":[{"name":"v","isDir":false,"size":3,"mediaType":"video"},{"name":"s","isDir":true,"size":0,"mediaType":""}]}"""))
            val items = RemoteSource(client, "ab12").list("/d")
            assertEquals(listOf("/d/v", "/d/s"), items.map { it.path })
            assertEquals(MediaKind.VIDEO, items[0].media)
            assertEquals(MediaKind.OTHER, items[1].media)
        } finally {
            client.close(); server.shutdown()
        }
    }

    @Test fun remoteUploadPassesLengthAndStreams() = runTest {
        val server = MockWebServer().also { it.start() }
        val client = ApiClient(server.url("/"), null)
        try {
            server.enqueue(MockResponse().setResponseCode(200).setBody("{}"))
            val payload = ByteArray(700_000) { 3 }
            RemoteSource(client, "ab12").upload("/d", "f.bin", ByteArrayInputStream(payload), payload.size.toLong())
            val r = server.takeRequest()
            assertEquals("700000", r.getHeader("Content-Length"))
            assertEquals(700_000L, r.bodySize)
            assertTrue(r.path!!.contains("name=f.bin"))
        } finally {
            client.close(); server.shutdown()
        }
    }

    @Test fun localUploadStreamsIntoWriter() = runTest {
        val vault = FakeLocalVault()
        val payload = ByteArray(600_000) { (it % 7).toByte() }
        LocalSource(vault).upload("/d", "f", ByteArrayInputStream(payload), payload.size.toLong())
        assertArrayEquals(payload, vault.files["/d/f"])
    }

    @Test fun repositoryDelegatesAndSavesOffline() = runTest {
        val src = FakeSource(mapOf("/" to listOf(item("/x.png", size = 3))), mapOf("/x.png" to byteArrayOf(1, 2, 3)))
        val vault = FakeLocalVault()
        val repo = VaultRepository(src, LocalSource(vault))
        assertEquals(1, repo.list("/").size)
        val rep = repo.saveOffline(repo.list("/"), "s/v")
        assertEquals(listOf("s/v/x.png"), rep.saved)
        assertArrayEquals(byteArrayOf(1, 2, 3), vault.files["/s/v/x.png"])
        try { VaultRepository(src, null).saveOffline(emptyList(), "s/v"); fail() } catch (e: SourceError) { assertTrue(e is SourceError.Other) }
    }
}
